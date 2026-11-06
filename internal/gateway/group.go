package gateway

// Routing groups in the gateway, and conversation affinity.
//
// An agent asks for group/<id> as if it were a model. The group's members
// are the models of several providers; their keys and accounts are weighed
// together (or member by member, when the group is in order), and the
// request goes to the first that can take it, as with any provider.
//
// Affinity keeps a conversation on the key or account that answered it last,
// so the vendor's prompt cache can hit. A conversation is named by the
// session id the agent sends in a header, and what is remembered is per
// session and per model asked for: a helper request to a small model in the
// same session does not move the main conversation. Rotation is by turn: a
// user turn is one user message that is not a tool result, and the tool
// results that come back within it stay on the key the turn started on.

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const (
	// groupPrefix is how an agent names a routing group: group/<id>.
	groupPrefix = "group/"
	// affinityKeep is how long a conversation's and its turns' memory lasts.
	affinityKeep = 24 * time.Hour
	// cacheWorth is the cached tokens a reply must have read from the vendor's
	// prompt cache for the next turn to stay on its key, and cacheFresh how
	// long after it the next turn may come.
	cacheWorth = 1024
	cacheFresh = 5 * time.Minute
)

// groupOf reads a model an agent asked for as a routing group, if it is one.
func groupOf(model string) (string, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if !strings.HasPrefix(m, groupPrefix) {
		return "", false
	}
	return strings.TrimPrefix(m, groupPrefix), true
}

// convo is the conversation a request belongs to, as far as routing goes.
type convo struct {
	session string // the session id the agent sent in a header; "" when it sent none
	model   string // the model as the agent asked
	turn    int    // user turns so far, counting this request's
	within  bool   // the request returns tool results within a turn
}

// convoOf reads the conversation a request belongs to.
func convoOf(h http.Header, body []byte, model string) *convo {
	turn, within := turnsOf(body)
	return &convo{session: sessionOf(h), model: strings.ToLower(strings.TrimSpace(model)), turn: turn, within: within}
}

// sessionOf is the session id a request names: the first non-empty of the
// headers agents send it in (see sessionHeaders).
func sessionOf(h http.Header) string {
	for _, k := range sessionHeaders {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

// affinityKey names what is remembered of a conversation: its session and the
// model asked for, which affinity is limited to.
func (cv *convo) affinityKey() string { return cv.session + "\x00" + cv.model }

// turnsOf counts the user turns of a conversation as a request sends it, and
// tells whether the request's last message is a tool result. It reads the
// message lists of all three client shapes: messages (Chat and Anthropic),
// input (Responses) and contents (Gemini).
func turnsOf(body []byte) (int, bool) {
	var v struct {
		Messages []json.RawMessage `json:"messages"`
		Contents []json.RawMessage `json:"contents"`
		Input    json.RawMessage   `json:"input"`
	}
	_ = json.Unmarshal(body, &v)
	items := v.Messages
	if len(items) == 0 {
		items = v.Contents
	}
	if len(items) == 0 && len(v.Input) > 0 && v.Input[0] == '[' {
		_ = json.Unmarshal(v.Input, &items)
	}
	turns, within := 0, false
	for i, raw := range items {
		var it struct {
			Type    string                       `json:"type"`
			Role    string                       `json:"role"`
			Content json.RawMessage              `json:"content"`
			Parts   []map[string]json.RawMessage `json:"parts"`
		}
		_ = json.Unmarshal(raw, &it)
		result := isToolResult(it.Type, it.Role, it.Content, it.Parts)
		if it.Role == "user" && !result {
			turns++
		}
		if i == len(items)-1 {
			within = result
		}
	}
	return turns, within
}

// isToolResult tells whether a message carries tool results back to the
// model, in any of the client shapes.
func isToolResult(typ, role string, content json.RawMessage, parts []map[string]json.RawMessage) bool {
	if typ == "function_call_output" || typ == "tool_result" || role == "tool" {
		return true
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return true
			}
		}
	}
	for _, p := range parts {
		if _, ok := p["functionResponse"]; ok {
			return true
		}
	}
	return false
}

// forget drops what was last touched more than affinityKeep ago.
func forget[V any](m map[string]V, now time.Time, at func(V) time.Time) {
	for k, v := range m {
		if now.Sub(at(v)) > affinityKeep {
			delete(m, k)
		}
	}
}

