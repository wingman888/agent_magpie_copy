package gateway

// Routing groups and conversations. A request for group/<id> is planned over
// the keys and accounts of all the group's members at once, and the same
// planning is kept for a provider's own models. Both can keep a conversation
// on the key or account that answered it last (affinity), so the vendor's
// prompt cache can hit, and rotating routings move on turn by turn rather
// than request by request. What a conversation remembers is kept a day.

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// turn is where a request stands in its conversation.
type turn struct {
	conv   string // the conversation's session id from the headers, see sessionOf; "" if none
	model  string // the model the agent asked for, as it named it
	turn   int    // user turns in the conversation, this one included
	within bool   // a tool result sent back inside the turn
}

// Affinity and rotation memory.
const (
	memoryKeep     = 24 * time.Hour // how long a conversation is remembered
	cacheWindow    = 5 * time.Minute
	cacheMinTokens = 1024 // what a vendor must have read from its cache to keep the conversation
)

// rotation is where a conversation's rotating routing stands for one owner
// (a provider or a group) and model.
type rotation struct {
	n     int    // turns started so far
	start string // the candidate that leads the turn in progress
	at    time.Time
}

// answer is the candidate that last answered a conversation's model.
type answer struct {
	rest, model string
	cacheRead   int
	at          time.Time
}

var conversations = struct {
	sync.Mutex
	rot   map[string]*rotation // owner, conversation and model → rotation
	last  map[string]answer    // conversation and model → last answer
	swept time.Time
}{rot: map[string]*rotation{}, last: map[string]answer{}}

// sweepLocked forgets what has not been used for memoryKeep, at most once a
// minute.
func sweepConversationsLocked(now time.Time) {
	if now.Sub(conversations.swept) < time.Minute {
		return
	}
	conversations.swept = now
	for k, r := range conversations.rot {
		if now.Sub(r.at) > memoryKeep {
			delete(conversations.rot, k)
		}
	}
	for k, a := range conversations.last {
		if now.Sub(a.at) > memoryKeep {
			delete(conversations.last, k)
		}
	}
}

// sessionOf is the conversation's session id as the agent's headers name it:
// the first of sessionHeaders that is set, or "" when the agent sends none.
// Affinity and rotation go by this id alone; a request without one stands on
// its own (see turn.conv).
func requestSession(in http.Header) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	return ""
}

// turnOf places a request in its conversation: how many user turns it has,
// and whether it sends tool results back within one.
func requestTurn(r *http.Request, from provider.Protocol, body []byte, model string) turn {
	t := turn{conv: requestSession(r.Header), model: model}
	req, err := parse(from, body)
	if err != nil {
		return t
	}
	for _, m := range req.Messages {
		if m.Role == "user" && !turnHasResult(m.Parts) {
			t.turn++
		}
	}
	if n := len(req.Messages); n > 0 {
		last := req.Messages[n-1]
		t.within = last.Role == "user" && turnHasResult(last.Parts)
	}
	return t
}

// hasResult says whether a message carries tool results: the ones an agent
// sends back within a turn.
func turnHasResult(parts []Part) bool {
	for _, p := range parts {
		if p.Kind == ToolResult {
			return true
		}
	}
	return false
}

// ident names a candidate for the turn and affinity memory.
func candIdent(c candidate) string { return c.rest + "\x00" + c.model }

