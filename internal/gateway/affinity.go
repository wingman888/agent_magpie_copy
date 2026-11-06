package gateway

// Conversations: what keeps a conversation on the key or account that
// answered it last, so the vendor's prompt cache can hit (affinity); where a
// routing's rotation begins for the turn a request is in (rotation by turn);
// which keys a request goes to first, the ones made for its protocol (tiers).
// What is remembered of a conversation is kept for a day.

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const (
	memoryKeep = 24 * time.Hour  // how long a conversation is remembered
	cacheWarm  = 1024            // tokens a vendor must have read from its cache for affinity to keep a turn
	cacheTTL   = 5 * time.Minute // how long after an answer that cache is still warm
)

// reqInfo is what routing knows of the conversation a request is in.
type reqInfo struct {
	session string // the conversation's id (see conversationID)
	scope   string // the model asked for: affinity and rotation are kept per model
	turn    int    // user turns so far, this one included
	within  bool   // the request returns tool results within a turn
}

// newReqInfo reads the conversation a request is in.
func newReqInfo(in http.Header, body []byte, from provider.Protocol, scope string) *reqInfo {
	rq := &reqInfo{session: conversationID(in, body), scope: scope}
	req, err := parse(from, body)
	if err != nil {
		return rq
	}
	for _, m := range req.Messages {
		if m.Role == "user" && !hasToolResult(m) {
			rq.turn++
		}
	}
	if n := len(req.Messages); n > 0 {
		last := req.Messages[n-1]
		rq.within = last.Role == "user" && hasToolResult(last)
	}
	return rq
}

// hasToolResult says whether a message returns tool results.
func hasToolResult(m Message) bool {
	for _, p := range m.Parts {
		if p.Kind == ToolResult {
			return true
		}
	}
	return false
}

func (rq *reqInfo) key() string { return rq.session + "\x00" + rq.scope }

// answer is the key or account a conversation's last answer came from.
type answer struct {
	rest      string // what rests after a failure: the provider, or one of its keys
	at        time.Time
	cacheRead int // tokens the vendor read from its prompt cache
}

var answers = struct {
	sync.Mutex
	m map[string]answer // by conversation and model
}{m: map[string]answer{}}

// remember notes that the key or account rest answered the request.
func (rq *reqInfo) remember(rest string, cacheRead int) {
	if rq == nil {
		return
	}
	answers.Lock()
	defer answers.Unlock()
	now := time.Now()
	if len(answers.m) > 4096 {
		for k, a := range answers.m {
			if now.Sub(a.at) > memoryKeep {
				delete(answers.m, k)
			}
		}
	}
	answers.m[rq.key()] = answer{rest, now, cacheRead}
}

// recall is the last answer to the conversation, while it is remembered.
func (rq *reqInfo) recall() (answer, bool) {
	answers.Lock()
	defer answers.Unlock()
	a, ok := answers.m[rq.key()]
	return a, ok && time.Since(a.at) <= memoryKeep
}

// turnRot is where a rotation began for the last request that rotated.
type turnRot struct {
	at    time.Time
	start int
}

var rotations = struct {
	sync.Mutex
	m map[string]turnRot // by conversation, model and what rotates
}{m: map[string]turnRot{}}

// rotStart is where a rotation over n candidates begins for the request:
// the first request of a turn moves it on by one, and tool results coming
// back within the turn keep it where it was. Other models' requests in the
// same conversation have their own rotation, so they don't move this one.
func rotStart(rq *reqInfo, owner string, n int) int {
	k := rq.key() + "\x00" + owner
	now := time.Now()
	rotations.Lock()
	defer rotations.Unlock()
	if len(rotations.m) > 4096 {
		for k, r := range rotations.m {
			if now.Sub(r.at) > memoryKeep {
				delete(rotations.m, k)
			}
		}
	}
	start := 0
	if r, ok := rotations.m[k]; ok && now.Sub(r.at) <= memoryKeep {
		start = r.start
		if !rq.within {
			start++
		}
	}
	start %= n
	rotations.m[k] = turnRot{now, start}
	return start
}

// item is one candidate of a plan and what the trace says of it.
type item struct {
	c     candidate
	w     Weighed
	aside bool // a key made for another protocol: tried after the main pool
}

// settle puts a plan's candidates in the order they are tried: the main
// pool's ready keys first, then its resting ones, then the asides, each in
// the order they had. A conversation's last answerer moves to the front of
// the ready main pool when affinity keeps it there. rq nil is a request
// outside any conversation, which has no affinity.
func settle(rq *reqInfo, mode string, items []item) ([]item, *AffinityTrace) {
	for i := range items {
		if r, ok := restOf(items[i].c.rest); ok {
			items[i].w.Rest = &r
		}
	}
	var mainReady, mainRest, asideReady, asideRest []item
	for _, it := range items {
		resting := it.w.Rest != nil
		switch {
		case it.aside && resting:
			asideRest = append(asideRest, it)
		case it.aside:
			asideReady = append(asideReady, it)
		case resting:
			mainRest = append(mainRest, it)
		default:
			mainReady = append(mainReady, it)
		}
	}
	var aff *AffinityTrace
	if rq != nil && len(items) > 1 {
		var keep string
		aff, keep = decide(rq, mode, items)
		if keep != "" {
			mainReady = moveFront(mainReady, keep)
		}
	}
	out := make([]item, 0, len(items))
	out = append(out, mainReady...)
	out = append(out, mainRest...)
	out = append(out, asideReady...)
	out = append(out, asideRest...)
	return out, aff
}