// rotation is where rotation of one owner's keys stands within a conversation:
// the key the current turn started on, and the turn it started in.
type rotation struct {
	cur, turn int
	at        time.Time
}

var rotations = struct {
	sync.Mutex
	m map[string]rotation // conversation and owner → rotation
}{m: map[string]rotation{}}

// rotateStart is where rotation starts for a request among n candidates of
// owner. Within a conversation it moves by turn: the turn's first request
// takes the next key, and the rest of the turn stays on it. Without one, it
// counts requests, as it always has.
func rotateStart(owner string, n int, cv *convo) int {
	if cv == nil || cv.session == "" {
		routed.Lock()
		defer routed.Unlock()
		i := routed.turn[owner] % n
		routed.turn[owner]++
		return i
	}
	rotations.Lock()
	defer rotations.Unlock()
	now := time.Now()
	forget(rotations.m, now, func(r rotation) time.Time { return r.at })
	k := cv.affinityKey() + "\x00" + owner
	st, ok := rotations.m[k]
	if ok && st.turn != cv.turn {
		st.cur = (st.cur + 1) % n
	}
	st.turn, st.at = cv.turn, now
	rotations.m[k] = st
	return st.cur % n
}

// answer is the last request of a conversation that a key answered.
type answer struct {
	rest      string // the candidate that answered, as rest names it
	turn      int    // the user turn it answered
	cacheRead int    // the cached tokens its reply read
	at        time.Time
}

var affinityMem = struct {
	sync.Mutex
	m map[string]answer // conversation and model → its last answer
}{m: map[string]answer{}}

// recordAnswer remembers that candidate rest answered the request of cv.
func recordAnswer(cv *convo, rest string, cacheRead int) {
	if cv == nil || cv.session == "" {
		return
	}
	now := time.Now()
	affinityMem.Lock()
	defer affinityMem.Unlock()
	forget(affinityMem.m, now, func(a answer) time.Time { return a.at })
	affinityMem.m[cv.affinityKey()] = answer{rest: rest, turn: cv.turn, cacheRead: cacheRead, at: now}
}

// lastAnswer is the last answer to a conversation, if it is remembered.
func lastAnswer(cv *convo) (answer, bool) {
	affinityMem.Lock()
	defer affinityMem.Unlock()
	forget(affinityMem.m, time.Now(), func(a answer) time.Time { return a.at })
	a, ok := affinityMem.m[cv.affinityKey()]
	return a, ok
}

// AffinityTrace is how affinity weighed a request, for the routing trace.
type AffinityTrace struct {
	Mode      string `json:"mode"`           // "auto", "session", "turn" or "off"
	Turn      int    `json:"turn"`           // user turns so far
	Within    bool   `json:"within"`         // the request returns tool results within a turn
	Last      string `json:"last,omitempty"` // the candidate that answered last
	CacheRead int    `json:"cacheRead"`      // cached tokens that answer read
	Kept      bool   `json:"kept"`           // the conversation was put back on Last
	Why       string `json:"why,omitempty"`  // "session", "turn" or "cache" when kept; else why not
}

// GroupTrace is the routing group a request asked for, for the routing trace.
type GroupTrace struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Routing  string   `json:"routing"`  // "", order, rotate or least-used
	Affinity string   `json:"affinity"` // as the group set it: "" is automatic
	Auto     bool     `json:"auto"`     // worked out from the providers, not the user's
	Members  []string `json:"members"`  // the members that can be used now, provider/model
}

// spent tells whether a candidate's account has used up nearly all of its
// allowance for the model: it is not kept for a conversation then.
func spentAccount(c candidate) bool {
	if c.p.Account == nil {
		return false
	}
	u, _ := allowances(c.p.Account.Agent)[c.p.Account.User].For(c.model, time.Now())
	return u >= usedShare
}

