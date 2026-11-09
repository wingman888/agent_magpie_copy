package gateway

// Fallback: a provider can name models to use when it can't take a request
// — a coding plan out of quota, a rate limit, an overloaded or failing
// vendor. The request goes to the next one only while none of the reply has
// been sent, so an agent sees one clean answer from whoever gave it, never a
// half from each. A provider that just failed that way waits at the back of
// the line for a minute, rather than costing every request a doomed try.
//
// A provider with several keys on is several candidates, one per key, in
// order, and so is a subscription with several accounts on: when one
// account runs out, the next account of the same provider takes the
// request before any fallback model does. A key can be made for one
// protocol only, as some relays hand them out; see perKey.

import (
	"bytes"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const fallbackCooldown = time.Minute

type candidate struct {
	p     provider.Provider
	model string
	rest  string // what rests after a failure: the provider, or one of its keys
}

// label names a candidate in a call's record: the provider, and the key
// when it has several on.
func (c candidate) label() string {
	if c.rest == c.p.ID {
		return c.p.ID
	}
	if c.p.Account != nil {
		return c.p.ID + " (" + c.p.Account.User + ")"
	}
	if c.p.KeyName != "" {
		return c.p.ID + " (" + c.p.KeyName + ")"
	}
	return c.p.ID + " (" + provider.Mask(c.p.Key) + ")"
}

// perKey is a provider once per key it has on, in order — or, for a
// signed-in agent, once per account it has on, its own first. A key made
// for one protocol only serves on that one's endpoint, and the keys that
// suit the request go first: the Anthropic key for a Claude model, the
// OpenAI one for a GPT model, else the key that speaks what the agent
// spoke, so nothing is translated that needn't be.
func perKey(p provider.Provider, model string, from provider.Protocol) []candidate {
	out, aside, _ := perKeyOf(p, model, from)
	return append(out, aside...)
}

// perKeyOf is perKey, split: the keys routing goes over, those made for
// another protocol, and the accounts or keys it left out as not listing
// the model. Keys made for different protocols are not one pool: routing
// weighs, rotates and keeps conversations over those made for the
// protocol that suits the request best, and the others are tried only
// after them, in the order they suit it.
func perKeyOf(p provider.Provider, model string, from provider.Protocol) (out, aside, left []candidate) {
	if p.Account != nil {
		all := []candidate{{p, model, p.ID}}
		for _, q := range p.AlsoOn() {
			all = append(all, candidate{q, model, p.ID + "@" + q.Account.User})
		}
		// an account whose plan lacks the model (a Free one behind a Plus)
		// would only answer 400; it is tried only when none lists it
		for _, c := range all {
			if c.p.Account.Lists(model) {
				out = append(out, c)
			} else {
				left = append(left, c)
			}
		}
		if len(out) == 0 {
			return all, nil, nil
		}
		return out, nil, left
	}
	keys := p.KeysOn()
	var unlisted []candidate
	for _, k := range keys {
		q := p.WithKey(k)
		if len(q.Speaks()) == 0 {
			continue // made for a protocol this provider has no endpoint for
		}
		rest := p.ID
		if len(keys) > 1 {
			rest += "#" + provider.KeyID(k.Key)
		}
		if !p.Serves(k, model) {
			// the vendor lists the model to another key only
			unlisted = append(unlisted, candidate{q, model, rest})
			continue
		}
		out = append(out, candidate{q, model, rest})
	}
	if len(out) == 0 {
		out, unlisted = unlisted, nil // no key lists it: try them all the same
	}
	if len(out) == 0 {
		return []candidate{{p, model, p.ID}}, nil, nil
	}
	sort.SliceStable(out, func(i, j int) bool { return keyFit(out[i].p, model, from) < keyFit(out[j].p, model, from) })
	pool := out[:0:0]
	for _, c := range out {
		if c.p.KeyProtocol == out[0].p.KeyProtocol {
			pool = append(pool, c)
		} else {
			aside = append(aside, c)
		}
	}
	return pool, aside, unlisted
}

// keyFit ranks how well a key suits a request, best first: 0 fits, 1 needs
// the request translated, 2 is made for another vendor's models.
func keyFit(q provider.Provider, model string, from provider.Protocol) int {
	if q.KeyProtocol == "" {
		return 0
	}
	switch modelFamily(model) {
	case provider.Anthropic:
		if q.KeyProtocol == provider.Anthropic {
			return 0
		}
		return 2
	case provider.Chat:
		if q.KeyProtocol != provider.Anthropic {
			return 0
		}
		return 2
	}
	if q.Base(from) != "" {
		return 0
	}
	return 1
}

// modelFamily is the protocol a model is at home in, when its name says:
// Anthropic for Claude, Chat (standing for OpenAI's) for GPT and the o-series.
func modelFamily(model string) provider.Protocol {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.HasPrefix(m, "claude"):
		return provider.Anthropic
	case strings.HasPrefix(m, "gpt-"), strings.Contains(m, "codex"), len(m) > 1 && m[0] == 'o' && m[1] >= '1' && m[1] <= '9':
		return provider.Chat
	}
	return ""
}

