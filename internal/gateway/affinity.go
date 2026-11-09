package gateway

// Session affinity: a conversation's next request goes first to the key or
// account that answered its last one, so the vendor's prompt cache can be
// hit. Rotation is by turn: a user's message starts one, and the tool
// results sent back within it stay on the key where it began. Both are
// remembered per conversation and per model (or routing group), for a day.

import (
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// What affinity and rotation remember, and for how long.
const (
	stickyKeep     = 24 * time.Hour
	stickyCacheMin = 1024            // cached tokens the last answer must have read
	stickyFresh    = 5 * time.Minute // how long after it the cache is still warm
)

// turnCtx is what routing knows of a request's conversation.
type turnCtx struct {
	conv   string // the conversation's id; "" when unknown, and nothing is remembered
	turn   int    // user turns the conversation has had, this one included
	within bool   // the request sends back tool results of the current turn
	scope  string // what the conversation is kept for: provider/model, or group/id
}

// isUserTurn is a message that starts a user turn: text from the user, not
// tool results sent back.
func isUserTurn(m Message) bool {
	if m.Role != "user" {
		return false
	}
	text := false
	for _, p := range m.Parts {
		switch p.Kind {
		case ToolResult:
			return false
		case Text, Image:
			text = true
		}
	}
	return text
}

// turnOf reads the conversation's turns from a request body. A body that
// doesn't parse has no turns, so routing goes by each request as before.
func turnOf(from provider.Protocol, body []byte, conv string) turnCtx {
	req, err := parse(from, body)
	if err != nil || conv == "" {
		return turnCtx{}
	}
	tc := turnCtx{conv: conv}
	for _, m := range req.Messages {
		if isUserTurn(m) {
			tc.turn++
		}
	}
	if n := len(req.Messages); n > 0 {
		last := req.Messages[n-1]
		tc.within = last.Role == "user" && !isUserTurn(last)
	}
	return tc
}

// stickyKey is a conversation's memory for one scope.
func stickyKey(conv, scope string) string { return conv + "\x00" + scope }

// rotation is where a conversation's rotation stands: the index of the key
// its current turn began on, and the turn it began at.
type rotation struct {
	idx, turn int
	at        time.Time
}

// stickyRec is the candidate that answered a conversation last.
type stickyRec struct {
	rest, provider, model string
	cacheRead             int
	at                    time.Time
}

var sticky = struct {
	sync.Mutex
	last     map[string]stickyRec // the last answerer, by stickyKey
	rotation map[string]rotation
}{last: map[string]stickyRec{}, rotation: map[string]rotation{}}

// pruneSticky forgets what is older than a day. Callers hold the lock.
func pruneSticky(now time.Time) {
	for k, r := range sticky.last {
		if now.Sub(r.at) > stickyKeep {
			delete(sticky.last, k)
		}
	}
	for k, r := range sticky.rotation {
		if now.Sub(r.at) > stickyKeep {
			delete(sticky.rotation, k)
		}
	}
}

// rotateStart is the index of the candidate a request starts on under
// rotate: the one its turn began on, the next one for a new turn. Without a
// conversation it goes by each request, as it always has.
func rotateStart(tc turnCtx, scope string, n int) int {
	if tc.conv == "" {
		routed.Lock()
		defer routed.Unlock()
		k := routed.turn[scope]
		routed.turn[scope]++
		return k % n
	}
	now := time.Now()
	key := stickyKey(tc.conv, scope)
	sticky.Lock()
	defer sticky.Unlock()
	pruneSticky(now)
	r, ok := sticky.rotation[key]
	switch {
	case !ok:
		r = rotation{turn: tc.turn}
	case tc.turn != r.turn:
		r = rotation{idx: r.idx + 1, turn: tc.turn}
	}
	r.at = now
	sticky.rotation[key] = r
	return r.idx % n
}

// rememberSticky records the candidate that answered a conversation.
func rememberSticky(conv, scope string, c candidate, cacheRead int) {
	if conv == "" {
		return
	}
	now := time.Now()
	sticky.Lock()
	defer sticky.Unlock()
	pruneSticky(now)
	sticky.last[stickyKey(conv, scope)] = stickyRec{rest: c.rest, provider: c.p.ID, model: c.model, cacheRead: cacheRead, at: now}
}

// lastSticky is the candidate that answered the conversation last, for scope.
func lastSticky(conv, scope string) (stickyRec, bool) {
	sticky.Lock()
	defer sticky.Unlock()
	r, ok := sticky.last[stickyKey(conv, scope)]
	return r, ok
}

// sameAnswerer is whether a candidate is the one a record names.
func sameAnswerer(c candidate, r stickyRec) bool {
	return c.rest == r.rest && c.p.ID == r.provider && c.model == r.model
}

// stickyWhy is why a conversation does or doesn't stick to its last
// answerer, as far as the conversation itself goes: "" while it might, the
// reason it can't otherwise. Whether the answerer can still take the
// request is for settle to say.
func stickyWhy(mode, routing string, found bool, tc turnCtx, rec stickyRec, age time.Duration) string {
	switch {
	case mode == provider.AffinityOff:
		return "off"
	case !found:
		return "first"
	case mode == provider.AffinityTurn && !tc.within:
		return "new-turn"
	case routing == provider.Rotate && !tc.within && mode != provider.AffinitySession:
		return "new-turn" // rotation moves on at each turn
	case mode == provider.AffinityAuto && !tc.within && rec.cacheRead < stickyCacheMin:
		return "no-cache"
	case mode == provider.AffinityAuto && !tc.within && age > stickyFresh:
		return "cold"
	}
	return ""
}

// spentNow is whether a candidate's account has used up its allowance for
// the model.
func spentNow(c candidate) bool {
	if c.p.Account == nil {
		return false
	}
	used, _ := allowances(c.p.Account.Agent)[c.p.Account.User].For(c.model, time.Now())
	return used >= usedShare
}

// settle puts the candidates that are resting at the back, in the order
// they came, and then, when the conversation has a reason to, moves its last
// answerer to the front of its own tier (main keys, or asides). Nothing here
// removes a candidate, so a request still reaches every one in order.
func settle(out []candidate, ws []Weighed, mode, scope, routing string, tc turnCtx) ([]candidate, []Weighed, *AffinityTrace) {
	var ready, resting []int
	for i, c := range out {
		if r, ok := restOf(c.rest); ok {
			ws[i].Rest = &r
			resting = append(resting, i)
		} else {
			ready = append(ready, i)
		}
	}
	order := append(ready, resting...)
	cs := make([]candidate, len(out))
	wss := make([]Weighed, len(out))
	for i, j := range order {
		cs[i], wss[i] = out[j], ws[j]
	}
	if len(out) < 2 || tc.conv == "" {
		return cs, wss, nil
	}

	now := time.Now()
	rec, found := lastSticky(tc.conv, scope)
	aff := &AffinityTrace{Mode: mode, Turn: tc.turn, Within: tc.within}
	if found {
		aff.Last, aff.CacheRead = rec.rest, rec.cacheRead
	}
	// a last answerer that is resting is never kept, whatever the mode says
	// (off aside, which says off)
	why := ""
	if mode != provider.AffinityOff && found {
		if _, sitting := restOf(rec.rest); sitting {
			why = "resting"
		}
	}
	if why == "" {
		why = stickyWhy(mode, routing, found, tc, rec, now.Sub(rec.at))
	}
	at := -1
	for i, c := range cs {
		if found && sameAnswerer(c, rec) {
			at = i
			break
		}
	}
	if why == "" {
		switch {
		case at < 0:
			why = "gone"
		case spentNow(cs[at]):
			why = "spent"
		case mode == provider.AffinitySession:
			why = "session"
		case tc.within:
			why = "turn"
		default:
			why = "cache"
		}
	}
	aff.Why = why
	if why == "session" || why == "turn" || why == "cache" {
		// the first of its tier: an aside keeps behind the main keys
		if at >= 0 && at < len(ready) {
			aff.Kept = true
			cs, wss = moveToTierFront(cs, wss, at, len(ready))
		}
	}
	return cs, wss, aff
}

// moveToTierFront moves cs[at] to the front of its tier among the first n
// (the ready ones), tiers being the main candidates before the asides.
func moveToTierFront(cs []candidate, ws []Weighed, at, n int) ([]candidate, []Weighed) {
	to := at
	for i := 0; i < n; i++ {
		if ws[i].Aside == ws[at].Aside {
			to = i
			break
		}
	}
	c, w := cs[at], ws[at]
	copy(cs[to+1:at+1], cs[to:at])
	copy(ws[to+1:at+1], ws[to:at])
	cs[to], ws[to] = c, w
	return cs, ws
}

// affinityMode is the mode a provider's or group's setting means; "" when
// unset or unknown, which is automatic.
func affinityMode(s string) string {
	switch s {
	case provider.AffinitySession, provider.AffinityTurn, provider.AffinityOff:
		return s
	}
	return provider.AffinityAuto
}