// applyAffinity puts a conversation back on the candidate that answered it
// last, when the mode and the state of that candidate allow it, and says so
// in the trace. out holds the candidates of one model, in order, with pl.order
// in step with them; the candidate kept goes first. A conversation with one
// candidate does not involve affinity at all.
func applyAffinity(out []candidate, pl *planned, cv *convo, mode string) []candidate {
	if cv == nil || len(out) < 2 {
		return out
	}
	if mode == "" {
		mode = "auto"
	}
	a := &AffinityTrace{Mode: mode, Turn: cv.turn, Within: cv.within}
	pl.affinity = a
	if mode == "off" {
		a.Why = "off"
		return out
	}
	if cv.session == "" {
		return out
	}
	rec, ok := lastAnswer(cv)
	if !ok {
		a.Why = "first"
		return out
	}
	a.Last, a.CacheRead = rec.rest, rec.cacheRead
	keep := "session"
	if mode != "session" {
		keep = "turn"
		if rec.turn != cv.turn {
			switch {
			case mode == "turn":
				a.Why = "new-turn"
				return out
			case rec.cacheRead < cacheWorth:
				a.Why = "no-cache"
				return out
			case time.Since(rec.at) > cacheFresh:
				a.Why = "cold"
				return out
			}
			keep = "cache"
		}
	}
	i := slices.IndexFunc(out, func(c candidate) bool { return c.rest == rec.rest && !c.aside })
	if i < 0 {
		a.Why = "gone"
		return out
	}
	if _, resting := restOf(rec.rest); resting {
		a.Why = "resting"
		return out
	}
	if spentAccount(out[i]) {
		a.Why = "spent"
		return out
	}
	a.Kept, a.Why = true, keep
	if i > 0 {
		out = moveToFront(out, i)
		pl.order = moveToFront(pl.order, i)
	}
	return out
}

// moveToFront returns xs with the element at i first, the others in order.
func moveToFront[T any](xs []T, i int) []T {
	out := make([]T, 0, len(xs))
	out = append(out, xs[i])
	out = append(out, xs[:i]...)
	return append(out, xs[i+1:]...)
}

// planGroup is the candidates for a routing group's request, in the order
// they are tried, and what the trace shows of it. Members are the ones
// provider.FindGroup resolved as usable. In order, each member's keys come as
// its own provider routes them, one member after another; otherwise all the
// members' keys are weighed together by the group's routing.
func (s *Server) planGroup(g provider.Group, ms []provider.Member, from provider.Protocol, cv *convo) ([]candidate, planned) {
	var pl planned
	gt := &GroupTrace{ID: g.ID, Name: g.Name, Routing: g.Routing, Affinity: g.Affinity, Auto: g.Auto}
	mode := g.Affinity
	for _, m := range ms {
		gt.Members = append(gt.Members, m.ProviderID+"/"+m.Model)
		if mode == "" {
			mode = m.Provider.Affinity // a group without its own setting takes its providers'
		}
	}
	pl.group = gt

	routing := g.Routing
	owner := groupPrefix + g.ID
	seen := map[string]bool{}
	var out []candidate
	var all []candidate
	for _, m := range ms {
		cs, left := perKeyOf(m.Provider, m.Model, from)
		var mine []candidate
		for _, c := range cs {
			if k := c.rest + "\x00" + c.model; !seen[k] {
				seen[k] = true
				mine = append(mine, c)
			}
		}
		for _, c := range left {
			w := weighed(c, m.Provider, weighing{}, false, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
		}
		if routing != provider.Ordered {
			all = append(all, mine...)
			continue
		}
		cs, wg := weighWith(m.Provider.Routing, m.ProviderID, mine, m.Model, from, cv, m.Provider.Account == nil)
		for i, c := range cs {
			w := weighed(c, c.p, wg, false, from)
			w.Turn = i == 0 && m.Provider.Routing == provider.Rotate && len(cs) > 1
			pl.order = append(pl.order, w)
		}
		out = append(out, cs...)
	}
	if routing != provider.Ordered {
		var pool, aside []candidate
		for _, c := range all {
			if c.aside {
				aside = append(aside, c)
			} else {
				pool = append(pool, c)
			}
		}
		// keys set aside for another protocol go last, unnamed ones first
		slices.SortStableFunc(aside, func(a, b candidate) int { return keyTier(a.p, a.model, from) - keyTier(b.p, b.model, from) })
		pool, wg := weighPool(routing, owner, pool, "", from, cv, false)
		out = append(pool, aside...)
		for i, c := range out {
			w := weighed(c, c.p, wg, false, from)
			w.Turn = i == 0 && routing == provider.Rotate && len(pool) > 1
			pl.order = append(pl.order, w)
		}
	}
	out = applyAffinity(out, &pl, cv, mode)
	return settle(out, pl)
}