// candidates is the primary and then its fallbacks, each provider's keys
// or accounts as its routing orders them, those resting after a recent
// failure moved behind the rest.
func (s *Server) candidates(p provider.Provider, model string, from provider.Protocol) []candidate {
	out, _ := s.plan(p, model, from)
	return out
}

// plan is candidates, and for the routing trace what each stood where it
// did by, and those left out.
func (s *Server) plan(p provider.Provider, model string, from provider.Protocol) ([]candidate, planned) {
	var pl planned
	add := func(q provider.Provider, m string, fallback bool) []candidate {
		cs, aside, left := perKeyOf(q, m, from)
		cs, wg := weigh(q, cs, m, from)
		for i, c := range cs {
			w := weighed(c, q, wg, fallback, from)
			w.Turn = i == 0 && q.Routing == provider.Rotate && len(cs) > 1
			pl.order = append(pl.order, w)
		}
		pl.order = append(pl.order, asideOf(aside, q, fallback, from)...)
		cs = append(cs, aside...)
		for _, c := range left {
			w := weighed(c, q, weighing{}, fallback, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
		}
		return cs
	}
	out := add(p, model, false)
	seen := map[string]bool{p.ID + "/" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		out = append(out, add(fp, fm, true)...)
	}
	return restLast(out, pl)
}

// planGroup is plan for a routing group: every member's keys or accounts
// weighed together as the group's routing says — in order, member by
// member, each as its own provider's routing orders it; else all as one,
// so a subscription whose allowance renews soonest goes first whichever
// provider it is of. A member's fallbacks are not the group's.
func (s *Server) planGroup(g provider.Group, ms []provider.Member, from provider.Protocol) ([]candidate, planned) {
	var pl planned
	var out []candidate
	of := map[string]provider.Provider{} // candidate → its member's provider
	var all, asides []candidate
	var wAsides []Weighed
	for _, m := range ms {
		cs, aside, left := perKeyOf(m.Provider, m.Model, from)
		asides, wAsides = append(asides, aside...), append(wAsides, asideOf(aside, m.Provider, false, from)...)
		for _, c := range left {
			w := weighed(c, m.Provider, weighing{}, false, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
		}
		if g.Routing == provider.Ordered {
			cs, wg := weigh(m.Provider, cs, m.Model, from)
			for i, c := range cs {
				w := weighed(c, m.Provider, wg, false, from)
				w.Turn = i == 0 && m.Provider.Routing == provider.Rotate && len(cs) > 1
				pl.order = append(pl.order, w)
			}
			out = append(out, cs...)
			continue
		}
		for _, c := range cs {
			of[c.rest+"/"+c.model] = m.Provider
		}
		all = append(all, cs...)
	}
	if g.Routing != provider.Ordered {
		cs, wg := weigh(provider.Provider{ID: provider.GroupPrefix + g.ID, Routing: g.Routing}, all, "", from)
		for i, c := range cs {
			w := weighed(c, of[c.rest+"/"+c.model], wg, false, from)
			w.Routing = g.Routing
			w.Turn = i == 0 && g.Routing == provider.Rotate && len(cs) > 1
			pl.order = append(pl.order, w)
		}
		out = cs
	}
	out, pl.order = append(out, asides...), append(pl.order, wAsides...)
	if len(out) == 0 {
		return nil, pl
	}
	return restLast(out, pl)
}

// asideOf is how the trace tells the keys made for another protocol than
// those routed over: after them, in the order they suit the request.
func asideOf(cs []candidate, q provider.Provider, fallback bool, from provider.Protocol) []Weighed {
	var out []Weighed
	for _, c := range cs {
		w := weighed(c, q, weighing{}, fallback, from)
		w.Aside = true
		out = append(out, w)
	}
	return out
}

// restLast moves those resting after a recent failure behind the rest.
func restLast(out []candidate, pl planned) ([]candidate, planned) {
	if len(out) == 1 {
		if r, ok := restOf(out[0].rest); ok {
			pl.order[0].Rest = &r // tried all the same: there is no other
		}
		return out, pl
	}
	var ready, resting []candidate
	var wReady, wResting []Weighed
	for i, c := range out {
		if r, ok := restOf(c.rest); ok {
			pl.order[i].Rest = &r
			resting, wResting = append(resting, c), append(wResting, pl.order[i])
		} else {
			ready, wReady = append(ready, c), append(wReady, pl.order[i])
		}
	}
	pl.order = append(wReady, wResting...)
	return append(ready, resting...), pl
}

var restingUntil = struct {
	sync.Mutex
	m    map[string]time.Time
	note map[string]Rest // why each rests
}{m: map[string]time.Time{}, note: map[string]Rest{}}

func (s *Server) resting(id string) bool {
	_, ok := restOf(id)
	return ok
}

// restOf is why a candidate is resting, while it is.
func restOf(id string) (Rest, bool) {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	until := restingUntil.m[id]
	if !time.Now().Before(until) {
		return Rest{}, false
	}
	r := restingUntil.note[id]
	r.Until = until
	return r, true
}

// quotaWords are how vendors say "out of quota" or "slow down" when their
// status code doesn't: some answer 400 or 403 with it.
var quotaWords = regexp.MustCompile(`(?i)quota|insufficient|balance|credit|billing|exceeded|rate.?limit|usage.?limit|limit.?reached|hit your .*limit|limit.{0,24}resets|too many requests|overloaded|余额|额度|欠费|限流|频率|套餐|用量|上限`)

// billingWords are the ways a 400 or 422 says the account is out of quota,
// slowed down or unpaid. Unlike quotaWords it does not count a bare
// "exceeded": a context that is too long is the request's own fault.
var billingWords = regexp.MustCompile(`(?i)quota|insufficient|balance|credit|billing|exceeded.{0,20}(plan|limit|balance|credit)|rate.?limit|usage.?limit|limit.?reached|hit your .*limit|limit.{0,24}resets|too many requests|余额|额度|欠费|限流|频率|套餐|用量`)

// modelWords are the ways a 400 or 422 says the model is not there for the
// caller: missing, not supported, not allowed, not switched on, or not served
// on the endpoint asked, which another provider may serve it on.
var modelWords = regexp.MustCompile(`(?i)model[^.]{0,40}(not (found|exist|exists|supported|available|accessible|allowed|enabled|activated|authorized|subscribed)|does not exist|doesn't exist|unsupported|no (access|permission)|not have access)|model_not_found|unsupported_model|unsupported_api_for_model|not accessible|not supported in (the )?v1/|not a chat model|use (the )?v1/|use /v1/|not activated|not authorized|no permission|无权限|没有权限|未开通|未授权|模型.{0,12}(不存在|不支持|无权|不可用|未开通|未启用)`)

// retryable says whether another provider may do better with a request
// that failed this way.
func retryable(status int, body []byte) bool {
	switch status {
	case 401, 402, 403, 404, 408, 429:
		return true
	case 400, 422:
		return billingWords.Match(body) || modelWords.Match(body)
	}
	return status >= 500
}

// modelRefusal reports a 400 or 422 that says the model is not there for the
// caller (or not on the endpoint asked), and not that the account is out of
// quota or the request is at fault. Such a refusal is about the model alone:
// the provider it came from is not at fault and does not rest for it.
func modelRefusal(status int, body []byte) bool {
	if status != 400 && status != 422 {
		return false
	}
	if failure(status, body) != failOther {
		return false
	}
	return modelWords.Match(body) || notChatModel(status, body)
}

// holdWriter keeps a reply back while another provider or another try may
// still answer: headers and body wait until release, or are dropped for the
// next try. Anything that cannot be taken back goes straight through. A
// stream's opening (everything before its first text) is held as well, so a
// provider that fails before it has said anything can still be replaced; see
// openStream. Its methods may be called from the timer that ends the holding,
// hence the lock.
type holdWriter struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	hold    bool
	header  http.Header
	status  int
	passing bool
	held    bytes.Buffer

	inOpening bool        // a stream's opening, held until its first text
	timer     *time.Timer // ends the holding of the opening after openingWait
	closed    bool        // released or discarded: nothing is held any more
}

// How long a stream's opening is held for its first text, and how much of it.
// Past either, the opening goes through as it is.
const (
	openingWait = 15 * time.Second
	openingCap  = 1 << 20
)

func newHoldWriter(w http.ResponseWriter, hold bool) *holdWriter {
	return &holdWriter{w: w, hold: hold, header: http.Header{}}
}

// holdOf is the holdWriter a writer is, or nil.
func holdOf(w http.ResponseWriter) *holdWriter {
	h, _ := w.(*holdWriter)
	return h
}

func (h *holdWriter) Header() http.Header { return h.header }

func (h *holdWriter) WriteHeader(code int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.writeHeaderLocked(code)
}

func (h *holdWriter) writeHeaderLocked(code int) {
	if h.status != 0 {
		return
	}
	h.status = code
	if h.inOpening || (h.hold && code >= 400) {
		return
	}
	h.passLocked()
}

// passLocked sends the headers on, and from then on the body goes straight through.
func (h *holdWriter) passLocked() {
	dst := h.w.Header()
	for k, v := range h.header {
		dst[k] = v
	}
	h.w.WriteHeader(h.status)
	h.passing = true
}

func (h *holdWriter) Write(b []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.status == 0 {
		h.writeHeaderLocked(http.StatusOK)
	}
	if h.passing {
		return h.w.Write(b)
	}
	n, err := h.held.Write(b)
	if h.inOpening && h.held.Len() > openingCap {
		h.commitLocked()
	}
	return n, err
}

func (h *holdWriter) Flush() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if f, ok := h.w.(http.Flusher); ok && h.passing {
		f.Flush()
	}
}

