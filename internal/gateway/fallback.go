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
	"encoding/json"
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

// modelWords are how vendors say the model is not there for this key: it
// does not exist, is not supported, is not allowed or not enabled.
var modelWords = regexp.MustCompile(`(?i)model[^"\n]{0,80}(not (found|exist|supported|available|accessible|enabled|allowed|activated)|does not exist|doesn't exist|unsupported|unavailable|no access|not have access|invalid)|(unknown|unsupported|invalid|unavailable) model|model_not_found|model_not_supported|no such model|(do(es)? not|don't|no) have access to|not (authorized|permitted) to (use|access)|模型[^"\n]{0,12}(不存在|不支持|无权限|未开通|不可用|没有权限)|(不存在|不支持|无权限|未开通)[^"\n]{0,8}模型|无权(访问|使用)`)

// contextWords are how vendors say the request itself is too big: another
// provider would only say the same, unless its window is bigger.
var contextWords = regexp.MustCompile(`(?i)context[_ ]?(length|window)|maximum context|prompt is too long|too long|too large|too many tokens|reduce the length|上下文|超长|过长`)

// hardQuotaWords are the unmistakable ones: money, quota, rate limits.
var hardQuotaWords = regexp.MustCompile(`(?i)quota|insufficient|balance|credit|billing|rate.?limit|usage.?limit|limit.?reached|too many requests|overloaded|余额|额度|欠费|限流|频率|套餐|用量`)

// retryable says whether another provider may do better with a request
// that failed this way.
func retryable(status int, body []byte) bool {
	switch {
	case status == 401, status == 402, status == 403, status == 404, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 422:
		body = bytes.ReplaceAll(body, []byte(`\"`), []byte(`"`))
		if notChatModel(status, body) {
			return true
		}
		if contextWords.Match(body) && !hardQuotaWords.Match(body) {
			return false
		}
		return quotaWords.Match(body) || modelWords.Match(body)
	}
	return false
}

// recoverable says whether a failure may mend itself soon enough to ask the
// same candidate again, and after how long if it said (zero: not said).
func recoverable(status int, h http.Header) (wait time.Duration, ok bool) {
	var said time.Duration
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			said = time.Duration(n * float64(time.Second))
		} else if t, err := http.ParseTime(v); err == nil {
			said = time.Until(t)
		}
	}
	switch status {
	case 408, 500, 502, 503, 504, 529:
	case 429:
		if said <= 0 {
			return 0, false
		}
	default:
		return 0, false
	}
	if said > retryMaxAfter {
		return 0, false
	}
	return max(said, 0), true
}

// How a last candidate is asked again: the waits between tries (a
// Retry-After replaces them), and the longest Retry-After waited for.
var (
	retryBackoff  = []time.Duration{time.Second, 2 * time.Second}
	retryMaxAfter = 8 * time.Second
)

// How long a stream's start is held back, waiting for the first content to
// show it is not an error, and how much of it.
var (
	streamHoldTime = 15 * time.Second
	streamHoldMax  = 1 << 20
)

// holdWriter keeps an error reply back while another provider may still
// answer: headers and body wait until release, or are dropped for the next
// try. A stream is kept back too until its first content shows, so an error
// event ahead of any content is as good as an error status. Anything else
// goes straight through.
type holdWriter struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	proto   provider.Protocol // what the agent speaks, so what is written
	hold    bool
	header  http.Header
	status  int
	passing bool
	held    bytes.Buffer

	streaming bool // an event stream not yet shown to be content
	scan      []byte
	timer     *time.Timer
	closed    bool
	errSeen   bool // an error event ahead of any content
	errStatus int
	errBody   []byte
	errMsg    string
}

func newHoldWriter(w http.ResponseWriter, hold bool) *holdWriter {
	return &holdWriter{w: w, hold: hold, header: http.Header{}}
}

func (h *holdWriter) Header() http.Header { return h.header }

func (h *holdWriter) WriteHeader(code int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.writeHeader(code)
}

func (h *holdWriter) writeHeader(code int) {
	if h.status != 0 {
		return
	}
	h.status = code
	if h.hold && code >= 400 {
		return
	}
	if h.hold && h.proto != "" && h.proto != provider.Gemini && strings.HasPrefix(h.header.Get("Content-Type"), "text/event-stream") {
		h.streaming = true
		h.timer = time.AfterFunc(streamHoldTime, func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if !h.closed && !h.passing && !h.errSeen {
				h.flushHeld()
			}
		})
		return
	}
	h.pass()
}

func (h *holdWriter) pass() {
	dst := h.w.Header()
	for k, v := range h.header {
		dst[k] = v
	}
	h.w.WriteHeader(h.status)
	h.passing = true
}

// flushHeld sends what was held, and lets the rest through.
func (h *holdWriter) flushHeld() {
	if h.passing || h.status == 0 {
		return
	}
	h.pass()
	h.streaming = false
	h.scan = nil
	if h.timer != nil {
		h.timer.Stop()
	}
	h.w.Write(h.held.Bytes())
	h.held.Reset()
	if f, ok := h.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (h *holdWriter) Write(b []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.status == 0 {
		h.writeHeader(http.StatusOK)
	}
	if h.passing {
		return h.w.Write(b)
	}
	h.held.Write(b)
	if h.streaming && !h.errSeen {
		h.scan = append(h.scan, b...)
		h.consume()
	}
	if h.streaming && !h.passing && h.held.Len() > streamHoldMax {
		h.flushHeld()
	}
	return len(b), nil
}

// consume reads the whole events held so far, until one is content or an error.
func (h *holdWriter) consume() {
	for !h.passing && !h.errSeen {
		i, n := nextEvent(h.scan)
		if i < 0 {
			return
		}
		block := string(h.scan[:i])
		h.scan = h.scan[i+n:]
		kind, e := classifyEvent(h.proto, block)
		switch kind {
		case evContent:
			h.flushHeld()
		case evError:
			h.errSeen, h.errStatus, h.errBody, h.errMsg = true, e.status, e.body, e.msg
			if !retryable(e.status, e.body) {
				h.flushHeld() // nobody else would do better: as it came
			}
		}
	}
}

func (h *holdWriter) Flush() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if f, ok := h.w.(http.Flusher); ok && h.passing {
		f.Flush()
	}
}

