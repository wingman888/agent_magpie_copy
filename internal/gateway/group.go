package gateway

// Routing groups, in the gateway. A request for group/<id> is spread over
// the keys and accounts of all its members, weighed together as the group's
// routing says (or member by member, in order). Affinity keeps a
// conversation on the key or account that answered it last, so the vendor's
// prompt cache can answer it again. Rotation counts turns, not requests: the
// requests of one turn stay on the key the turn started on.

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const (
	conversationKeep = 24 * time.Hour  // how long a conversation is remembered
	cacheMinTokens   = 1024            // cached tokens a vendor needs to keep a prompt warm
	cacheWindow      = 5 * time.Minute // how long after an answer its prompt stays warm
)

// GroupTrace is the routing group a request was routed as.
type GroupTrace struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Routing  string   `json:"routing"`
	Affinity string   `json:"affinity"`
	Auto     bool     `json:"auto"`
	Members  []string `json:"members"` // the members ready to take it, provider/model
}

// AffinityTrace is what affinity went by for one request.
type AffinityTrace struct {
	Mode      string `json:"mode"`   // session, turn, off, or auto
	Turn      int    `json:"turn"`   // the user turns the conversation had
	Within    bool   `json:"within"` // the request sends tool results back within its turn
	Last      string `json:"last,omitempty"`
	CacheRead int    `json:"cacheRead"` // cached input tokens of the last answer
	Kept      bool   `json:"kept"`      // the last answerer was put first
	Why       string `json:"why"`
}

// turnScope is the turn a request is in, for rotation.
type turnScope struct {
	key    string // the rotation's counter: a provider or group, and a model
	within bool   // the request sends tool results back within its turn
	start  string // the candidate the turn started on
	picked string // set when rotation moved on: the candidate a new turn starts on
}

// conversation is what a conversation's last answer left for affinity and
// rotation.
type conversation struct {
	last      string    // the candidate that answered last
	at        time.Time // when it answered
	cacheRead int       // its cached input tokens
	start     string    // the candidate the current turn started on
	seen      time.Time // when the conversation was last touched
}

var conversations = struct {
	sync.Mutex
	m map[string]*conversation // session id and route key → conversation
}{m: map[string]*conversation{}}

// touchConversation applies f to a conversation, made when new, and forgets
// those not touched in a day.
func touchConversation(sid, key string, now time.Time, f func(c *conversation)) {
	if sid == "" {
		return
	}
	conversations.Lock()
	defer conversations.Unlock()
	for k, c := range conversations.m {
		if now.Sub(c.seen) > conversationKeep {
			delete(conversations.m, k)
		}
	}
	id := sid + "\x00" + key
	c := conversations.m[id]
	if c == nil {
		c = &conversation{}
		conversations.m[id] = c
	}
	c.seen = now
	if f != nil {
		f(c)
	}
}

// conversationOf is a conversation as it stands, and whether it is known.
func conversationOf(sid, key string, now time.Time) (conversation, bool) {
	if sid == "" {
		return conversation{}, false
	}
	conversations.Lock()
	defer conversations.Unlock()
	c, ok := conversations.m[sid+"\x00"+key]
	if !ok || now.Sub(c.seen) > conversationKeep {
		return conversation{}, false
	}
	c.seen = now
	return *c, true
}

