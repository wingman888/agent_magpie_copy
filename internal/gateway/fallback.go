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
	"io"
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

// modelWords are how vendors say a model is missing, unsupported, or not
// open to this key — which another provider may well serve.
var modelWords = regexp.MustCompile(`(?i)model.{0,60}(not (found|exist|supported|available|accessible|enabled|activated)|does not exist|doesn't exist|unsupported|unavailable|no access|not have access)|(no|not|without).{0,30}access.{0,30}model|unknown model|model_not_found|unsupported_api_for_model|not accessible|do(es)? not have access|permission.{0,20}model|模型.{0,12}(不存在|不支持|无权|未开通|不可用|未找到|没有权限)|无权.{0,6}(访问|使用)|未开通`)

// tooLongWords: the request itself is too big for the model; no other
// provider of the same model does better, and the agent must shrink it.
var tooLongWords = regexp.MustCompile(`(?i)context.{0,20}(length|window|limit)|maximum context|prompt is too long|input is too long|too long|too many (input |prompt )?tokens|max(imum)?.{0,12}tokens|reduce the length|上下文|超长|过长`)

// retryable says whether another provider may do better with a request
// that failed this way.
func retryable(status int, body []byte) bool {
	switch {
	case status == 401, status == 402, status == 403, status == 404, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 422:
		if tooLongWords.Match(body) {
			return false
		}
		return quotaWords.Match(body) || modelWords.Match(body)
	}
	return false
}

// Limits on how long and how much a stream is held back while waiting for
// its first content, in case it fails before saying anything.
var (
	holdTime = 15 * time.Second
	holdSize = 1 << 20
)

// holdWriter keeps an error reply back while another provider may still
// answer: headers and body wait until release, or are dropped for the next
// try. A stream is kept back too until its first content shows, or an error
// event comes before it. Anything else goes straight through.
type holdWriter struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	hold    bool
	proto   provider.Protocol // what the stream speaks: the agent's
	header  http.Header
	status  int
	passing bool
	held    bytes.Buffer

	// a stream waiting for its first content
	sniffing bool
	scanned  int
	timer    *time.Timer
	// a stream that failed before any content
	errStatus int
	errBody   []byte
}

func newHoldWriter(w http.ResponseWriter, hold bool, proto provider.Protocol) *holdWriter {
	return &holdWriter{w: w, hold: hold, proto: proto, header: http.Header{}}
}

func (h *holdWriter) Header() http.Header { return h.header }

func (h *holdWriter) WriteHeader(code int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.status != 0 {
		return
	}
	h.status = code
	if h.hold && code >= 400 {
		return
	}
	if h.hold && strings.HasPrefix(h.header.Get("Content-Type"), "text/event-stream") {
		h.sniffing = true
		h.timer = time.AfterFunc(holdTime, h.expire)
		return
	}
	h.pass()
}

// expire starts passing a stream that has said nothing for too long.
func (h *holdWriter) expire() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.flushSniffed()
}

func (h *holdWriter) pass() {
	dst := h.w.Header()
	for k, v := range h.header {
		dst[k] = v
	}
	h.w.WriteHeader(h.status)
	h.passing = true
}

// flushSniffed gives up waiting: what was held goes through, and so does
// the rest. Called with the lock held.
func (h *holdWriter) flushSniffed() {
	if !h.sniffing {
		return
	}
	h.sniffing = false
	if h.timer != nil {
		h.timer.Stop()
	}
	h.pass()
	if h.held.Len() > 0 {
		h.w.Write(h.held.Bytes())
		h.held.Reset()
	}
	if f, ok := h.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (h *holdWriter) Write(b []byte) (int, error) {
	if h.status == 0 {
		h.WriteHeader(http.StatusOK)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.errStatus != 0 {
		return 0, io.ErrClosedPipe // a failed stream: nothing more is wanted
	}
	if h.sniffing {
		h.held.Write(b)
		if h.held.Len() > holdSize {
			h.flushSniffed() // too much held back: let it through, errors included
			return len(b), nil
		}
		h.scan()
		return len(b), nil
	}
	if !h.passing {
		return h.held.Write(b)
	}
	return h.w.Write(b)
}

// scan reads the held events the stream has finished: content ends the
// wait, an error ends it as a failure.
func (h *holdWriter) scan() {
	for h.sniffing {
		buf := h.held.Bytes()[h.scanned:]
		end, n := bytes.Index(buf, []byte("\n\n")), 2
		if e := bytes.Index(buf, []byte("\r\n\r\n")); e >= 0 && (end < 0 || e < end) {
			end, n = e, 4
		}
		if end < 0 {
			return
		}
		ev := buf[:end]
		h.scanned += end + n
		var name string
		var data []string
		for _, line := range strings.Split(string(ev), "\n") {
			line = strings.TrimRight(line, "\r")
			switch {
			case strings.HasPrefix(line, "event:"):
				name = strings.TrimSpace(line[6:])
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimPrefix(line[5:], " "))
			}
		}
		if len(data) == 0 {
			continue
		}
		payload := strings.Join(data, "\n")
		switch kind, status := classifyEvent(h.proto, name, payload); kind {
		case evContent:
			h.flushSniffed()
		case evError:
			h.sniffing = false
			if h.timer != nil {
				h.timer.Stop()
			}
			h.errStatus, h.errBody = status, []byte(payload)
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
	if h.errStatus != 0 {
		return retryable(h.errStatus, h.errBody)
	}
	return !h.passing && !h.sniffing && h.status >= 400 && retryable(h.status, h.held.Bytes())
}

// outcome is the status and body of what failed, as rest and retry read them.
func (h *holdWriter) outcome() (int, []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.errStatus != 0 {
		return h.errStatus, h.errBody
	}
	return h.status, h.held.Bytes()
}

// streamFailed reports a stream that failed before any content.
func (h *holdWriter) streamFailed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.errStatus != 0
}

// stop drops the timer without sending anything.
func (h *holdWriter) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.timer != nil {
		h.timer.Stop()
	}
}

// release sends what is held after all: nobody else is left to try.
func (h *holdWriter) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.timer != nil {
		h.timer.Stop()
	}
	if h.passing || h.status == 0 {
		return
	}
	if h.sniffing || h.errStatus != 0 {
		h.sniffing = false
		h.errStatus = 0 // what was held, error event and all, goes out as it came
		h.pass()
		h.w.Write(h.held.Bytes())
		h.held.Reset()
		return
	}
	h.pass()
	h.w.Write(h.held.Bytes())
}

