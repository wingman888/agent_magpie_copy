package gateway

// Conversation affinity: a conversation's later requests go first to the key
// or account that last answered it, so the vendor's prompt cache can hit
// (see provider.Affinity). Within a turn — the requests that send tool results
// back — the candidate that began the turn stays; across turns it stays only
// as the mode says. Routing rotates by turn: a request that starts a turn
// moves on to the next candidate, the requests inside it do not move it, and
// requests for other models count apart. What is remembered is kept per
// conversation and requested model, for a day after it was last asked.

import (
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const (
	affinityKeep = 24 * time.Hour  // how long a conversation is remembered
	cacheFloor   = 1024            // tokens read from the vendor's cache that make keeping worth it
	cacheFresh   = 5 * time.Minute // how long the vendor's cache is trusted to be warm
)

// AffinityTrace is how a request's conversation affinity went, for the trace.
type AffinityTrace struct {
	Mode      string `json:"mode"`   // off, session, turn, or auto
	Turn      int    `json:"turn"`   // user turns the conversation has had
	Within    bool   `json:"within"` // the request sends tool results back within a turn
	Last      string `json:"last,omitempty"`
	CacheRead int    `json:"cacheRead"` // what the last answer read from the vendor's cache
	Kept      bool   `json:"kept"`      // the last answerer was put first
	// Why is the reason: session, turn or cache when kept; off, first, new-turn,
	// no-cache, cold, resting, spent or gone when not.
	Why string `json:"why"`
}

// memory is what one conversation's affinity remembers.
type memory struct {
	rest, model string          // who last answered, and as which model
	cacheRead   int             // tokens that answer read from the vendor's cache
	at, seen    time.Time       // when it answered; when the conversation was last asked
	starts      map[string]bool // candidates that began the current turn
}

var affinities = struct {
	sync.Mutex
	m map[string]*memory // routeCtx.key → memory
}{m: map[string]*memory{}}

// routeCtx is what routing knows of a request within its conversation. The
// zero value routes one request at a time, as before conversations were kept.
type routeCtx struct {
	key    string          // conversation and requested model; "" for none
	model  string          // as the agent asked
	turns  int             // user turns the conversation has had
	within bool            // the request sends tool results back within a turn
	pins   map[string]bool // candidates that began the turn, when within
}

// newRouteCtx reads the conversation a request belongs to. A request that
// starts a turn ends the one before: nothing began the new one yet.
func newRouteCtx(in http.Header, body []byte, from provider.Protocol, model string) routeCtx {
	rc := routeCtx{key: conversationID(in, body) + "\x00" + model, model: model}
	if req, err := parse(from, body); err == nil {
		rc.turns, rc.within = turnsOf(req.Messages)
	}
	now := time.Now()
	affinities.Lock()
	defer affinities.Unlock()
	pruneLocked(now)
	m := entryLocked(rc.key, now)
	if !rc.within {
		m.starts = map[string]bool{}
		return rc
	}
	rc.pins = maps.Clone(m.starts)
	return rc
}

// pinned is the index of the candidate that began this turn, when the request
// is within it; -1 otherwise.
func (rc routeCtx) pinned(cs []candidate) int {
	if !rc.within || len(rc.pins) == 0 {
		return -1
	}
	return slices.IndexFunc(cs, func(c candidate) bool { return rc.pins[candKey(c)] })
}

// begin records that a candidate began the turn now under way.
func (rc routeCtx) begin(c candidate) {
	if rc.key == "" {
		return
	}
	affinities.Lock()
	defer affinities.Unlock()
	entryLocked(rc.key, time.Now()).starts[candKey(c)] = true
}

// recall is what the conversation remembers of its last answer; false when
// nothing answered it yet.
func recall(key string) (memory, bool) {
	affinities.Lock()
	defer affinities.Unlock()
	m := affinities.m[key]
	if m == nil || m.rest == "" {
		return memory{}, false
	}
	return *m, true
}

// remember records that candidate c answered the conversation.
func remember(key string, c candidate, cacheRead int, now time.Time) {
	if key == "" {
		return
	}
	affinities.Lock()
	defer affinities.Unlock()
	m := entryLocked(key, now)
	m.rest, m.model, m.cacheRead, m.at = c.rest, c.model, cacheRead, now
}

// entryLocked is the memory of a conversation, made if need be; the caller
// holds affinities.
func entryLocked(key string, now time.Time) *memory {
	m := affinities.m[key]
	if m == nil {
		m = &memory{starts: map[string]bool{}}
		affinities.m[key] = m
	}
	m.seen = now
	return m
}

// pruneLocked forgets the conversations not asked in affinityKeep.
func pruneLocked(now time.Time) {
	for k, m := range affinities.m {
		if now.Sub(m.seen) > affinityKeep {
			delete(affinities.m, k)
		}
	}
}

// keepAffine puts a conversation's last answerer first when its mode and the
// state of things allow, and tells why in the trace. With one candidate there
// is no affinity to speak of, and the trace has none.
func keepAffine(cs []candidate, pl planned, mode string, rc routeCtx, now time.Time) ([]candidate, planned, *AffinityTrace) {
	if len(cs) < 2 {
		return cs, pl, nil
	}
	mode = provider.NormalizeAffinity(mode)
	af := &AffinityTrace{Mode: affinityName(mode), Turn: rc.turns, Within: rc.within}
	if mode == provider.AffinityOff {
		af.Why = "off"
		return cs, pl, af
	}
	mem, ok := recall(rc.key)
	if !ok {
		af.Why = "first"
		return cs, pl, af
	}
	af.Last, af.CacheRead = mem.rest, mem.cacheRead
	i := slices.IndexFunc(cs, func(c candidate) bool { return c.rest == mem.rest && c.model == mem.model })
	if i < 0 {
		af.Why = "gone"
		return cs, pl, af
	}
	if _, resting := restOf(cs[i].rest); resting {
		af.Why = "resting"
		return cs, pl, af
	}
	if candShare(cs[i], now) >= usedShare {
		af.Why = "spent"
		return cs, pl, af
	}
	var why string
	switch {
	case mode == provider.AffinitySession:
		why = "session"
	case rc.within:
		why = "turn"
	case mode == provider.AffinityTurn:
		af.Why = "new-turn"
		return cs, pl, af
	case mem.cacheRead < cacheFloor:
		af.Why = "no-cache"
		return cs, pl, af
	case now.Sub(mem.at) > cacheFresh:
		af.Why = "cold"
		return cs, pl, af
	default:
		why = "cache"
	}
	af.Why = why
	// a key made for another protocol stays behind the pool, whoever answered
	if cs[i].aside {
		return cs, pl, af
	}
	af.Kept = true
	cs = moveFirst(cs, i)
	if k := slices.IndexFunc(pl.order, func(w Weighed) bool { return w.ID == cs[0].rest && w.Model == cs[0].model }); k >= 0 {
		pl.order = moveFirst(pl.order, k)
	}
	return cs, pl, af
}

// candShare is the share of its allowance a subscription candidate has used
// for its model; zero for a key.
func candShare(c candidate, now time.Time) float64 {
	if c.p.Account == nil {
		return 0
	}
	u, _ := allowances(c.p.Account.Agent)[c.p.Account.User].For(c.model, now)
	return u
}

func affinityName(mode string) string {
	if mode == provider.AffinityAuto {
		return "auto"
	}
	return mode
}

// turnsOf counts a conversation's user turns, and tells whether the last
// message only sends tool results back, which is a request within a turn.
func turnsOf(ms []Message) (int, bool) {
	turns := 0
	for _, m := range ms {
		if isTurn(m) {
			turns++
		}
	}
	last := len(ms) - 1
	within := last >= 0 && ms[last].Role == "user" && hasToolResult(ms[last]) && !isTurn(ms[last])
	return turns, within
}

// isTurn: a user message that says something besides tool results.
func isTurn(m Message) bool {
	if m.Role != "user" {
		return false
	}
	for _, p := range m.Parts {
		if p.Kind != ToolResult {
			return true
		}
	}
	return false
}

func hasToolResult(m Message) bool {
	for _, p := range m.Parts {
		if p.Kind == ToolResult {
			return true
		}
	}
	return false
}

// candKey names a candidate within a request: what rests, and its model.
func candKey(c candidate) string { return c.rest + "\x00" + c.model }

// candModel is the model a candidate is weighed for: the one given, or its own.
func candModel(c candidate, model string) string {
	if model != "" {
		return model
	}
	return c.model
}

// allKeys: no candidate is a subscription account.
func allKeys(cs []candidate) bool {
	for _, c := range cs {
		if c.p.Account != nil {
			return false
		}
	}
	return true
}

// rotated starts a list at index n, the rest after it in order.
func rotated(cs []candidate, n int) []candidate {
	return append(append([]candidate{}, cs[n:]...), cs[:n]...)
}

// moveFirst is xs with the element at i first, the others in order.
func moveFirst[T any](xs []T, i int) []T {
	out := make([]T, 0, len(xs))
	out = append(out, xs[i])
	out = append(out, xs[:i]...)
	return append(out, xs[i+1:]...)
}