// failed reports a held error another provider could answer instead.
func (h *holdWriter) failed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.passing {
		return false
	}
	if h.errSeen {
		return retryable(h.errStatus, h.errBody)
	}
	return h.status >= 400 && retryable(h.status, h.held.Bytes())
}

// holding reports whether the reply is still held back, nothing sent on.
func (h *holdWriter) holding() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.passing && h.streaming
}

// result is what the held reply was: its status, and what it said.
func (h *holdWriter) result() (int, []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.errSeen {
		return h.errStatus, h.errBody
	}
	return h.status, h.held.Bytes()
}

// release sends a held reply after all: nobody else is left to try.
func (h *holdWriter) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.flushHeld()
}

// close ends the hold: the reply is dropped or released already.
func (h *holdWriter) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	if h.timer != nil {
		h.timer.Stop()
	}
}

const (
	evHold = iota
	evContent
	evError
)

type streamErr struct {
	status int
	body   []byte
	msg    string
}

// nextEvent finds the end of the first whole event: where its blank line
// starts, and how long that is.
func nextEvent(b []byte) (int, int) {
	best, n := -1, 0
	for _, sep := range []string{"\n\n", "\r\n\r\n"} {
		if i := bytes.Index(b, []byte(sep)); i >= 0 && (best < 0 || i < best) {
			best, n = i, len(sep)
		}
	}
	return best, n
}

// classifyEvent says what one event of the agent's own protocol is: still
// the head of a stream, content, or an error.
func classifyEvent(proto provider.Protocol, block string) (int, streamErr) {
	var name string
	var data []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	if len(data) == 0 {
		return evHold, streamErr{}
	}
	raw := strings.Join(data, "\n")
	if raw == "[DONE]" {
		return evContent, streamErr{}
	}
	var v struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
		Code    json.RawMessage `json:"code"`
		Error   json.RawMessage `json:"error"`
		Resp    struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
		Choices []struct {
			Delta        map[string]json.RawMessage `json:"delta"`
			FinishReason json.RawMessage            `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(raw), &v) != nil {
		return evContent, streamErr{}
	}
	if v.Type == "" {
		v.Type = name
	}
	var vmsg string
	json.Unmarshal(v.Message, &vmsg) // a string, or an object that is no message

	fail := func(obj json.RawMessage, outer string) (int, streamErr) {
		var e struct {
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
		}
		json.Unmarshal(obj, &e)
		code := strings.Trim(string(e.Code), `"`)
		if code == "null" {
			code = ""
		}
		msg := e.Message
		if msg == "" {
			msg = outer
		}
		return evError, streamErr{status: statusOfError(e.Type, code, msg), body: []byte(raw), msg: msg}
	}
	isObj := func(m json.RawMessage) bool { return len(m) > 0 && m[0] == '{' }
	switch proto {
	case provider.Anthropic:
		switch v.Type {
		case "message_start", "ping":
			return evHold, streamErr{}
		case "error":
			return fail(v.Error, vmsg)
		}
		return evContent, streamErr{}
	case provider.Chat:
		if isObj(v.Error) {
			return fail(v.Error, vmsg)
		}
		for _, c := range v.Choices {
			for k, val := range c.Delta {
				if k == "role" {
					continue
				}
				if s := strings.TrimSpace(string(val)); s != "" && s != "null" && s != `""` && s != "[]" {
					return evContent, streamErr{}
				}
			}
			if s := strings.TrimSpace(string(c.FinishReason)); s != "" && s != "null" {
				return evContent, streamErr{}
			}
		}
		return evHold, streamErr{}
	case provider.Responses:
		switch v.Type {
		case "response.created", "response.in_progress", "response.queued":
			return evHold, streamErr{}
		case "response.failed":
			if isObj(v.Resp.Error) {
				return fail(v.Resp.Error, "")
			}
			return evError, streamErr{status: 500, body: []byte(raw), msg: "response failed"}
		case "error":
			if isObj(v.Error) {
				return fail(v.Error, vmsg)
			}
			code := strings.Trim(string(v.Code), `"`)
			return evError, streamErr{status: statusOfError("", code, vmsg), body: []byte(raw), msg: vmsg}
		}
		return evContent, streamErr{}
	}
	return evContent, streamErr{}
}

// statusOfError is the HTTP status an error event stands for.
func statusOfError(typ, code, msg string) int {
	s := strings.ToLower(typ + " " + code + " " + msg)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(s, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("overloaded"):
		return 529
	case has("rate_limit", "rate limit", "too many requests", "quota", "resource_exhausted"):
		return 429
	case has("authentication", "unauthorized", "invalid_api_key"):
		return 401
	case has("permission", "forbidden"):
		return 403
	case has("not_found", "model_not_found"):
		return 404
	case has("invalid_request", "invalid_prompt", "context_length"):
		return 400
	}
	return 500
}
