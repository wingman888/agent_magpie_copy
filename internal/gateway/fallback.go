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
	"context"
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

// modelWords are how vendors say the model asked for is not theirs to
// serve, or not to this account or key: absent, unsupported, not enabled,
// not permitted. Another provider, or another account, may well serve it.
var modelWords = regexp.MustCompile(`(?i)model_not_found|model.?not.?found|no such model|unknown model|invalid model|unsupported.?model|model_not_supported|` +
	`model.{0,80}(not (found|exist|supported|available|accessible|enabled|activated|allowed|permitted|authorized)|does ?n[o']t (exist|support)|is unavailable|disabled|no longer (available|supported))|` +
	`(not (have )?access|no access|not authorized|not entitled|not allowed|not permitted|permission).{0,40}model|` +
	`模型.{0,16}(不存在|不支持|无权|没有权限|未开通|未授权|不可用|已下线|未开放|未找到)|(无权|没有权限|未开通|未授权|不支持).{0,16}模型`)

// contextWords are how vendors say the request itself is too big for the
// model: no other provider would take it either.
var contextWords = regexp.MustCompile(`(?i)context.?(length|window|size)|maximum context|prompt is too long|input is too long|too many tokens|上下文|超长|过长`)

// retryable says whether another provider may do better with a request
// that failed this way: the provider or the key was refused, out of credit
// or quota, rate limited, failing, or doesn't serve the model. A request
// the vendor found wrong in itself — a missing field, too long a prompt —
// would fail the same everywhere, and goes back to the agent.
func retryable(status int, body []byte) bool {
	switch {
	case status == 401, status == 402, status == 403, status == 404, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 422:
		if contextWords.Match(body) {
			return false
		}
		return quotaWords.Match(body) || modelWords.Match(body) || wrongEndpoint(status, body)
	}
	return false
}

// Retrying the last candidate, the only one left to answer.
const (
	maxRetries       = 2               // tries again after the first
	longestRetryWait = 8 * time.Second // a vendor asking to wait longer gets its error passed on
)

// retryBackoff is how long to wait before each retry when the vendor
// doesn't say.
var retryBackoff = []time.Duration{time.Second, 2 * time.Second}

// retrySleep waits before a retry, or until the agent goes away. A
// variable so tests needn't wait.
var retrySleep = func(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryDelay says whether a failure may clear up by itself, and how long to
// wait before trying the same candidate again: a timeout, a server error,
// an overload, or a rate limit that says when it lifts. The vendor's own
// Retry-After is waited for, unless it asks for longer than is worth
// keeping the agent waiting.
func retryDelay(status int, h http.Header, n int) (time.Duration, bool) {
	if n >= maxRetries {
		return 0, false
	}
	ra, has := retryAfterHeader(h, time.Now())
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
		if ra > longestRetryWait {
			return 0, false
		}
		return ra, true
	}
	return retryBackoff[min(n, len(retryBackoff)-1)], true
}

