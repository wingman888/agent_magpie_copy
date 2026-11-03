package gateway

// Session affinity: a conversation's follow-up requests go to the key or
// account that answered last, so the vendor's prompt cache can hit. The
// provider's (or the group's) Affinity says how long that holds:
//
//   - session: the whole conversation stays put;
//   - turn: only while one turn's tool results come back;
//   - off: never;
//   - auto (the default): within a turn always; across turns only when the
//     last answer read at least affinityCache tokens from the vendor's cache
//     and came no more than affinityFresh ago.
//
// Affinity only orders candidates: a kept one goes first, and the rest keep
// their order, so a failure still falls through to the next. It never
// decides whether a request succeeds.

import (
	"net/http"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const (
	affinityKeep  = 24 * time.Hour // how long a conversation is remembered
	affinityCache = 1024           // cache tokens read that make a conversation worth keeping
	affinityFresh = 5 * time.Minute
)

// convo is the conversation a request belongs to.
type convo struct {
	id     string // the conversation's id (see conversationID); "" when none
	model  string // the model as the agent asked for it
	turn   int    // user turns so far: the user's messages that carry text
	within bool   // the request carries a turn's tool results back
}

func (cv convo) key() string { return cv.id + "\x00" + cv.model }

// answer is who answered a conversation's request last, and what it read
// from the vendor's cache.
type answer struct {
	key       string // candKey of the candidate
	rest      string
	at        time.Time
	cacheRead int
}

var affinity = struct {
	sync.Mutex
	last map[string]answer // conversation and model → who answered last
}{last: map[string]answer{}}

// conversationOf is the conversation a request belongs to, and where in it.
func conversationOf(h http.Header, body []byte, from provider.Protocol, model string) convo {
	cv := convo{id: conversationID(h, body), model: model}
	if req, err := parse(from, body); err == nil {
		cv.turn, cv.within = turnsOf(req.Messages)
	}
	return cv
}

// turnsOf counts the turns in a conversation: each user message that says
// something. A request whose last message is only tool results is within a
// turn: its tool results are coming back.
func turnsOf(msgs []Message) (turn int, within bool) {
	for _, m := range msgs {
		if startsTurn(m) {
			turn++
		}
	}
	return turn, len(msgs) > 0 && !startsTurn(msgs[len(msgs)-1])
}

// startsTurn is whether a message opens a turn: a user message that says
// something. A user message carrying tool results is part of the turn that
// asked for them, even when it also has text or images.
func startsTurn(m Message) bool {
	if m.Role != "user" {
		return false
	}
	says := false
	for _, p := range m.Parts {
		switch p.Kind {
		case ToolResult:
			return false
		case Text, Image:
			says = true
		}
	}
	return says
}

// affinityMode is the mode a request's affinity goes by: the first of the
// ones given that is set (a group's, then its first member's provider's),
// else automatic. It is shown as "auto" when automatic.
func affinityMode(modes ...string) string {
	for _, m := range modes {
		switch m {
		case provider.AffinitySession, provider.AffinityTurn, provider.AffinityOff:
			return m
		}
	}
	return "auto"
}

// lastAnswer is who answered the conversation's request last, if anyone.
func lastAnswer(cv convo) (answer, bool) {
	if cv.id == "" {
		return answer{}, false
	}
	affinity.Lock()
	defer affinity.Unlock()
	a, ok := affinity.last[cv.key()]
	return a, ok
}

// answered remembers that candidate c answered the conversation's request,
// reading cacheRead tokens from the vendor's cache.
func answered(cv convo, c candidate, cacheRead int) {
	if cv.id == "" {
		return
	}
	now := time.Now()
	affinity.Lock()
	defer affinity.Unlock()
	for k, a := range affinity.last {
		if now.Sub(a.at) > affinityKeep {
			delete(affinity.last, k)
		}
	}
	affinity.last[cv.key()] = answer{key: candKey(c), rest: c.rest, at: now, cacheRead: cacheRead}
}

// affine orders a candidate list by the conversation's affinity, and tells
// why it kept or let go of the candidate that answered last. ws is the trace
// of cs, in the same order, and is reordered alike. rotating says the
// routing rotates, which across turns gives way to the rotation.
func affine(mode string, cs []candidate, ws []Weighed, lefts map[string]left, cv convo, rotating bool, now time.Time) ([]candidate, []Weighed, *AffinityTrace) {
	if len(cs) < 2 {
		return cs, ws, nil
	}
	tr := &AffinityTrace{Mode: mode, Turn: cv.turn, Within: cv.within}
	last, known := lastAnswer(cv)
	idx := -1
	if known {
		tr.Last, tr.CacheRead = last.rest, last.cacheRead
		for i, c := range cs {
			if !c.aside && candKey(c) == last.key {
				idx = i
				break
			}
		}
	}
	tr.Why = affinityWhy(mode, cs, idx, known, last, lefts, cv, rotating, now)
	switch tr.Why {
	case "session", "turn", "cache":
		tr.Kept = true
		cs, ws = toFront(cs, idx), toFront(ws, idx)
	}
	return cs, ws, tr
}

// affinityWhy is why affinity keeps (session, turn, cache) or lets go of
// (the rest) the candidate at idx, which answered last when known.
func affinityWhy(mode string, cs []candidate, idx int, known bool, last answer, lefts map[string]left, cv convo, rotating bool, now time.Time) string {
	switch {
	case mode == provider.AffinityOff:
		return "off"
	case !known:
		return "first"
	case idx < 0:
		return "gone"
	}
	c := cs[idx]
	if _, resting := restOf(c.rest); resting {
		return "resting"
	}
	if lefts[candKey(c)].used >= usedShare {
		return "spent"
	}
	switch {
	case mode == provider.AffinitySession:
		return "session"
	case mode == provider.AffinityTurn && cv.within:
		return "turn"
	case mode == provider.AffinityTurn:
		return "new-turn"
	case cv.within:
		return "turn"
	case rotating:
		return "new-turn" // the rotation moves on to the next key
	case last.cacheRead < affinityCache:
		return "no-cache"
	case now.Sub(last.at) > affinityFresh:
		return "cold"
	}
	return "cache"
}

// toFront moves the element at i to the front, the rest keeping their order.
func toFront[T any](xs []T, i int) []T {
	out := make([]T, 0, len(xs))
	out = append(out, xs[i])
	for j, x := range xs {
		if j != i {
			out = append(out, x)
		}
	}
	return out
}
