package gateway

// Session affinity: a vendor's prompt cache lives with the key or account
// that answered, so a conversation that hops between keys pays for its
// history in full each time. A request of a session goes first to whoever
// answered the session last, when that is worth it:
//
//	session   the whole session
//	turn      inside one user turn only (tool results going back)
//	off       never
//	auto      inside a turn always; past it only while the cache is there to
//	          hit — the last answer read 1024 tokens or more from it, five
//	          minutes ago or less
//
// Whichever the mode, one that is resting, all but out of quota or no longer
// a candidate is not kept to. Affinity only moves a candidate to the front:
// the others still follow, so it never decides whether a request succeeds.
//
// Rotation keeps to the same idea by turns: the tool results of a turn stay
// on the key that took it, and the next user turn moves on.

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// What counts as a cache worth keeping to, in auto mode.
const (
	cacheWorth   = 1024            // tokens read from the vendor's cache
	cacheWarm    = 5 * time.Minute // how long the vendor's cache lasts
	memoryKeep   = 24 * time.Hour  // how long sessions and conversations are remembered
	sweepEvery   = time.Minute
	affinityAuto = "auto"
)

// clock is the time affinity goes by; tests move it.
var clock = time.Now

// reqInfo is what a request says about the conversation it belongs to.
type reqInfo struct {
	session string // the agent's session id, "" when it sent none
	conv    string // the session, or what the conversation's first message hashes to
	target  string // the model as the agent asked for it
	turns   int    // user turns so far, this one included
	within  bool   // a tool result going back inside a turn

	affinity *AffinityTrace // what affinity did, once planned
}

// newReqInfo reads a request for its conversation. body is as the client
// sent it.
func newReqInfo(h http.Header, body []byte, from provider.Protocol, target string) *reqInfo {
	rq := &reqInfo{conv: conversationID(h, body), target: target}
	for _, name := range sessionHeaders {
		if v := strings.TrimSpace(h.Get(name)); v != "" {
			rq.session = v
			break
		}
	}
	rq.turns, rq.within = turnsOf(from, body)
	return rq
}

// turnsOf counts the user turns of a conversation, and says whether the
// last message is only tool results: a turn still going.
func turnsOf(from provider.Protocol, body []byte) (turns int, within bool) {
	req, err := parse(from, body)
	if err != nil || req == nil {
		return 0, false
	}
	for i, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		onlyResults := len(m.Parts) > 0
		for _, p := range m.Parts {
			if p.Kind != ToolResult {
				onlyResults = false
			}
		}
		if !onlyResults {
			turns++
		}
		if i == len(req.Messages)-1 {
			within = onlyResults
		}
	}
	return turns, within
}

type answer struct {
	id        string // candidate.id of who answered
	rest      string
	at        time.Time
	cacheRead int
}

type pin struct {
	id string
	at time.Time
}

// memory is what is remembered of sessions and conversations, for a day.
var memory = struct {
	sync.Mutex
	answers map[string]answer // session|model → who answered last
	pins    map[string]pin    // conversation|model|scope → who took the turn
	swept   time.Time
}{answers: map[string]answer{}, pins: map[string]pin{}}

func (rq *reqInfo) answerKey() string { return rq.session + "\x00" + rq.target }
func (rq *reqInfo) pinKey(scope string) string {
	return rq.conv + "\x00" + rq.target + "\x00" + scope
}

// pinned is who took the turn rotation is in, "" when none is known.
func (rq *reqInfo) pinned(scope string) string {
	memory.Lock()
	defer memory.Unlock()
	p, ok := memory.pins[rq.pinKey(scope)]
	if !ok || clock().Sub(p.at) > memoryKeep {
		return ""
	}
	return p.id
}

// remember notes who answered a request.
func (rq *reqInfo) remember(c candidate, cacheRead int) {
	now := clock()
	memory.Lock()
	defer memory.Unlock()
	if rq.session != "" {
		memory.answers[rq.answerKey()] = answer{id: c.id(), rest: c.rest, at: now, cacheRead: cacheRead}
	}
	if c.scope != "" {
		memory.pins[rq.pinKey(c.scope)] = pin{id: c.id(), at: now}
	}
	if now.Sub(memory.swept) > sweepEvery {
		memory.swept = now
		for k, a := range memory.answers {
			if now.Sub(a.at) > memoryKeep {
				delete(memory.answers, k)
			}
		}
		for k, p := range memory.pins {
			if now.Sub(p.at) > memoryKeep {
				delete(memory.pins, k)
			}
		}
	}
}

// keepTo puts the candidate that last answered the session first when
// affinity says to, and tells why it did or didn't. cs and order go
// together.
func (rq *reqInfo) keepTo(mode string, cs []candidate, order []Weighed) ([]candidate, []Weighed, *AffinityTrace) {
	if rq.session == "" {
		return cs, order, nil
	}
	tr := &AffinityTrace{Mode: mode, Turn: rq.turns, Within: rq.within}
	if tr.Mode == "" {
		tr.Mode = affinityAuto
	}
	memory.Lock()
	a, seen := memory.answers[rq.answerKey()]
	memory.Unlock()
	now := clock()
	if seen && now.Sub(a.at) > memoryKeep {
		seen = false
	}
	if seen {
		tr.Last, tr.CacheRead = a.rest, a.cacheRead
	}
	at := -1
	for i, c := range cs {
		if seen && c.id() == a.id {
			at = i
			break
		}
	}
	switch {
	case tr.Mode == provider.AffinityOff:
		tr.Why = "off"
	case !seen:
		tr.Why = "first"
	case at < 0:
		tr.Why = "gone"
	case isResting(cs[at].rest):
		tr.Why = "resting"
	case order[at].Known && order[at].Used >= usedShare:
		tr.Why = "spent"
	case tr.Mode == provider.AffinitySession:
		tr.Why, tr.Kept = "session", true
	case rq.within:
		tr.Why, tr.Kept = "turn", true
	case tr.Mode == provider.AffinityTurn:
		tr.Why = "new-turn"
	case a.cacheRead < cacheWorth:
		tr.Why = "no-cache"
	case now.Sub(a.at) > cacheWarm:
		tr.Why = "cold"
	default:
		tr.Why, tr.Kept = "cache", true
	}
	if tr.Kept && at > 0 {
		cs = append([]candidate{cs[at]}, append(append([]candidate{}, cs[:at]...), cs[at+1:]...)...)
		order = append([]Weighed{order[at]}, append(append([]Weighed{}, order[:at]...), order[at+1:]...)...)
	}
	return cs, order, tr
}

func isResting(rest string) bool {
	_, ok := restOf(rest)
	return ok
}