// openStream holds a stream's opening back until content, a time or size
// limit, or the end of the reply.
func (h *holdWriter) openStream() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.passing {
		return
	}
	h.inOpening = true
	h.timer = time.AfterFunc(openingWait, h.expire)
}

func (h *holdWriter) expire() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commitLocked()
}

// content says the reply has begun to say something: its opening goes out,
// and a failure from here on is the agent's to see.
func (h *holdWriter) content() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commitLocked()
}

// commitLocked ends the holding of an opening: what was held goes on, and
// what follows is passed through.
func (h *holdWriter) commitLocked() {
	if !h.inOpening {
		return
	}
	h.inOpening = false
	h.stopTimerLocked()
	if h.status == 0 {
		return // no headers yet: the next WriteHeader sends them through
	}
	if !h.passing {
		h.passLocked()
	}
	if h.held.Len() > 0 {
		h.w.Write(h.held.Bytes())
		h.held.Reset()
	}
}

// streamFail takes back a stream's opening that failed before any text. The
// reply becomes the error the provider gave, in the agent's own shape with
// the status it stands for, and is held like any refused reply, so nothing
// of the dropped opening reaches the agent. It reports false when the opening
// is already past holding: the error then goes to the agent as it came.
func (h *holdWriter) streamFail(from provider.Protocol, status int, msg string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	if !h.inOpening {
		h.mu.Unlock()
		return false
	}
	h.inOpening = false
	h.stopTimerLocked()
	h.status, h.passing = 0, false
	h.held.Reset()
	h.header = http.Header{}
	h.mu.Unlock()
	writeError(h, from, status, msg)
	return true
}

