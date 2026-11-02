package gateway

// Session affinity: a conversation's next request goes, when it may, to the
// key or account that answered it last, so the vendor's prompt cache — kept
// per key or account — is read rather than the whole history paid for again.
// It is kept per conversation and model: a small model's side request (a
// title, a summary) in the same conversation leaves the main one where it is.
//
// How hard it holds is the provider's Affinity, or the routing group's:
//   - session: the whole conversation;
//   - turn: within one user turn — while tool results go back — only;
//   - off: never;
//   - auto (""): within a turn always; across turns only when the last
//     answer read at least cacheWorth tokens from the vendor's cache, no
//     more than cacheFresh ago, the cache being warm then.
//
// Whatever the mode, one resting after a failure, with its allowance all but
// used up, or no longer among those to try is not kept. Keeping only puts
// one first: every other candidate still follows, so whether a request is
// answered never depends on it.

import (
	"net/http"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const (
	cacheWorth  = 1024             // cache-read tokens worth keeping a conversation for across turns
	cacheFresh  = 5 * time.Minute  // how long a vendor's prompt cache stays warm
	sessionKeep = 24 * time.Hour   // how long conversations are remembered
	prunePeriod = 10 * time.Minute // how often the forgotten are swept
)

// session is one request's place in its conversation.
type session struct {
	key    string // the conversation and the model asked for
	turn   int    // the user turns the conversation has had
	within bool   // a tool result sent back within a turn
}

// sessionOf places a request in its conversation: the agent's own session
// id when it sends one, else one derived from the conversation's start.
func sessionOf(in http.Header, from provider.Protocol, body []byte, model string) session {
	s := session{key: conversationID(in, body) + "\x00" + model}
	req, err := parse(from, body)
	if err != nil {
		return s
	}
	for i, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		result := false
		for _, p := range m.Parts {
			if p.Kind == ToolResult {
				result = true
				break
			}
		}
		if !result {
			s.turn++
		}
		if i == len(req.Messages)-1 {
			s.within = result
		}
	}
	return s
}

// answered is who answered a conversation last, and how.
type answered struct {
	rest      string
	turn      int
	cacheRead int
	at        time.Time
}

var sessions = struct {
	sync.Mutex
	last   map[string]answered
	pruned time.Time
}{last: map[string]answered{}}

// remember notes who answered a conversation.
func remember(s session, c candidate, cacheRead int) {
	if s.key == "" {
		return
	}
	now := time.Now()
	sessions.Lock()
	defer sessions.Unlock()
	sessions.last[s.key] = answered{rest: c.rest, turn: s.turn, cacheRead: cacheRead, at: now}
	if now.Sub(sessions.pruned) < prunePeriod {
		return
	}
	sessions.pruned = now
	for k, a := range sessions.last {
		if now.Sub(a.at) > sessionKeep {
			delete(sessions.last, k)
		}
	}
}

func lastAnswered(key string) (answered, bool) {
	sessions.Lock()
	defer sessions.Unlock()
	a, ok := sessions.last[key]
	if !ok || time.Since(a.at) > sessionKeep {
		return answered{}, false
	}
	return a, true
}

// keep puts first the one that answered the conversation last, when its
// affinity mode says to and it can; with fewer than two to choose from
// there is nothing to keep. The order in the trace follows.
func keep(s session, mode string, cs []candidate, pl planned) ([]candidate, planned, *RouteAffinity) {
	if len(cs) < 2 {
		return cs, pl, nil
	}
	ra := &RouteAffinity{Mode: mode, Turn: s.turn, Within: s.within}
	last, ok := lastAnswered(s.key)
	if ok {
		ra.Last, ra.CacheRead = last.rest, last.cacheRead
	}
	switch {
	case mode == provider.AffinityOff:
		ra.Why = "off"
	case !ok:
		ra.Why = "first"
	case mode == provider.AffinitySession:
		ra.Why = "session"
	case s.within:
		ra.Why = "turn"
	case mode == provider.AffinityTurn:
		ra.Why = "new-turn"
	case last.cacheRead < cacheWorth:
		ra.Why = "no-cache"
	case time.Since(last.at) > cacheFresh:
		ra.Why = "cold"
	default:
		ra.Why = "cache"
	}
	switch ra.Why {
	case "session", "turn", "cache":
	default:
		return cs, pl, ra
	}
	at := -1
	for i, c := range cs {
		if c.rest == last.rest && !c.aside {
			at = i
			break
		}
	}
	switch {
	case at < 0:
		ra.Why = "gone"
	case resting(last.rest):
		ra.Why = "resting"
	case spent(cs[at]):
		ra.Why = "spent"
	default:
		ra.Kept = true
		cs = append(append([]candidate{cs[at]}, cs[:at]...), cs[at+1:]...)
		if at < len(pl.order) {
			order := append([]Weighed{pl.order[at]}, pl.order[:at]...)
			pl.order = append(order, pl.order[at+1:]...)
		}
	}
	return cs, pl, ra
}

func resting(id string) bool {
	_, ok := restOf(id)
	return ok
}

// spent says whether an account has all but used up the allowance that
// counts the model, as its vendor last told.
func spent(c candidate) bool {
	if c.p.Account == nil {
		return false
	}
	a, ok := allowances(c.p.Account.Agent)[c.p.Account.User]
	if !ok {
		return false
	}
	used, _ := a.For(c.model, time.Now())
	return used >= usedShare
}

// affinityOf is the mode a request's conversation is kept by: its routing
// group's when it asked for one, else its provider's.
func affinityOf(p provider.Provider, g *provider.Group) string {
	if g != nil {
		return g.Affinity
	}
	return p.Affinity
}
