package gateway

// Routing a routing group: every member's keys and accounts are weighed
// together, as the group's routing says, and a conversation is kept to the
// key or account that answered it last when its affinity says so, so the
// vendor's prompt cache can serve it. Non-group requests never come here.

import (
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// What affinity counts by: a vendor's cache must have read at least this
// many tokens of the last answer, and the answer be this recent, for a
// conversation to keep to its key across user turns.
const (
	cacheMinTokens = 1024
	cacheWindow    = 5 * time.Minute
	memoryKeep     = 24 * time.Hour
)

// clock is the time group routing reads; tests replace it.
var clock = time.Now

// groupMemory is what routing keeps between a group's requests: who
// answered each conversation last, and where each conversation's rotation
// stands. Both are kept a day.
var groupMemory = struct {
	sync.Mutex
	answers map[string]groupAnswer
	turns   map[string]groupTurn
}{answers: map[string]groupAnswer{}, turns: map[string]groupTurn{}}

type groupAnswer struct {
	rest      string // what rests after a failure: the key or account that answered
	model     string // the member's model it answered as
	at        time.Time
	cacheRead int // the vendor's cache read tokens of that answer
}

type groupTurn struct {
	idx int // where the rotation started this turn, in the pool
	at  time.Time
}

// groupCall is what a request to a routing group brings to routing.
type groupCall struct {
	group   string // the group's id
	session string // the agent's conversation id; "" when it sent none
	within  bool   // the last message brings back tool results: same turn
	turns   int    // the conversation's user turns so far
}

// groupCallOf reads a group request's conversation: its session id from the
// headers, and from the body whether it is a turn's own tool results.
func groupCallOf(h http.Header, from provider.Protocol, body []byte, group string) *groupCall {
	gc := &groupCall{group: group, session: sessionOf(h)}
	req, err := parse(from, body)
	if err != nil {
		return gc
	}
	// a user turn is a user message that says something; one that only
	// brings back tool results continues the turn it answers
	for _, m := range req.Messages {
		if m.Role == "user" && hasPartNot(m, ToolResult) {
			gc.turns++
		}
	}
	if n := len(req.Messages); n > 0 {
		last := req.Messages[n-1]
		gc.within = last.Role == "user" && hasPart(last, ToolResult)
	}
	return gc
}

// hasPart reports whether a message holds a part of kind k.
func hasPart(m Message, k Kind) bool {
	for _, p := range m.Parts {
		if p.Kind == k {
			return true
		}
	}
	return false
}

// hasPartNot reports whether a message holds a part not of kind k.
func hasPartNot(m Message, k Kind) bool {
	for _, p := range m.Parts {
		if p.Kind != k {
			return true
		}
	}
	return false
}

// sessionOf is the agent's conversation id, from the first of the session
// headers that is set.
func sessionOf(h http.Header) string {
	for _, k := range sessionHeaders {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

func (gc *groupCall) key() string { return gc.session + "\x00" + gc.group }

// answered remembers which key or account answered the conversation, for
// affinity. A request with no session id leaves no memory.
func (gc *groupCall) answered(c candidate, cacheRead int) {
	if gc.session == "" {
		return
	}
	now := clock()
	groupMemory.Lock()
	defer groupMemory.Unlock()
	groupMemory.answers[gc.key()] = groupAnswer{rest: c.rest, model: c.model, at: now, cacheRead: cacheRead}
	forgetLocked(now)
}

// forgetLocked drops the conversations and rotations not seen for a day.
func forgetLocked(now time.Time) {
	for k, a := range groupMemory.answers {
		if now.Sub(a.at) > memoryKeep {
			delete(groupMemory.answers, k)
		}
	}
	for k, t := range groupMemory.turns {
		if now.Sub(t.at) > memoryKeep {
			delete(groupMemory.turns, k)
		}
	}
}

// scored is one key or account of a group's members with what routing
// weighs it by.
type scored struct {
	c      candidate
	aside  bool
	known  bool      // the vendor said what the account has left
	used   float64   // share of the allowance counting the model, used
	renews []time.Time
	tokens float64 // tokens it served lately
}

// wg is the trace's view of a scored candidate.
func (s scored) wg(routing string) weighing {
	var w weighing
	if s.known {
		w.lefts = map[string]left{s.c.rest: {used: s.used, renews: s.renews}}
	}
	if routing == provider.GroupLeastUsed {
		w.tokens = map[string]float64{s.c.rest: s.tokens}
	}
	return w
}

// planGroup orders a group's candidates. Every member's keys and accounts
// are weighed together: in order, each member's own way; otherwise as one
// pool. Keys made for another protocol than the request's come after the
// pool, and a key that rests goes behind the ready ones of its part. The
// affinity of the conversation, when it has one, puts its last answerer
// first, if that one can take the request.
func (s *Server) planGroup(g provider.Group, ms []provider.Member, from provider.Protocol, gc *groupCall) ([]candidate, planned, *AffinityRoute) {
	now := clock()
	knownBy := map[string]map[string]provider.Allowance{}
	score := func(c candidate) scored { return scoreOf(c, now, knownBy) }

	// The tier a key suits the request by is judged across the whole group,
	// not member by member: the pool is the keys of the group's best tier,
	// and every other key is aside. Accounts are never aside.
	type member struct {
		m  provider.Member
		cs []candidate
	}
	var mems []member
	var unlisted []candidate
	best := -1
	for _, m := range ms {
		cs, unl := perKeyOf(m.Provider, m.Model, from)
		mems = append(mems, member{m: m, cs: cs})
		unlisted = append(unlisted, unl...)
		for _, c := range cs {
			if c.p.Account == nil {
				if t := keyTier(c.p, c.model, from); best < 0 || t < best {
					best = t
				}
			}
		}
	}
	for mi := range mems {
		for ci := range mems[mi].cs {
			c := &mems[mi].cs[ci]
			c.Aside = c.p.Account == nil && keyTier(c.p, c.model, from) != best
		}
	}

	var main []scored
	var aside []scored
	switch g.Routing {
	case provider.Ordered:
		// each member in turn, its own routing; a member with no key of the
		// best tier has all its keys aside
		for _, mb := range mems {
			var cs []candidate
			for _, c := range mb.cs {
				if !c.Aside {
					cs = append(cs, c)
				}
			}
			if len(cs) == 0 {
				for _, c := range mb.cs {
					aside = append(aside, score(c))
				}
				continue
			}
			ordered, _ := weighFor(mb.m.Provider, cs, mb.m.Model, from, gc)
			for _, c := range ordered {
				main = append(main, score(c))
			}
			for _, c := range mb.cs {
				if c.Aside {
					aside = append(aside, score(c))
				}
			}
		}
	default:
		var pool []scored
		for _, mb := range mems {
			for _, c := range mb.cs {
				if sc := score(c); c.Aside {
					aside = append(aside, sc)
				} else {
					pool = append(pool, sc)
				}
			}
		}
		switch g.Routing {
		case provider.Rotate:
			main = rotateTurn(gc, pool)
		case provider.GroupLeastUsed:
			main = leastUsed(pool)
		default:
			main = smartOrder(pool)
		}
	}
	sort.SliceStable(aside, func(i, j int) bool {
		return keyTier(aside[i].c.p, aside[i].c.model, from) < keyTier(aside[j].c.p, aside[j].c.model, from)
	})

	var aff *AffinityRoute
	if len(main)+len(aside) > 1 && gc.session != "" {
		var kept int
		// the group's own affinity, else its members' providers' when they
		// agree, else automatic
		eff := g
		if eff.Affinity == "" {
			eff.Affinity = memberAffinity(ms)
		}
		aff, kept = s.decideAffinity(gc, eff, main, now)
		aff.Mode = g.Affinity // the group's own setting, not a member's it inherited
		if kept >= 0 {
			first := main[kept]
			main = append([]scored{first}, slices.Delete(slices.Clone(main), kept, kept+1)...)
		}
	}

	// the pool first, then the keys made for another protocol; within each,
	// those ready before those resting
	mainReady, mainResting := splitResting(main)
	asideReady, asideResting := splitResting(aside)
	seq := slices.Concat(mainReady, mainResting, asideReady, asideResting)

	var out []candidate
	var pl planned
	for _, sc := range seq {
		w := weighed(sc.c, sc.c.p, sc.wg(g.Routing), false, from)
		if r, ok := restOf(sc.c.rest); ok {
			w.Rest = &r
		}
		pl.order = append(pl.order, w)
		out = append(out, sc.c)
	}
	for _, c := range unlisted {
		w := weighed(c, c.p, weighing{}, false, from)
		w.Unlisted = true
		pl.left = append(pl.left, w)
	}
	return out, pl, aff
}

// splitResting separates those ready from those resting after a failure,
// each keeping its order.
func splitResting(ss []scored) (ready, resting []scored) {
	for _, s := range ss {
		if _, ok := restOf(s.c.rest); ok {
			resting = append(resting, s)
		} else {
			ready = append(ready, s)
		}
	}
	return ready, resting
}

// smartOrder is the smart routing over a whole group: of those with quota
// to spare, the one whose allowance renews soonest; then those low on it,
// then the ones all but used up, each by how much is left.
func smartOrder(ss []scored) []scored {
	var fine, low, spent []scored
	for _, s := range ss {
		switch {
		case s.used >= usedShare:
			spent = append(spent, s)
		case s.used >= lowShare:
			low = append(low, s)
		default:
			fine = append(fine, s)
		}
	}
	sort.SliceStable(fine, func(i, j int) bool { return renewsBefore(fine[i].renews, fine[j].renews) })
	byUsed := func(l []scored) {
		sort.SliceStable(l, func(i, j int) bool { return l[i].used < l[j].used })
	}
	byUsed(low)
	byUsed(spent)
	return slices.Concat(fine, low, spent)
}

// renewsBefore is whether an allowance whose windows renew at a comes before
// one renewing at b: the biggest window first, to the hour, and those not
// known after those known.
func renewsBefore(ri, rj []time.Time) bool {
	for k := 0; k < len(ri) || k < len(rj); k++ {
		var a, b time.Time
		if k < len(ri) {
			a = ri[k].Truncate(time.Hour)
		}
		if k < len(rj) {
			b = rj[k].Truncate(time.Hour)
		}
		switch {
		case a.Equal(b):
			continue
		case a.IsZero() || b.IsZero():
			return b.IsZero()
		}
		return a.Before(b)
	}
	return false
}

// leastUsed orders a group's candidates by the share of their allowance
// used, then by the tokens each served lately.
func leastUsed(ss []scored) []scored {
	sort.SliceStable(ss, func(i, j int) bool {
		if ss[i].used != ss[j].used {
			return ss[i].used < ss[j].used
		}
		return ss[i].tokens < ss[j].tokens
	})
	return ss
}

// rotateStart is where a provider's rotation starts a request among its n
// (at least two) keys, for a request to model. With no conversation it counts
// requests, as it always has; with one, it counts the conversation's turns: a
// turn's requests stay where the turn started, and the next user turn moves on
// by one. Each model keeps its own rotation, so other models' requests in the
// same conversation do not move it.
func rotateStart(gc *groupCall, providerID, model string, n int) int {
	model = strings.ToLower(strings.TrimSpace(model))
	if gc == nil || gc.session == "" {
		routed.Lock()
		defer routed.Unlock()
		k := providerID + "\x00" + model
		i := routed.turn[k] % n
		routed.turn[k]++
		return i
	}
	now := clock()
	key := gc.session + "\x00provider\x00" + providerID + "\x00" + model
	groupMemory.Lock()
	defer groupMemory.Unlock()
	idx := 0
	if prev, ok := groupMemory.turns[key]; ok {
		idx = prev.idx
		if !gc.within {
			idx++
		}
		idx %= n
	}
	groupMemory.turns[key] = groupTurn{idx: idx, at: now}
	forgetLocked(now)
	return idx
}

// rotateTurn rotates a group's pool by turn, not by request: a turn's
// requests stay where the turn started, the next user turn moves on by one.
// Requests of other models do not move it, since they keep their own key.
func rotateTurn(gc *groupCall, ss []scored) []scored {
	if len(ss) < 2 {
		return ss
	}
	now := clock()
	key := gc.key()
	groupMemory.Lock()
	defer groupMemory.Unlock()
	idx := 0
	if prev, ok := groupMemory.turns[key]; ok {
		idx = prev.idx
		if !gc.within {
			idx++
		}
		idx %= len(ss)
	}
	groupMemory.turns[key] = groupTurn{idx: idx, at: now}
	forgetLocked(now)
	return slices.Concat(ss[idx:], ss[:idx])
}

// decideAffinity is whether a conversation keeps to the key or account that
// answered it last, and why it does or does not. It returns the index in
// main of that candidate when it keeps to it, else -1.
func (s *Server) decideAffinity(gc *groupCall, g provider.Group, main []scored, now time.Time) (*AffinityRoute, int) {
	aff := &AffinityRoute{Mode: g.Affinity, Turn: gc.turns, Within: gc.within}
	groupMemory.Lock()
	prev, known := groupMemory.answers[gc.key()]
	groupMemory.Unlock()
	if known {
		aff.Last, aff.CacheRead = prev.rest, prev.cacheRead
	}
	switch {
	case g.Affinity == provider.AffinityOff:
		aff.Why = "off"
		return aff, -1
	case !known:
		aff.Why = "first"
		return aff, -1
	case g.Affinity == provider.AffinityTurn && !gc.within:
		aff.Why = "new-turn"
		return aff, -1
	case g.Affinity == "" && !gc.within && prev.cacheRead < cacheMinTokens:
		aff.Why = "no-cache"
		return aff, -1
	case g.Affinity == "" && !gc.within && now.Sub(prev.at) > cacheWindow:
		aff.Why = "cold"
		return aff, -1
	}
	i := slices.IndexFunc(main, func(sc scored) bool { return sc.c.rest == prev.rest && sc.c.model == prev.model })
	switch {
	case i < 0:
		aff.Why = "gone"
		return aff, -1
	case isResting(main[i].c.rest):
		aff.Why = "resting"
		return aff, -1
	case main[i].used >= usedShare:
		aff.Why = "spent"
		return aff, -1
	case g.Affinity == "" && !gc.within && rotating(g, main[i]):
		// a rotation moves on by turn: the cache does not hold a new turn to its key
		aff.Why = "new-turn"
		return aff, -1
	}
	aff.Kept = true
	switch {
	case g.Affinity == provider.AffinitySession:
		aff.Why = "session"
	case g.Affinity == provider.AffinityTurn, gc.within:
		aff.Why = "turn"
	default:
		aff.Why = "cache"
	}
	return aff, i
}

// memberAffinity is the affinity the members' providers set, when they all
// set the same; "" (automatic) when they differ.
func memberAffinity(ms []provider.Member) string {
	var mode string
	for i, m := range ms {
		if i == 0 {
			mode = m.Provider.Affinity
		} else if m.Provider.Affinity != mode {
			return ""
		}
	}
	return mode
}

func isResting(rest string) bool {
	_, ok := restOf(rest)
	return ok
}

// scoreOf is what routing weighs a candidate by at now. known caches each
// agent's allowances across the candidates scored with it.
func scoreOf(c candidate, now time.Time, known map[string]map[string]provider.Allowance) scored {
	sc := scored{c: c, aside: c.Aside}
	if a := c.p.Account; a != nil {
		k, ok := known[a.Agent]
		if !ok {
			k = allowances(a.Agent)
			known[a.Agent] = k
		}
		if al, ok := k[a.User]; ok {
			sc.used, sc.renews = al.For(c.model, now)
			sc.known = true
		}
	}
	routed.Lock()
	sc.tokens = routed.used[c.rest].now(now)
	routed.Unlock()
	return sc
}

// rotating is whether the pool a candidate is in moves by turn under the
// group's routing: the group rotates, or, in order, the candidate's own
// provider does.
func rotating(g provider.Group, sc scored) bool {
	return g.Routing == provider.Rotate || g.Routing == provider.Ordered && sc.c.p.Routing == provider.Rotate
}

// providerAffinity keeps a direct request's conversation to the key or
// account of its provider that answered it last, by the provider's affinity,
// as a routing group keeps its own. It returns cs, with that candidate first
// when it keeps to it.
func (s *Server) providerAffinity(gc *groupCall, q provider.Provider, cs []candidate) ([]candidate, *AffinityRoute) {
	now := clock()
	known := map[string]map[string]provider.Allowance{}
	scs := make([]scored, len(cs))
	for i, c := range cs {
		scs[i] = scoreOf(c, now, known)
	}
	aff, kept := s.decideAffinity(gc, provider.Group{Routing: q.Routing, Affinity: q.Affinity}, scs, now)
	if kept < 0 {
		return cs, aff
	}
	return append([]candidate{cs[kept]}, slices.Delete(slices.Clone(cs), kept, kept+1)...), aff
}

// keyedCall is the conversation of a direct request to model on provider q:
// its affinity is kept per provider and model, as a group's is per group.
func keyedCall(gc *groupCall, q provider.Provider, model string) *groupCall {
	c := *gc
	c.group = q.ID + "/" + model
	return &c
}