// release sends a held reply after all: nobody else is left to try.
func (h *holdWriter) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.inOpening = false
	h.stopTimerLocked()
	if h.passing || h.status == 0 {
		return
	}
	h.passLocked()
	h.w.Write(h.held.Bytes())
	h.held.Reset()
}

// discard drops a held reply for the next try: nothing of it is sent.
func (h *holdWriter) discard() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.inOpening = false
	h.stopTimerLocked()
	h.held.Reset()
}

func (h *holdWriter) stopTimerLocked() {
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
}

// sent reports whether the headers have gone to the agent.
func (h *holdWriter) sent() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.passing
}

// reply is the status the reply carries, its headers and its held body, for
// judging it.
func (h *holdWriter) reply() (int, http.Header, []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status, h.header, append([]byte(nil), h.held.Bytes()...)
}

// failed reports a held error another provider could answer instead.
func (h *holdWriter) failed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.passing && h.status >= 400 && retryable(h.status, h.held.Bytes())
}

// Waits between tries of the last candidate, when its failure may pass by
// itself, and the longest a Retry-After may ask for before it is not waited.
var (
	retryWaits      = []time.Duration{time.Second, 2 * time.Second}
	retryMaxAfter   = 8 * time.Second
	retryableStatus = map[int]bool{408: true, 500: true, 502: true, 503: true, 504: true, 529: true}
)

// retryWait says how long the last candidate waits before trying again, and
// whether it tries again at all: after a status that may pass by itself,
// up to len(retryWaits) times. A 429 is retried only when its Retry-After says
// when; a Retry-After is waited as it says, and not at all past retryMaxAfter.
func (h *holdWriter) retryWait(retries int) (time.Duration, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if retries >= len(retryWaits) || h.passing {
		return 0, false
	}
	after, has := retryAfterOf(h.header, time.Now())
	if !retryableStatus[h.status] && !(h.status == 429 && has) {
		return 0, false
	}
	if has {
		return after, after <= retryMaxAfter
	}
	return retryWaits[retries], true
}

// retryAfterOf is a Retry-After the vendor sent, in seconds or as a date.
func retryAfterOf(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		return max(time.Duration(n*float64(time.Second)), 0), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}
