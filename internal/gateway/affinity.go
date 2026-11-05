package gateway

// Session affinity and turn-based rotation: a conversation's requests stay
// on the key or account that answered its last one, so the vendor's prompt
// cache can hit, and a "rotate" routing moves on once per user turn rather
// than per request. Both are remembered per session the agent names (see
// sessionOf) and per model asked for, for a day. A request without a session
// id has neither.

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// convKeep is how long a conversation's affinity and rotation are remembered.
const convKeep = 24 * time.Hour

// Prompt caching: a vendor keeps a cache for five minutes, and only a prefix
// of at least this many tokens is worth keeping a conversation for.
const (
	cacheMinTokens = 1024
	cacheWindow    = 5 * time.Minute
)

// convo is what routing knows of the request's conversation.
type convo struct {
	id     string // the session id; "" when there is none
	model  string // the model asked for, as the agent named it
	turns  int    // user turns so far, tool results not counted
	within bool   // the request carries tool results of the turn in progress
}

// conversationOf is the request's conversation. Its id is the session the
// agent named, never one derived from the messages: affinity and rotation
// follow only a session the agent sends.
func conversationOf(h http.Header, body []byte, model string) convo {
	turns, within := turnsOf(body)
	return convo{id: sessionOf(h), model: model, turns: turns, within: within}
}

// turnsOf counts the user turns in a request's messages, and tells whether
// the request continues the last one (its last message is the agent's own
// reply or a tool result, not a new user message).
func turnsOf(body []byte) (int, bool) {
	var m struct {
		Messages []json.RawMessage `json:"messages"`
		Contents []json.RawMessage `json:"contents"`
		Input    json.RawMessage   `json:"input"`
	}
	if json.Unmarshal(body, &m) != nil {
		return 0, false
	}
	items := m.Messages
	if len(items) == 0 {
		items = m.Contents
	}
	if len(items) == 0 && len(m.Input) > 0 {
		switch m.Input[0] {
		case '[':
			_ = json.Unmarshal(m.Input, &items)
		case '"':
			return 1, false // a bare string is one user message
		}
	}
	turns, last := 0, false
	for _, it := range items {
		last = userTurnStart(it)
		if last {
			turns++
		}
	}
	return turns, turns > 0 && !last
}

// userTurnStart is whether a message opens a user turn: a user message that
// is not just tool results coming back.
func userTurnStart(it json.RawMessage) bool {
	var v struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Parts   json.RawMessage `json:"parts"`
	}
	if json.Unmarshal(it, &v) != nil || v.Role != "user" {
		return false
	}
	return !hasToolResult(v.Content) && !hasToolResult(v.Parts)
}

// hasToolResult is whether content blocks (Anthropic's tool_result, Gemini's
// functionResponse) are tool results.
func hasToolResult(raw json.RawMessage) bool {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if string(b["type"]) == `"tool_result"` || b["functionResponse"] != nil {
			return true
		}
	}
	return false
}

// candKey names a candidate in the weighing of a routing: the key or account
// it is, for the model it is asked for (a group may ask one account for two
// models).
func candKey(c candidate) string {
	return c.rest + "\x00" + c.model
}

// memory of conversations: rotation position per conversation and routing key,
// and the candidate that answered each conversation's model last.
var convMem = struct {
	sync.Mutex
	rot   map[string]rotation
	last  map[string]answered
	swept time.Time
}{rot: map[string]rotation{}, last: map[string]answered{}}

type rotation struct {
	turn, pos int
	at        time.Time
}

type answered struct {
	rest      string // the candidate that answered, as the trace names it
	key       string // its candKey: the same key or account, for that model
	cacheRead int    // prompt tokens it read from the vendor's cache
	at        time.Time
}

// sweep forgets what is older than convKeep. Called with convMem locked.
func sweep(now time.Time) {
	if now.Sub(convMem.swept) < time.Minute {
		return
	}
	convMem.swept = now
	for k, v := range convMem.rot {
		if now.Sub(v.at) > convKeep {
			delete(convMem.rot, k)
		}
	}
	for k, v := range convMem.last {
		if now.Sub(v.at) > convKeep {
			delete(convMem.last, k)
		}
	}
}

