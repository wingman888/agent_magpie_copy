package gateway

// Session affinity: a conversation stays on the key or account that last
// answered it, so the vendor's prompt cache for it isn't thrown away by a
// request landing somewhere else. What was answered by whom is kept per
// conversation and per scope — a provider's model, or a routing group — so
// the small side requests an agent makes (a title) leave the main
// conversation's owner alone.
//
// Routing "in turn" hands out user turns, not requests: a new turn goes to
// the one after the last answerer, and the tool results sent back within a
// turn stay on the key that began it.

import (
	"net/http"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const (
	// affinityKeep is how long who answered a conversation is remembered.
	affinityKeep = 24 * time.Hour
	// cacheWarm is how long after an answer the vendor's cache is taken to
	// be warm, and cacheWorth the tokens it must have read for staying on
	// it to be worth anything.
	cacheWarm  = 5 * time.Minute
	cacheWorth = 1024
)

// Why a request stayed with, or left, the last answerer.
const (
	whyOff     = "off"
	whyFirst   = "first"
	whyGone    = "gone"
	whyResting = "resting"
	whySpent   = "spent"
	whySession = "session"
	whyTurn    = "turn"
	whyNewTurn = "new-turn"
	whyNoCache = "no-cache"
	whyCold    = "cold"
	whyCache   = "cache"
)

// convo is what a request says about its conversation.
type convo struct {
	id     string
	turns  int  // user turns so far, this request's included
	within bool // an agent sending tool results back, in the middle of a turn
}

// conversationOf reads a request for its conversation.
func conversationOf(from provider.Protocol, h http.Header, body []byte) convo {
	cv := convo{id: conversationID(h, body)}
	req, err := parse(from, body)
	if err != nil {
		return cv
	}
	for _, m := range req.Messages {
		if m.Role == "user" && !onlyResults(m) {
			cv.turns++
		}
	}
	if n := len(req.Messages); n > 0 {
		last := req.Messages[n-1]
		cv.within = last.Role == "user" && onlyResults(last)
	}
	return cv
}

// onlyResults: a message that holds tool results and nothing a user typed.
func onlyResults(m Message) bool {
	results := false
	for _, p := range m.Parts {
		switch p.Kind {
		case ToolResult:
			results = true
		case Text, Image:
			return false
		}
	}
	return results
}

// answered is who last answered a conversation within a scope.
type answered struct {
	last      string // the candidate's rest id
	turn      int    // the user turn it answered
	at        time.Time
	cacheRead int // tokens it read from the vendor's cache
}

var answers = struct {
	sync.Mutex
	m map[string]answered
}{m: map[string]answered{}}

func answerKey(conversation, scope string) string { return conversation + "\x00" + scope }

// lastAnswer is who answered a conversation in a scope, when that's known.
func lastAnswer(conversation, scope string) (answered, bool) {
	answers.Lock()
	defer answers.Unlock()
	a, ok := answers.m[answerKey(conversation, scope)]
	if !ok || time.Since(a.at) > affinityKeep {
		return answered{}, false
	}
	return a, true
}

// answeredBy records a request that succeeded.
func answeredBy(conversation, scope, rest string, turn, cacheRead int) {
	now := time.Now()
	answers.Lock()
	defer answers.Unlock()
	for k, a := range answers.m {
		if now.Sub(a.at) > affinityKeep {
			delete(answers.m, k)
		}
	}
	answers.m[answerKey(conversation, scope)] = answered{rest, turn, now, cacheRead}
}

// Affinity is what the routing trace tells of staying with the last answerer.
type Affinity struct {
	Mode      string     `json:"mode"` // the provider's or group's setting
	Turn      int        `json:"turn"` // user turns so far
	Within    bool       `json:"within,omitempty"`
	Last      string     `json:"last,omitempty"`
	LastTurn  int        `json:"lastTurn,omitempty"`
	At        *time.Time `json:"at,omitempty"`
	CacheRead int        `json:"cacheRead,omitempty"`
	Kept      bool       `json:"kept,omitempty"` // the last answerer was put first
	Why       string     `json:"why"`
}

// affinityOf judges whether the conversation stays with its last answerer.
// cs are all the candidates, resting ones too; spent says which are all but
// used up. rotate is whether routing hands out turns.
func affinityOf(mode string, rotate bool, cv convo, rec answered, has bool, cs []candidate, spent func(rest string) bool, now time.Time) Affinity {
	a := Affinity{Mode: mode, Turn: cv.turns, Within: cv.within}
	if has {
		a.Last, a.LastTurn, a.CacheRead = rec.last, rec.turn, rec.cacheRead
		at := rec.at
		a.At = &at
	}
	gone := true
	for _, c := range cs {
		if has && c.rest == rec.last {
			gone = false
		}
	}
	switch {
	case mode == provider.AffinityOff:
		a.Why = whyOff
	case !has:
		a.Why = whyFirst
	case gone:
		a.Why = whyGone
	case func() bool { _, r := restOf(rec.last); return r }():
		a.Why = whyResting
	case spent(rec.last):
		a.Why = whySpent
	case mode == provider.AffinitySession:
		a.Why = whySession
	case cv.within:
		a.Why = whyTurn
	case mode == provider.AffinityTurn || rotate:
		a.Why = whyNewTurn
	case rec.cacheRead < cacheWorth:
		a.Why = whyNoCache
	case now.Sub(rec.at) > cacheWarm:
		a.Why = whyCold
	default:
		a.Why = whyCache
	}
	a.Kept = a.Why == whySession || a.Why == whyTurn || a.Why == whyCache
	return a
}

// nextTurn rotates cs so the one after the last answerer comes first,
// skipping those resting. It gives nil when there's no one to go on from.
func nextTurn(mode string, rec answered, has bool, cs []candidate) []candidate {
	if mode == provider.AffinityOff || !has {
		return nil
	}
	at := -1
	for i, c := range cs {
		if c.rest == rec.last {
			at = i
		}
	}
	if at < 0 {
		return nil
	}
	start := (at + 1) % len(cs)
	for k := 1; k <= len(cs); k++ {
		j := (at + k) % len(cs)
		if _, r := restOf(cs[j].rest); !r {
			start = j
			break
		}
	}
	return append(append([]candidate{}, cs[start:]...), cs[:start]...)
}