// rotate turns cs for owner: within a turn the candidate that led it stays
// first, and each new turn leads with the next one, k1, k2, k3, k1.
func (t turn) rotate(owner string, cs []candidate) []candidate {
	now := time.Now()
	key := owner + "\x00" + t.conv + "\x00" + t.model
	conversations.Lock()
	defer conversations.Unlock()
	sweepConversationsLocked(now)
	st := conversations.rot[key]
	if st == nil {
		st = &rotation{}
		conversations.rot[key] = st
	}
	idx := -1
	if t.within && st.start != "" {
		for i, c := range cs {
			if candIdent(c) == st.start {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		idx = st.n % len(cs)
		st.n++
	}
	st.start, st.at = candIdent(cs[idx]), now
	return append(append([]candidate{}, cs[idx:]...), cs[:idx]...)
}

// answered remembers which candidate answered a conversation's model, and
// what it read from the vendor's cache.
func recordAnswer(t turn, c candidate, cacheRead int) {
	if t.conv == "" {
		return
	}
	now := time.Now()
	conversations.Lock()
	defer conversations.Unlock()
	sweepConversationsLocked(now)
	conversations.last[t.conv+"\x00"+t.model] = answer{rest: c.rest, model: c.model, cacheRead: cacheRead, at: now}
}

// affinityOf decides whether the candidate that answered the conversation
// last leads this request, and says why for the trace. It moves that
// candidate to the front of the pool when it keeps it; every candidate is
// still tried in turn, so the affinity never decides whether a request
// succeeds. A single candidate is no affinity at all.
func applyAffinity(cs []candidate, order []Weighed, mode string, t turn) ([]candidate, []Weighed, *AffinityRoute) {
	pool := 0
	for _, c := range cs {
		if !c.Aside {
			pool++
		}
	}
	// no session id from the agent: nothing to keep a conversation by
	if t.conv == "" || pool < 2 || len(order) != len(cs) {
		return cs, order, nil
	}
	if mode == "" {
		mode = "auto"
	}
	a := &AffinityRoute{Mode: mode, Turn: t.turn, Within: t.within}
	now := time.Now()
	key := t.conv + "\x00" + t.model
	conversations.Lock()
	last, seen := conversations.last[key]
	conversations.Unlock()
	if seen {
		a.Last, a.CacheRead = last.rest, last.cacheRead
	}
	idx := -1
	keep := false
	switch {
	case mode == provider.AffinityOff:
		a.Why = "off"
	case !seen:
		a.Why = "first"
	default:
		for i, c := range cs {
			if !c.Aside && c.rest == last.rest && c.model == last.model {
				idx = i
				break
			}
		}
		switch {
		case idx < 0:
			a.Why = "gone"
		case order[idx].Rest != nil:
			a.Why = "resting"
		case order[idx].Known && order[idx].Used >= usedShare:
			a.Why = "spent"
		case mode == provider.AffinitySession:
			a.Why, keep = "session", true
		case t.within:
			a.Why, keep = "turn", true
		case mode == provider.AffinityTurn:
			a.Why = "new-turn"
		case last.cacheRead < cacheMinTokens:
			a.Why = "no-cache"
		case now.Sub(last.at) > cacheWindow:
			a.Why = "cold"
		default:
			a.Why, keep = "cache", true
		}
	}
	if keep && idx >= 0 {
		a.Kept = true
		cs = moveToFront(cs, idx)
		order = moveToFront(order, idx)
	}
	return cs, order, a
}

// moveToFront is xs with the element at i first, the others in their order.
func moveToFront[T any](xs []T, i int) []T {
	out := append([]T{xs[i]}, xs[:i]...)
	return append(out, xs[i+1:]...)
}

// request is one request as routing planned it.
type request struct {
	owner string // what the trace calls the model: the provider, or group/<id>
	cands []candidate
	pl    planned
	group *GroupRoute
	aff   *AffinityRoute
}

// providerRequest plans a request to one of a provider's models.
func (s *Server) providerRequest(p provider.Provider, model string, from provider.Protocol, t turn) request {
	cands, pl := s.planTurn(p, model, from, t)
	cands, order, aff := applyAffinity(cands, pl.order, p.Affinity, t)
	pl.order = order
	return request{owner: p.ID, cands: cands, pl: pl, aff: aff}
}

// groupRequest plans a request to a routing group. A group's affinity is its
// own; left unset, the first of its members' providers that has one.
func (s *Server) groupRequest(g provider.Group, ms []provider.Member, from provider.Protocol, t turn) request {
	cands, pl := s.planGroup(g, ms, from, t)
	mode := g.Affinity
	for _, m := range ms {
		if mode == "" {
			mode = m.Provider.Affinity
		}
	}
	gr := &GroupRoute{ID: g.ID, Name: g.Name, Routing: g.Routing, Affinity: g.Affinity, Auto: g.Auto}
	for _, m := range ms {
		gr.Members = append(gr.Members, m.Ref())
	}
	cands, order, aff := applyAffinity(cands, pl.order, mode, t)
	pl.order = order
	return request{owner: provider.GroupPrefix + g.ID, cands: cands, pl: pl, group: gr, aff: aff}
}

// planGroup is the candidates of a routing group's members. Under order each
// member's keys are weighed by its own routing, the members in their order.
// Otherwise every member's keys are weighed together by the group's routing.
// Either way the members' asides follow the pool, and a member's own
// fallbacks are not the group's.
func (s *Server) planGroup(g provider.Group, ms []provider.Member, from provider.Protocol, t turn) ([]candidate, planned) {
	var pl planned
	owner := provider.GroupPrefix + g.ID
	ordered := g.Routing == provider.Ordered
	var pool, asides []candidate
	var poolW []Weighed
	base := map[string]provider.Provider{} // the member's provider, by candidate
	for _, m := range ms {
		cs, left := perKeyOf(m.Provider, m.Model, from)
		n := 0
		for n < len(cs) && !cs[n].Aside {
			n++
		}
		for _, c := range cs {
			base[candIdent(c)] = m.Provider
		}
		for _, c := range left {
			base[candIdent(c)] = m.Provider
			w := weighed(c, m.Provider, weighing{}, false, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
		}
		asides = append(asides, cs[n:]...)
		main := cs[:n]
		if !ordered {
			pool = append(pool, main...)
			continue
		}
		main, wg := weighByTurn(m.Provider, main, m.Model, from, t)
		for j, c := range main {
			w := weighed(c, m.Provider, wg, false, from)
			w.Turn = j == 0 && m.Provider.Routing == provider.Rotate && len(main) > 1
			pool = append(pool, c)
			poolW = append(poolW, w)
		}
	}
	if !ordered {
		var wg weighing
		pool, wg = weighByTurn(provider.Provider{ID: owner, Routing: g.Routing}, pool, "", from, t)
		for i, c := range pool {
			w := weighed(c, base[candIdent(c)], wg, false, from)
			w.Turn = i == 0 && g.Routing == provider.Rotate && len(pool) > 1
			poolW = append(poolW, w)
		}
	}
	out := append(pool, asides...)
	order := poolW
	for _, c := range asides {
		order = append(order, weighed(c, base[candIdent(c)], weighing{}, false, from))
	}
	for i := range out {
		order[i].Aside = out[i].Aside
	}
	cs, ord := arrangeWeighed(out, order)
	pl.order = ord
	return cs, pl
}

// firstMember is the member a routing group answers for: its first usable one.
func firstGroupMember(e provider.Entry) (provider.Member, bool) {
	_, ms, ok := provider.FindGroup(strings.TrimPrefix(e.ID, provider.GroupPrefix))
	if !ok || len(ms) == 0 {
		return provider.Member{}, false
	}
	return ms[0], true
}