// decide says whether the conversation's last answerer keeps it, and why. keep
// names that candidate when it does. An answerer that is resting, spent, or no
// longer a candidate never keeps it, whatever the mode.
func decide(rq *reqInfo, mode string, items []item) (aff *AffinityTrace, keep string) {
	aff = &AffinityTrace{Mode: affMode(mode), Turn: rq.turn, Within: rq.within}
	rec, has := rq.recall()
	var at *item
	if has {
		aff.Last, aff.CacheRead = rec.rest, rec.cacheRead
		for i := range items {
			if items[i].c.rest == rec.rest {
				at = &items[i]
				break
			}
		}
	}
	var why string
	switch {
	case aff.Mode == provider.AffinityOff:
		why = "off"
	case !has:
		why = "first"
	case at == nil:
		why = "gone"
	case at.w.Rest != nil:
		why = "resting"
	case at.aside:
		why = "gone" // no longer in the main pool it answered from
	case at.w.Known && at.w.Used >= usedShare:
		why = "spent"
	case aff.Mode == provider.AffinitySession:
		why = "session"
	case aff.Mode == provider.AffinityTurn && rq.within:
		why = "turn"
	case aff.Mode == provider.AffinityTurn:
		why = "new-turn"
	case rq.within:
		why = "turn"
	case rec.cacheRead < cacheWarm:
		why = "no-cache"
	case time.Since(rec.at) > cacheTTL:
		why = "cold"
	default:
		why = "cache"
	}
	aff.Why = why
	switch why {
	case "session", "turn", "cache":
		aff.Kept = true
		return aff, rec.rest
	}
	return aff, ""
}

// affMode is the affinity a provider or group keeps to, by name: unknown or
// empty is automatic.
func affMode(m string) string {
	switch v := strings.ToLower(strings.TrimSpace(m)); v {
	case provider.AffinitySession, provider.AffinityTurn, provider.AffinityOff:
		return v
	}
	return "auto"
}

// moveFront moves the candidate whose key or account is rest to the front.
func moveFront(its []item, rest string) []item {
	for i := range its {
		if its[i].c.rest == rest {
			it := its[i]
			copy(its[1:i+1], its[:i])
			its[0] = it
			break
		}
	}
	return its
}

func candidatesOf(its []item) []candidate {
	var out []candidate
	for _, it := range its {
		out = append(out, it.c)
	}
	return out
}

func weighedOf(its []item) []Weighed {
	var out []Weighed
	for _, it := range its {
		out = append(out, it.w)
	}
	return out
}

// effectiveProto is the protocol a request is best served in: the one its
// model is at home in when its name says (Claude's Anthropic, GPT's Chat),
// else the one the client spoke.
func effectiveProto(model string, from provider.Protocol) provider.Protocol {
	if f := modelFamily(model); f != "" {
		return f
	}
	return from
}

// fitOf is how well a candidate's key suits a request: 0 when the key is made
// for its protocol, 1 when it names none, 2 when it is made for another. An
// account suits any request.
func fitOf(c candidate, from provider.Protocol) int {
	if c.p.Account != nil {
		return 0
	}
	switch c.p.KeyProtocol {
	case effectiveProto(c.model, from):
		return 0
	case "":
		return 1
	}
	return 2
}

// tiered splits candidates into the main pool, the keys that suit the request
// best, and the asides, the rest: those that name no protocol before those
// made for another. Each keeps the order it had.
func tiered(cs []candidate, from provider.Protocol) (main, aside []candidate) {
	best := 2
	for _, c := range cs {
		best = min(best, fitOf(c, from))
	}
	for _, c := range cs {
		if fitOf(c, from) == best {
			main = append(main, c)
		}
	}
	for f := best + 1; f <= 2; f++ {
		for _, c := range cs {
			if fitOf(c, from) == f {
				aside = append(aside, c)
			}
		}
	}
	return main, aside
}

// orderMain orders the main pool as routing says, rotating by turn when the
// request is in a conversation.
func orderMain(rq *reqInfo, routing, owner string, cs []candidate, model string, from provider.Protocol, keys bool) ([]candidate, weighing) {
	var start func(int) int
	if rq != nil {
		start = func(n int) int { return rotStart(rq, owner, n) }
	}
	return weighBy(routing, owner, cs, model, from, keys, start)
}

// mergeWG adds what one ordering went by to what the others went by.
func mergeWG(dst *weighing, src weighing) {
	if dst.lefts == nil {
		dst.lefts = map[string]left{}
	}
	if dst.tokens == nil {
		dst.tokens = map[string]float64{}
	}
	for k, v := range src.lefts {
		dst.lefts[k] = v
	}
	for k, v := range src.tokens {
		dst.tokens[k] = v
	}
}

// GroupTrace is the routing group a request asked for, as the trace shows it.
type GroupTrace struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Routing  string   `json:"routing"`
	Affinity string   `json:"affinity"`
	Auto     bool     `json:"auto"`
	Members  []string `json:"members"` // the members that can answer, "provider/model"
}

// AffinityTrace is what affinity went by for a request.
type AffinityTrace struct {
	Mode      string `json:"mode"`      // session, turn, off, or auto
	Turn      int    `json:"turn"`      // user turns in the conversation so far
	Within    bool   `json:"within"`    // the request returns tool results within a turn
	Last      string `json:"last"`      // what answered the conversation last
	CacheRead int    `json:"cacheRead"` // tokens that answer read from the vendor's cache
	Kept      bool   `json:"kept"`      // the conversation went back to that answerer
	Why       string `json:"why"`       // session, turn or cache when kept; else why not
}