// rotationStart is where a "rotate" routing starts its turn over n candidates
// of key. Without a conversation it is the old count of requests; with one,
// the position moves on once per user turn, stays put within a turn, and is
// not moved by requests of other conversations, other routing keys, or other
// models asked for in the same conversation.
func rotationStart(key string, n int, cv convo) int {
	if cv.id == "" {
		routed.Lock()
		defer routed.Unlock()
		i := routed.turn[key] % n
		routed.turn[key]++
		return i
	}
	now := time.Now()
	convMem.Lock()
	defer convMem.Unlock()
	k := cv.id + "\x00" + cv.model + "\x00" + key
	st, ok := convMem.rot[k]
	pos := 0
	switch {
	case !ok:
	case cv.within || cv.turns == st.turn:
		pos = st.pos
	default:
		pos = st.pos + 1
	}
	convMem.rot[k] = rotation{turn: cv.turns, pos: pos, at: now}
	sweep(now)
	return pos % n
}

// remember notes that candidate c answered the conversation's model.
func remember(cv convo, c candidate, cacheRead int) {
	if cv.id == "" {
		return
	}
	now := time.Now()
	convMem.Lock()
	defer convMem.Unlock()
	convMem.last[cv.id+"\x00"+cv.model] = answered{rest: c.rest, key: candKey(c), cacheRead: cacheRead, at: now}
	sweep(now)
}

// answeredBy is the candidate that answered the conversation's model last,
// while it is remembered.
func answeredBy(cv convo, now time.Time) (answered, bool) {
	convMem.Lock()
	defer convMem.Unlock()
	a, ok := convMem.last[cv.id+"\x00"+cv.model]
	if !ok || now.Sub(a.at) > convKeep {
		return answered{}, false
	}
	return a, true
}

// keepLast decides whether a conversation stays on the candidate that
// answered it last, as its affinity mode says, and returns that candidate's
// index in cs (-1 when it does not stay) with what it went by. Only a
// candidate of the main pool can be kept, and no candidate is kept while it
// rests, is out of allowance, or is no longer among the candidates.
func keepLast(mode string, cv convo, cs []candidate) (int, AffinityRoute) {
	now := time.Now()
	if mode == "" {
		mode = "auto"
	}
	aff := AffinityRoute{Mode: mode, Turn: cv.turns, Within: cv.within}
	if mode == "off" {
		aff.Why = "off"
		return -1, aff
	}
	last, ok := answeredBy(cv, now)
	if !ok {
		aff.Why = "first"
		return -1, aff
	}
	aff.Last, aff.CacheRead = last.rest, last.cacheRead
	k := -1
	for i, c := range cs {
		if candKey(c) == last.key && !c.aside {
			k = i
			break
		}
	}
	if k < 0 {
		aff.Why = "gone"
		return -1, aff
	}
	if _, resting := restOf(last.rest); resting {
		aff.Why = "resting"
		return -1, aff
	}
	if spentNow(cs[k], now) {
		aff.Why = "spent"
		return -1, aff
	}
	session := mode == "session"
	switch {
	case cv.within && session:
		aff.Why = "session"
	case cv.within:
		aff.Why = "turn"
	case session:
		aff.Why = "session"
	case mode == "turn":
		aff.Why = "new-turn"
		return -1, aff
	case last.cacheRead < cacheMinTokens:
		aff.Why = "no-cache"
		return -1, aff
	case now.Sub(last.at) > cacheWindow:
		aff.Why = "cold"
		return -1, aff
	default:
		aff.Why = "cache"
	}
	aff.Kept = true
	return k, aff
}

// spentNow is whether a candidate that is a subscription account has used up
// the share of its allowance that counts its model.
func spentNow(c candidate, now time.Time) bool {
	if c.p.Account == nil {
		return false
	}
	used, _ := allowances(c.p.Account.Agent)[c.p.Account.User].For(c.model, now)
	return used >= usedShare
}

// AffinityRoute is how a request's conversation stuck, or not, to the key
// or account that answered its model last; the routing trace shows it.
type AffinityRoute struct {
	Mode      string `json:"mode"`   // "auto", "session", "turn" or "off"
	Turn      int    `json:"turn"`   // user turns the conversation has had
	Within    bool   `json:"within"` // the request carries the turn's tool results
	Last      string `json:"last,omitempty"`
	CacheRead int    `json:"cacheRead"` // tokens the last answer read from the vendor's cache
	Kept      bool   `json:"kept"`      // the last answerer was put first
	Why       string `json:"why"`       // see keepLast
}

// affinityMode is the mode a request's routing is held to: the group's, else
// its first member provider's that has one, else automatic.
func affinityMode(g provider.Group, ms []provider.Member) string {
	if g.Affinity != "" {
		return g.Affinity
	}
	for _, m := range ms {
		if m.Provider.Affinity != "" {
			return m.Provider.Affinity
		}
	}
	return ""
}