// retryAfterHeader is the Retry-After header, in seconds or as a date.
func retryAfterHeader(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil && n >= 0 {
		return time.Duration(n * float64(time.Second)), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// unrest ends a candidate's rest: it just answered.
func unrest(id string) {
	restingUntil.Lock()
	delete(restingUntil.m, id)
	delete(restingUntil.note, id)
	restingUntil.Unlock()
}

// How long, and how much of, a streamed reply is held back waiting for its
// first content, so an error before it can still be handed to another
// candidate. Past either, it goes through as it comes.
var (
	streamHoldTime  = 15 * time.Second
	streamHoldBytes = 1 << 20
)

// holdWriter keeps an error reply back while another provider may still
// answer: headers and body wait until release, or are dropped for the next
// try. A streamed reply waits likewise until its first content: an error
// event before any (an overload, a rate limit) is a failure like an error
// status, and the agent sees none of it. Anything else goes straight
// through.
type holdWriter struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	from    provider.Protocol // what the agent speaks, which is what is written
	hold    bool
	header  http.Header
	status  int
	passing bool
	held    bytes.Buffer
	settled bool

	// a stream held until its first content
	waiting bool
	timer   *time.Timer
	line    []byte
	event   string
	data    []string
	// an error event before any content
	errStatus int
	errBody   []byte
	errMsg    string
}

func newHoldWriter(w http.ResponseWriter, from provider.Protocol, hold bool) *holdWriter {
	return &holdWriter{w: w, from: from, hold: hold, header: http.Header{}}
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
	if h.hold && code < 300 && strings.HasPrefix(h.header.Get("Content-Type"), "text/event-stream") {
		h.waiting = true
		if !h.settled {
			h.timer = time.AfterFunc(streamHoldTime, h.timeout)
		}
		return
	}
	h.pass()
}

// pass sends the headers and whatever was held, and lets the rest through.
func (h *holdWriter) pass() {
	if h.passing {
		return
	}
	dst := h.w.Header()
	for k, v := range h.header {
		dst[k] = v
	}
	h.w.WriteHeader(h.status)
	h.passing, h.waiting = true, false
	if h.timer != nil {
		h.timer.Stop()
	}
	if h.held.Len() > 0 {
		h.w.Write(h.held.Bytes())
		h.held.Reset()
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
	if h.waiting && h.errStatus == 0 {
		h.held.Write(b)
		if h.scan(b) {
			h.pass()
			h.flush()
		} else if h.errStatus == 0 && h.held.Len() > streamHoldBytes {
			h.pass()
			h.flush()
		}
		return len(b), nil
	}
	if h.held.Len() < 8<<20 {
		h.held.Write(b)
	}
	return len(b), nil
}

func (h *holdWriter) Flush() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.flush()
}

func (h *holdWriter) flush() {
	if f, ok := h.w.(http.Flusher); ok && h.passing {
		f.Flush()
	}
}

// timeout lets a stream through that has gone too long without content.
func (h *holdWriter) timeout() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.waiting && !h.passing && h.errStatus == 0 && !h.settled {
		h.pass()
		h.flush()
	}
}

// settle ends the try: what is held stays held for the caller to decide on.
func (h *holdWriter) settle() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.settled = true
	if h.timer != nil {
		h.timer.Stop()
	}
}

// scan reads the events in b, reporting true at the first content. An
// error event before it is kept as the try's failure.
func (h *holdWriter) scan(b []byte) bool {
	h.line = append(h.line, b...)
	for {
		i := bytes.IndexByte(h.line, '\n')
		if i < 0 {
			return false
		}
		line := strings.TrimRight(string(h.line[:i]), "\r")
		h.line = h.line[i+1:]
		switch {
		case line == "":
			if len(h.data) == 0 {
				h.event = ""
				continue
			}
			kind := streamEventKind(h.from, h.event, strings.Join(h.data, "\n"))
			data := strings.Join(h.data, "\n")
			h.event, h.data = "", nil
			switch kind {
			case streamContent:
				return true
			case streamError:
				h.errStatus, h.errMsg = streamErrorOf(data)
				h.errBody = []byte(data)
				return false
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			h.event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			h.data = append(h.data, strings.TrimPrefix(line[5:], " "))
		}
	}
}

// heldFailure is the failure held back from the agent, if any: an error
// status, or an error event before any content.
func (h *holdWriter) heldFailure() (status int, body []byte, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.passing:
		return 0, nil, false
	case h.errStatus != 0:
		return h.errStatus, h.errBody, true
	case h.status >= 400:
		return h.status, h.held.Bytes(), true
	}
	return 0, nil, false
}

// streamFailed reports an error event held back before any content, and
// what it said.
func (h *holdWriter) streamFailed() (int, string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.errStatus, h.errMsg, !h.passing && h.errStatus != 0
}

// failed reports a held error another provider could answer instead.
func (h *holdWriter) failed() bool {
	st, body, ok := h.heldFailure()
	return ok && retryable(st, body)
}

// release sends a held reply after all: nobody else is left to try.
func (h *holdWriter) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.passing || h.status == 0 {
		return
	}
	h.pass()
	h.flush()
}