// sessionID is the conversation's id from the request's headers, the first
// of sessionHeaders that is set; "" when none is.
func sessionID(h http.Header) string {
	for _, k := range sessionHeaders {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

// affinityMode is the affinity a request goes by: session, turn or off, or
// auto for the default.
func affinityMode(a string) string {
	switch a {
	case "session", "turn", "off":
		return a
	}
	return "auto"
}

// turnsOf is how many user turns a conversation has, and whether the request
// sends tool results back within its turn: its last message holds them.
func turnsOf(from provider.Protocol, body []byte) (turns int, within bool) {
	req, err := parse(from, body)
	if err != nil || req == nil {
		return 0, false
	}
	for _, m := range req.Messages {
		if m.Role == "user" && hasPartKind(m, Text) {
			turns++
		}
	}
	if n := len(req.Messages); n > 0 {
		within = hasPartKind(req.Messages[n-1], ToolResult)
	}
	return turns, within
}

func hasPartKind(m Message, k Kind) bool {
	for _, p := range m.Parts {
		if p.Kind == k {
			return true
		}
	}
	return false
}

// keepWhy is whether a request stays on the candidate that answered its
// conversation last, and why, or why not. conv.last is a candKey.
func keepWhy(mode string, conv conversation, have, within bool, cs []candidate, from provider.Protocol, now time.Time) string {
	if mode == "off" {
		return "off"
	}
	if !have || conv.last == "" {
		return "first"
	}
	idx := -1
	for i, c := range cs {
		if candKey(c) == conv.last {
			idx = i
			break
		}
	}
	// a key made for another protocol is only ever a fallback
	if idx < 0 || keyTier(cs[idx], from) > bestTier(cs, from) {
		return "gone"
	}
	if _, ok := restOf(cs[idx].rest); ok {
		return "resting"
	}
	if !cs[idx].full(now).IsZero() {
		return "spent"
	}
	switch {
	case mode == "session":
		return "session"
	case mode == "turn" && !within:
		return "new-turn"
	case mode == "turn" || within:
		return "turn"
	case conv.cacheRead < cacheMinTokens:
		return "no-cache"
	case now.Sub(conv.at) > cacheWindow:
		return "cold"
	}
	return "cache"
}

// keeps says whether a why keeps the request on its conversation's last
// candidate.
func keeps(why string) bool {
	switch why {
	case "session", "turn", "cache":
		return true
	}
	return false
}

// affine puts the candidate that answered the conversation last first, when
// affinity keeps it there, and says what it went by. Affinity never removes a
// candidate, so a failure still falls through to the rest in order.
func affine(cs []candidate, from provider.Protocol, mode, sid, key string, turns int, within bool, now time.Time) ([]candidate, *AffinityTrace) {
	if len(cs) < 2 {
		return cs, nil
	}
	conv, have := conversationOf(sid, key, now)
	at := &AffinityTrace{Mode: mode, Turn: turns, Within: within, Last: strings.Replace(conv.last, "\x00", "/", 1), CacheRead: conv.cacheRead}
	at.Why = keepWhy(mode, conv, have, within, cs, from, now)
	if !keeps(at.Why) {
		return cs, at
	}
	for i, c := range cs {
		if candKey(c) != conv.last {
			continue
		}
		at.Kept = true
		out := []candidate{c}
		out = append(out, cs[:i]...)
		out = append(out, cs[i+1:]...)
		return out, at
	}
	return cs, at
}

// planGroup orders a routing group's candidates: the keys and accounts of
// every member, weighed together as the group's routing says, or member by
// member in order when it is "order". Keys that suit the request less than
// the best come after the rest, as asides.
func (s *Server) planGroup(g provider.Group, ms []provider.Member, from provider.Protocol, t *turnScope) ([]candidate, planned) {
	var pl planned
	var ps []planItem
	seen := map[string]bool{}
	per := make([][]candidate, len(ms))
	for i, m := range ms {
		cs, left := perKeyOf(m.Provider, m.Model, from)
		for _, c := range cs {
			if k := candKey(c); !seen[k] {
				seen[k] = true
				per[i] = append(per[i], c)
			}
		}
		for _, c := range left {
			w := weighed(c, c.p, weighing{}, false, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
		}
	}
	mainsBy := make([][]candidate, len(ms))
	var asides []candidate
	for i := range ms {
		var a []candidate
		mainsBy[i], a = partitionTier(per[i], from, bestTier(per[i], from))
		asides = append(asides, a...)
	}
	sort.SliceStable(asides, func(i, j int) bool { return keyTier(asides[i], from) < keyTier(asides[j], from) })

	wgOf := map[string]weighing{}
	var mains []candidate
	if g.Routing == provider.Ordered {
		for i, m := range ms {
			cs, wg := weighT(m.Provider, mainsBy[i], m.Model, from, nil)
			for _, c := range cs {
				wgOf[candKey(c)] = wg
			}
			mains = append(mains, cs...)
		}
	} else {
		var pool []candidate
		for _, mb := range mainsBy {
			pool = append(pool, mb...)
		}
		cs, wg := weighT(provider.Provider{ID: "group/" + g.ID, Routing: g.Routing}, pool, "", from, t)
		for _, c := range cs {
			wgOf[candKey(c)] = wg
		}
		mains = cs
	}
	for i, c := range mains {
		w := weighed(c, c.p, wgOf[candKey(c)], false, from)
		w.Turn = i == 0 && g.Routing == provider.Rotate && len(mains) > 1
		if g.Routing != provider.Ordered {
			w.Routing = g.Routing
		}
		ps = append(ps, planItem{c: c, w: w})
	}
	for _, c := range asides {
		w := weighed(c, c.p, weighing{}, false, from)
		w.Aside = true
		if g.Routing != provider.Ordered {
			w.Routing = g.Routing
		}
		ps = append(ps, planItem{c: c, w: w})
	}
	return orderItems(ps, pl)
}

// candKey tells candidates apart within a group: a key can serve several
// models of one provider.
func candKey(c candidate) string { return c.rest + "\x00" + c.model }

// groupTrace describes a routing group for the trace.
func groupTrace(g provider.Group, ms []provider.Member) *GroupTrace {
	gt := &GroupTrace{ID: g.ID, Name: g.Name, Routing: g.Routing, Affinity: g.Affinity, Auto: g.Auto}
	for _, m := range ms {
		gt.Members = append(gt.Members, m.Provider.ID+"/"+m.Model)
	}
	return gt
}
