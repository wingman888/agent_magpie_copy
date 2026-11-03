package gateway

// Session affinity: a conversation's next request goes first to the key or
// account that answered its last one, so the prompt cache the vendor built up
// for it is read rather than written again. It is a preference only: every
// candidate is still tried in order, so it never decides whether a request
// succeeds. What magpie keeps of a conversation is per model (an agent's
// small-model side requests, such as titles, don't move the main one), and it
// is forgotten a day after the conversation was last seen.
//
// A conversation is named by the agent's session header (see sessionOf). A
// rotation moves on a turn at a time: the tool results an agent sends back
// within a turn stay on the key the turn began with, and the next user message
// begins the next one. Without a session there is no turn to keep to, so each
// request is a turn of its own.

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// How long a conversation is remembered, the cache an answer must have read
// for its next turn to keep to its key, and how recent that answer must be.
const (
	convoKeep     = 24 * time.Hour
	cacheMinimum  = 1024
	cacheLifetime = 5 * time.Minute
)

// sessionOf is the conversation a request belongs to, as its agent names it:
// the first of sessionHeaders that is set. "" when none is.
func sessionOf(in http.Header) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	return ""
}

// turnsOf is how many user turns a request's conversation has had, and whether
// the request is within one: its last message is the tool results sent back
// for the turn's tool calls, rather than something the user said.
func turnsOf(from provider.Protocol, body []byte) (turns int, within bool) {
	req, err := parse(from, body)
	if err != nil {
		return 0, false
	}
	for i, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		var speaks, results bool
		for _, p := range m.Parts {
			if p.Kind == ToolResult {
				results = true
			} else {
				speaks = true
			}
		}
		if speaks && !results {
			turns++
		}
		if i == len(req.Messages)-1 {
			within = results
		}
	}
	return turns, within
}

// answer is the candidate that answered a conversation's request last.
type answer struct {
	rest      string // what rests after a failure: the provider, or one of its keys
	model     string // the candidate's model, which a key may serve more than one of
	provider  string
	at        time.Time
	cacheRead int // cache tokens the answer read from the vendor's cache
}

// convo is what magpie keeps of one conversation with one model: who answered
// it last, and where each provider's rotation has got to. Its fields are
// guarded by convos.
type convo struct {
	seen time.Time
	last *answer
	turn map[string]int // provider → rotation position, one step on each new turn
}

var convos = struct {
	sync.Mutex
	m     map[string]*convo // by session and model
	swept time.Time
}{m: map[string]*convo{}}

// convoOf is the conversation's memory for a model, made if it has none yet.
// Conversations unused for a day are forgotten along the way.
func convoOf(session, model string, now time.Time) *convo {
	convos.Lock()
	defer convos.Unlock()
	if now.Sub(convos.swept) >= time.Minute {
		convos.swept = now
		for k, c := range convos.m {
			if now.Sub(c.seen) > convoKeep {
				delete(convos.m, k)
			}
		}
	}
	k := session + "\x00" + model
	c := convos.m[k]
	if c == nil {
		c = &convo{turn: map[string]int{}}
		convos.m[k] = c
	}
	c.seen = now
	return c
}

// lastAnswer is who answered the conversation last, nil when nobody has.
func (c *convo) lastAnswer() *answer {
	convos.Lock()
	defer convos.Unlock()
	if c.last == nil {
		return nil
	}
	a := *c.last
	return &a
}

// answered remembers the candidate that just answered the conversation.
func (c *convo) answered(a answer) {
	convos.Lock()
	defer convos.Unlock()
	c.last = &a
}

// position is where a provider's rotation stands for the conversation: the
// first turn starts with the first key, and each new turn moves one on.
func (c *convo) position(pid string, newTurn bool) int {
	convos.Lock()
	defer convos.Unlock()
	n, ok := c.turn[pid]
	switch {
	case !ok:
		n = 0
	case newTurn:
		n++
	}
	c.turn[pid] = n
	return n
}

// sessionTurn is a request's place in its conversation, for a rotation: the
// conversation's memory, and whether the request begins a new turn. A nil one,
// or one with no conversation, is a request of its own: each is a turn.
type sessionTurn struct {
	c       *convo
	newTurn bool
}

// position is the key, among n, that a rotation starts a request with.
func (t *sessionTurn) position(pid string, n int) int {
	if t == nil || t.c == nil {
		routed.Lock()
		i := routed.turn[pid]
		routed.turn[pid]++
		routed.Unlock()
		return i % n
	}
	return t.c.position(pid, t.newTurn) % n
}

// keepTo decides whether a request keeps to the candidate that answered its
// conversation last, and says why in the trace. When it does, that candidate
// moves to the front of the order; the candidates and their trace stay
// aligned, and nothing else changes. mode is the provider's or group's
// affinity, and rotating says whether its routing is a rotation, which moves
// on at each new turn unless the mode keeps the whole session.
func keepTo(cs []candidate, pl planned, mode string, rotating bool, cv *convo, turns int, within bool, now time.Time) ([]candidate, planned, *AffinityTrace) {
	t := &AffinityTrace{Mode: mode, Turn: turns, Within: within}
	if t.Mode == "" {
		t.Mode = "auto"
	}
	if len(pl.order) != len(cs) {
		return cs, pl, nil
	}
	last := cv.lastAnswer()
	if last == nil {
		t.Why = "first"
		return cs, pl, t
	}
	t.Last, t.CacheRead = last.rest, last.cacheRead

	var keep string
	switch {
	case mode == provider.AffinityOff:
		t.Why = "off"
		return cs, pl, t
	case mode == provider.AffinitySession:
		keep = "session"
	case within:
		keep = "turn"
	case mode == provider.AffinityTurn || rotating:
		t.Why = "new-turn"
		return cs, pl, t
	case last.cacheRead < cacheMinimum:
		t.Why = "no-cache"
		return cs, pl, t
	case now.Sub(last.at) > cacheLifetime:
		t.Why = "cold"
		return cs, pl, t
	default:
		keep = "cache"
	}

	idx := -1
	for i, c := range cs {
		if !pl.order[i].Aside && c.rest == last.rest && c.model == last.model {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Why = "gone"
		return cs, pl, t
	}
	if _, resting := restOf(cs[idx].rest); resting {
		t.Why = "resting"
		return cs, pl, t
	}
	if spentOf(cs[idx], now) {
		t.Why = "spent"
		return cs, pl, t
	}
	t.Kept, t.Why = true, keep
	cs = moveFirst(cs, idx)
	pl.order = moveFirst(pl.order, idx)
	return cs, pl, t
}

// spentOf is whether a candidate is an account whose allowance, as last
// known, is all but used up for its model.
func spentOf(c candidate, now time.Time) bool {
	a := c.p.Account
	if a == nil {
		return false
	}
	used, _ := allowances(a.Agent)[a.User].For(c.model, now)
	return used >= usedShare
}

// moveFirst is xs with the element at i moved to the front, the rest in order.
func moveFirst[T any](xs []T, i int) []T {
	out := make([]T, 0, len(xs))
	out = append(out, xs[i])
	out = append(out, xs[:i]...)
	return append(out, xs[i+1:]...)
}