// What one event of a stream is, to the hold on it.
const (
	streamPreamble = iota // says nothing yet: a start, a ping, a role
	streamContent         // the reply has begun
	streamError           // failed before it began
)

// streamEventKind tells an event, in the agent's protocol, apart: one
// saying nothing yet, the first content, or an error.
func streamEventKind(proto provider.Protocol, event, data string) int {
	if strings.TrimSpace(data) == "[DONE]" {
		return streamContent
	}
	var v map[string]json.RawMessage
	if json.Unmarshal([]byte(data), &v) != nil {
		return streamContent
	}
	var typ string
	json.Unmarshal(v["type"], &typ)
	if event == "" {
		event = typ
	}
	switch proto {
	case provider.Anthropic:
		switch event {
		case "message_start", "ping", "content_block_start":
			return streamPreamble
		case "error":
			return streamError
		}
		return streamContent
	case provider.Responses:
		switch event {
		case "response.created", "response.in_progress", "response.queued", "response.output_item.added", "response.content_part.added":
			return streamPreamble
		case "response.failed", "error":
			return streamError
		}
		return streamContent
	case provider.Chat:
		if len(v["error"]) > 0 && string(v["error"]) != "null" {
			return streamError
		}
		var ch struct {
			Choices []struct {
				Delta        map[string]json.RawMessage `json:"delta"`
				FinishReason *string                    `json:"finish_reason"`
			} `json:"choices"`
		}
		json.Unmarshal([]byte(data), &ch)
		for _, c := range ch.Choices {
			if c.FinishReason != nil && *c.FinishReason != "" {
				return streamContent
			}
			for k, x := range c.Delta {
				if k == "role" {
					continue
				}
				s := strings.TrimSpace(string(x))
				if s != "null" && s != `""` && s != "[]" && s != "" {
					return streamContent
				}
			}
		}
		return streamPreamble
	}
	if len(v["error"]) > 0 && string(v["error"]) != "null" {
		return streamError
	}
	return streamContent
}

// streamErrorOf is the status an error event stands for, and its message:
// an overload is 529, a rate limit 429, and so on; one that doesn't say is
// a failing upstream, 502.
func streamErrorOf(data string) (int, string) {
	var v struct {
		Type     string          `json:"type"`
		Message  string          `json:"message"`
		Code     json.RawMessage `json:"code"`
		Error    json.RawMessage `json:"error"`
		Response struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
	}
	json.Unmarshal([]byte(data), &v)
	var e struct {
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Status  string          `json:"status"`
	}
	switch {
	case len(v.Error) > 0 && v.Error[0] == '{':
		json.Unmarshal(v.Error, &e)
	case len(v.Response.Error) > 0 && v.Response.Error[0] == '{':
		json.Unmarshal(v.Response.Error, &e)
	default:
		e.Message, e.Code = v.Message, v.Code
	}
	msg := e.Message
	if msg == "" {
		msg = data
	}
	var code string
	if len(e.Code) > 0 {
		if json.Unmarshal(e.Code, &code) != nil {
			code = string(e.Code)
		}
	}
	if n, err := strconv.Atoi(code); err == nil && n >= 400 && n < 600 && e.Type == "" {
		return n, msg
	}
	all := strings.ToLower(e.Type + " " + code + " " + e.Status + " " + msg)
	switch {
	case strings.Contains(all, "overloaded"):
		return 529, msg
	case strings.Contains(all, "rate_limit"), strings.Contains(all, "rate limit"), strings.Contains(all, "too many requests"),
		strings.Contains(all, "resource_exhausted"), strings.Contains(all, "insufficient_quota"), usedUpWords.MatchString(all):
		return 429, msg
	case strings.Contains(all, "authentication"), strings.Contains(all, "unauthenticated"):
		return 401, msg
	case strings.Contains(all, "permission"):
		return 403, msg
	case strings.Contains(all, "not_found"), modelWords.MatchString(all):
		return 404, msg
	case strings.Contains(all, "invalid_request"), strings.Contains(all, "invalid_argument"), contextWords.MatchString(all):
		return 400, msg
	}
	return 502, msg
}
