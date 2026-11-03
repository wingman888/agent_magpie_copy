package gateway

// Session affinity: a conversation's requests keep to the key or account
// that last answered them, so the vendor's prompt cache, warm on that one,
// can be hit again. The conversation is the agent's own session id (see
// sessionOf). Its place is kept per model the agent asked for, so a helper
// request to a small model does not move the main conversation.
//
// The modes (provider.Provider.Affinity, provider.Group.Affinity): "session"
// keeps the whole conversation; "turn" keeps one user turn, so a new turn
// may move; "off" keeps nothing; and "" (automatic) keeps a turn, and across
// turns only while the vendor's cache was warm for the last answer. Whatever
// the mode, a last answerer that is resting, all but out of its allowance,
// or no longer a candidate is not kept to.

import (
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const (
	sessionKeep  = 24 * time.Hour  // how long a session's place is remembered
	cacheWarm    = 5 * time.Minute // how long a vendor keeps a prompt cache warm
	cacheMinimum = 1024            // tokens read from the cache for an answer to be worth keeping to
)

// sessionOf is the agent's id for the conversation a request belongs to: the
// first of sessionHeaders that is set, or "" when none is.
func sessionOf(h http.Header) string {
	for _, k := range sessionHeaders {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

// turnOf is where a request stands in its conversation: how many user turns
// the conversation holds, and whether the request is a tool result sent back
// within the last one. Tool results are no turn of their own.
func turnOf(from provider.Protocol, body []byte) (turn int, within bool) {
	req, err := parse(from, body)
	if err != nil {
		return 0, false
	}
	for _, m := range req.Messages {
		if m.Role == "user" && !toolResultsOnly(m) {
			turn++
		}
	}
	if n := len(req.Messages); n > 0 {
		last := req.Messages[n-1]
		within = last.Role == "user" && toolResultsOnly(last)
	}
	return turn, within
}

// toolResultsOnly says whether a message carries nothing but tool results.
func toolResultsOnly(m Message) bool {
	if len(m.Parts) == 0 {
		return false
	}
	for _, p := range m.Parts {
		if p.Kind != ToolResult {
			return false
		}
	}
	return true
}

// lastReply is what a session last got back for a model: from which candidate
// (cid), what it is called in the trace (rest), and how much of the prompt
// the vendor read from its cache.
type lastReply struct {
	cand      string
	rest      string
	cacheRead int
	at        time.Time
}

var replies = struct {
	sync.Mutex
	m map[string]lastReply // session and model → its last reply
}{m: map[string]lastReply{}}

func replyKey(session, model string) string { return session + "\x00" + model }

// recordReply keeps a session's place for model at the candidate that just
// answered it, with what the vendor read from its cache. Places older than
// a day are dropped.
func recordReply(session, model string, c candidate, cacheRead int) {
	now := time.Now()
	replies.Lock()
	defer replies.Unlock()
	for k, r := range replies.m {
		if now.Sub(r.at) > sessionKeep {
			delete(replies.m, k)
		}
	}
	replies.m[replyKey(session, model)] = lastReply{cand: cid(c), rest: c.rest, cacheRead: cacheRead, at: now}
}

// lastReplyOf is the session's last reply for model, if it is remembered.
func lastReplyOf(session, model string, now time.Time) (lastReply, bool) {
	replies.Lock()
	defer replies.Unlock()
	r, ok := replies.m[replyKey(session, model)]
	if !ok || now.Sub(r.at) > sessionKeep {
		return lastReply{}, false
	}
	return r, true
}

// spentNow says whether a candidate is a subscription whose allowance for the
// model is all but used up, so a conversation is not kept to it.
func spentNow(c candidate, now time.Time) bool {
	if c.p.Account == nil {
		return false
	}
	a, ok := allowances(c.p.Account.Agent)[c.p.Account.User]
	if !ok {
		return false
	}
	used, _ := a.For(c.model, now)
	return used >= usedShare
}

// keptWhy says whether a reason keeps the conversation to its last answerer.
func keptWhy(why string) bool {
	switch why {
	case "session", "turn", "cache":
		return true
	}
	return false
}

// affine decides whether the conversation keeps to the candidate that last
// answered it, and says why. It returns the index of that candidate in cands
// and where it moves to (counted without it), or -1 when nothing moves, and
// the trace; the trace is nil when affinity does not come into it: no
// session, or fewer than two candidates.
func affine(cands []candidate, session, model, mode string, tn turnRef, now time.Time) (int, int, *AffinityTrace) {
	if session == "" || len(cands) < 2 {
		return -1, -1, nil
	}
	if mode == "" {
		mode = "auto"
	}
	at := &AffinityTrace{Mode: mode, Turn: tn.turn, Within: tn.within}
	if mode == provider.AffinityOff {
		at.Why = "off"
		return -1, -1, at
	}
	last, ok := lastReplyOf(session, model, now)
	if !ok {
		at.Why = "first"
		return -1, -1, at
	}
	at.Last, at.CacheRead = last.rest, last.cacheRead
	i := slices.IndexFunc(cands, func(c candidate) bool { return cid(c) == last.cand })
	if i < 0 {
		at.Why = "gone"
		return -1, -1, at
	}
	c := cands[i]
	if _, resting := restOf(c.rest); resting {
		at.Why = "resting"
		return -1, -1, at
	}
	if spentNow(c, now) {
		at.Why = "spent"
		return -1, -1, at
	}
	switch mode {
	case provider.AffinitySession:
		at.Why = "session"
	case provider.AffinityTurn:
		at.Why = "new-turn"
		if tn.within {
			at.Why = "turn"
		}
	default: // automatic
		switch {
		case tn.within:
			at.Why = "turn"
		case last.cacheRead < cacheMinimum:
			at.Why = "no-cache"
		case now.Sub(last.at) > cacheWarm:
			at.Why = "cold"
		default:
			at.Why = "cache"
		}
	}
	if !keptWhy(at.Why) {
		return -1, -1, at
	}
	at.Kept = true
	// moved to the front of its own kind: a key aside stays aside
	rest := slices.Delete(slices.Clone(cands), i, i+1)
	to := 0
	if c.aside {
		to = slices.IndexFunc(rest, func(x candidate) bool { return x.aside })
		if to < 0 {
			to = len(rest)
		}
	}
	return i, to, at
}

// shift moves xs[i] to index to, counted in the list without it.
func shift[T any](xs []T, i, to int) []T {
	out := slices.Delete(slices.Clone(xs), i, i+1)
	return slices.Insert(out, to, xs[i])
}