// ---- the first event of a stream ----------------------------------------------

type eventKind int

const (
	evNeutral eventKind = iota // message_start, a ping, a role chunk: nothing yet to show
	evContent                  // something an agent would act on
	evError
)

// classifyEvent says what a stream event is, in the agent's protocol; for
// an error, the status it stands for.
func classifyEvent(proto provider.Protocol, name, data string) (eventKind, int) {
	data = strings.TrimSpace(data)
	if data == "[DONE]" {
		return evContent, 0
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(data), &m) != nil {
		return evContent, 0
	}
	str := func(raw json.RawMessage) string {
		var s string
		json.Unmarshal(raw, &s)
		return s
	}
	typ := str(m["type"])
	if typ == "" {
		typ = name
	}
	errText := func(raw json.RawMessage) string {
		var e map[string]json.RawMessage
		if json.Unmarshal(raw, &e) != nil {
			return str(raw)
		}
		return str(e["type"]) + " " + str(e["code"]) + " " + str(e["message"])
	}
	switch proto {
	case provider.Anthropic:
		switch typ {
		case "error":
			return evError, streamErrorStatus(errText(m["error"]), typ)
		case "message_start", "ping":
			return evNeutral, 0
		}
	case provider.Responses:
		switch typ {
		case "error":
			t := errText(m["error"])
			if t == "  " || strings.TrimSpace(t) == "" {
				t = str(m["code"]) + " " + str(m["message"])
			}
			return evError, streamErrorStatus(t, typ)
		case "response.failed":
			var r map[string]json.RawMessage
			json.Unmarshal(m["response"], &r)
			return evError, streamErrorStatus(errText(r["error"]), typ)
		case "response.created", "response.in_progress", "response.queued":
			return evNeutral, 0
		}
	default:
		if raw, ok := m["error"]; ok && string(raw) != "null" {
			return evError, streamErrorStatus(errText(raw), "error")
		}
		var ch struct {
			Choices []struct {
				Delta struct {
					Content   json.RawMessage `json:"content"`
					Reasoning json.RawMessage `json:"reasoning_content"`
					Tools     json.RawMessage `json:"tool_calls"`
				} `json:"delta"`
				Finish *string `json:"finish_reason"`
			} `json:"choices"`
		}
		json.Unmarshal([]byte(data), &ch)
		for _, c := range ch.Choices {
			if c.Finish != nil && *c.Finish != "" {
				return evContent, 0
			}
			for _, raw := range []json.RawMessage{c.Delta.Content, c.Delta.Reasoning, c.Delta.Tools} {
				if s := strings.TrimSpace(string(raw)); s != "" && s != "null" && s != `""` && s != "[]" {
					return evContent, 0
				}
			}
		}
		return evNeutral, 0
	}
	return evContent, 0
}

// streamErrorStatus is the HTTP status an error event stands for.
func streamErrorStatus(text, typ string) int {
	t := strings.ToLower(text)
	has := func(ws ...string) bool {
		for _, w := range ws {
			if strings.Contains(t, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("overloaded"):
		return 529
	case has("rate_limit", "rate limit", "too many requests", "quota", "insufficient_quota"):
		return 429
	case has("billing", "insufficient_balance", "credit"):
		return 402
	case has("authentication", "invalid_api_key", "unauthorized"):
		return 401
	case has("permission"):
		return 403
	case has("not_found", "model_not_found"):
		return 404
	case has("invalid_request", "context_length"):
		return 400
	case has("timeout", "timed out"):
		return 504
	case has("unavailable"):
		return 503
	}
	return 500
}

// ---- retry ------------------------------------------------------------------

// Waits before the first and the second try again of the last candidate to
// try, and the longest Retry-After trusted. Variables so tests can shorten them.
var (
	retryDelays = []time.Duration{time.Second, 2 * time.Second}
	retryLimit  = 8 * time.Second
)

// retryWait says whether the last candidate should be asked again after
// this failure, and after how long. n is how many times it already was.
func retryWait(status int, header http.Header, n int) (time.Duration, bool) {
	if n >= len(retryDelays) {
		return 0, false
	}
	var explicit time.Duration
	var has bool
	if v := strings.TrimSpace(header.Get("Retry-After")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			explicit, has = time.Duration(f*float64(time.Second)), true
		} else if t, err := http.ParseTime(v); err == nil {
			explicit, has = max(time.Until(t), 0), true
		}
	}
	switch status {
	case 408, 500, 502, 503, 504, 529:
	case 429:
		if !has {
			return 0, false
		}
	default:
		return 0, false
	}
	if has {
		if explicit > retryLimit {
			return 0, false
		}
		return explicit, true
	}
	return retryDelays[n], true
}
