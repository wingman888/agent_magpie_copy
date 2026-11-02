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

var (
	// contextWords: the request itself is too big, which no other provider
	// would take better than this one
	contextWords = regexp.MustCompile(`(?i)context.?(length|window)|too long|too large|maximum context|max(imum)? (prompt )?tokens|token limit|上下文|超长|过长`)
	// modelWords: the model is not there for this account or provider
	modelWords = regexp.MustCompile(`(?i)model.{0,80}(not.?(exist|found|supported|available|accessible|enabled|activated|allowed)|does not exist|unsupported|no access|(do(es)? not|don'?t) have access|access denied|permission)|(unknown|unsupported|no such) model|model_not_found|not accessible|no permission|permission denied|not authorized|无权|未开通|未启用|不支持|不存在|不可用`)
)

// retryable says whether another provider may do better with a request
// that failed this way.
func retryable(status int, body []byte) bool {
	switch {
	case status == 401, status == 402, status == 403, status == 404, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 422:
		if apiRejected(status, body) {
			return true
		}
		if contextWords.Match(body) && !modelWords.Match(body) {
			return false
		}
		return quotaWords.Match(body) || modelWords.Match(body)
	}
	return false
}

// Limits on how long, and how much, of a stream is held back while it might
// still turn out to be an error: past either, it is passed through.
var (
	streamHoldWait = 15 * time.Second
	streamHoldMax  = 1 << 20
)

const (
	evPre = iota // nothing the agent would read yet
	evContent
	evError
)

// holdWriter keeps an error reply back while another provider may still
// answer: headers and body wait until release, or are dropped for the next
// try. A stream waits too, until its first content; an error event before
// that counts as the error status it stands for. Anything else goes straight
// through.
type holdWriter struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	hold    bool
	proto   provider.Protocol
	header  http.Header
	status  int
	passing bool
	held    bytes.Buffer
	// a stream waiting for its first content
	waiting bool
	scanned int
	timer   *time.Timer
	closed  bool
	// the error a stream opened with, as the status it stands for
	errStatus int
	errBody   []byte
	errMsg    string
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
		h.waiting = true
		h.timer = time.AfterFunc(streamHoldWait, h.timeout)
		return
	}
	h.pass()
}

// timeout starts passing a stream that has been silent of content too long.
func (h *holdWriter) timeout() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.waiting && !h.closed && h.errStatus == 0 {
		h.flushHeld()
	}
}

// flushHeld stops waiting and passes what is held on.
func (h *holdWriter) flushHeld() {
	h.waiting = false
	h.pass()
	h.w.Write(h.held.Bytes())
	h.held.Reset()
	if f, ok := h.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (h *holdWriter) pass() {
	if h.passing {
		return
	}
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
	if h.closed {
		return len(b), nil
	}
	if h.status == 0 {
		h.mu.Unlock()
		h.WriteHeader(http.StatusOK)
		h.mu.Lock()
	}
	if h.waiting {
		h.held.Write(b)
		if h.errStatus == 0 {
			h.scan()
		}
		if h.waiting && h.errStatus == 0 && h.held.Len() > streamHoldMax {
			h.flushHeld()
		}
		return len(b), nil
	}
	if !h.passing {
		return h.held.Write(b)
	}
	return h.w.Write(b)
}

// scan reads the events held so far for the first content or error.
func (h *holdWriter) scan() {
	for h.waiting && h.errStatus == 0 {
		rest := h.held.Bytes()[h.scanned:]
		end, n := -1, 0
		for _, sep := range []string{"\n\n", "\r\n\r\n"} {
			if i := bytes.Index(rest, []byte(sep)); i >= 0 && (end < 0 || i < end) {
				end, n = i, len(sep)
			}
		}
		if end < 0 {
			return
		}
		ev := string(rest[:end])
		h.scanned += end + n
		var name string
		var data []string
		for _, l := range strings.Split(strings.ReplaceAll(ev, "\r\n", "\n"), "\n") {
			switch {
			case strings.HasPrefix(l, "event:"):
				name = strings.TrimSpace(l[6:])
			case strings.HasPrefix(l, "data:"):
				data = append(data, strings.TrimPrefix(l[5:], " "))
			}
		}
		switch kind, status, msg := classifyEvent(name, strings.Join(data, "\n")); kind {
		case evContent:
			h.flushHeld()
		case evError:
			h.errStatus, h.errMsg = status, msg
			h.errBody = []byte(strings.Join(data, "\n"))
		}
	}
}

// classifyEvent says what one event of a stream is to its reader: nothing
// yet, content, or an error — and then the status it stands for.
func classifyEvent(name, data string) (kind, status int, msg string) {
	data = strings.TrimSpace(data)
	if data == "" {
		return evPre, 0, ""
	}
	if data == "[DONE]" {
		return evContent, 0, ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(data), &m) != nil {
		return evContent, 0, ""
	}
	typ, _ := m["type"].(string)
	if name == "" {
		name = typ
	}
	var errObj any
	switch {
	case name == "error" || typ == "error":
		errObj = m["error"]
		if errObj == nil {
			errObj = m
		}
	case typ == "response.failed" || typ == "response.error":
		if r, ok := m["response"].(map[string]any); ok {
			errObj = r["error"]
		}
		if errObj == nil {
			errObj = m["error"]
		}
		if errObj == nil {
			errObj = m
		}
	case m["error"] != nil:
		errObj = m["error"]
	}
	if errObj != nil {
		status, msg = errorEventStatus(errObj, typ)
		return evError, status, msg
	}
	switch typ {
	case "message_start", "ping", "response.created", "response.in_progress", "response.queued":
		return evPre, 0, ""
	}
	if ch, ok := m["choices"].([]any); ok {
		for _, c := range ch {
			cm, _ := c.(map[string]any)
			if cm["finish_reason"] != nil {
				return evContent, 0, ""
			}
			d, _ := cm["delta"].(map[string]any)
			for k, v := range d {
				if k == "role" || v == nil {
					continue
				}
				if s, ok := v.(string); ok && s == "" {
					continue
				}
				if l, ok := v.([]any); ok && len(l) == 0 {
					continue
				}
				return evContent, 0, ""
			}
		}
		return evPre, 0, ""
	}
	return evContent, 0, ""
}

// errorEventStatus is the HTTP status an error event stands for.
func errorEventStatus(errObj any, typ string) (int, string) {
	var msg, kind string
	status := 0
	switch e := errObj.(type) {
	case map[string]any:
		msg, _ = e["message"].(string)
		for _, k := range []string{"type", "code", "status"} {
			switch v := e[k].(type) {
			case string:
				kind += " " + strings.ToLower(v)
			case float64:
				if v >= 400 && v < 600 {
					status = int(v)
				}
			}
		}
	case string:
		msg = e
	}
	if msg == "" {
		msg = "upstream stream error"
	}
	if status != 0 {
		return status, msg
	}
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(kind, "overload") || strings.Contains(l, "overloaded"):
		return 529, msg
	case strings.Contains(kind, "rate_limit") || strings.Contains(kind, "insufficient_quota") || strings.Contains(kind, "rate limit"):
		return 429, msg
	case strings.Contains(kind, "authentication"):
		return 401, msg
	case strings.Contains(kind, "permission"):
		return 403, msg
	case strings.Contains(kind, "not_found"):
		return 404, msg
	case strings.Contains(kind, "invalid_request"):
		return 400, msg
	case strings.Contains(kind, "timeout"):
		return 408, msg
	}
	return 500, msg
}

func (h *holdWriter) Flush() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if f, ok := h.w.(http.Flusher); ok && h.passing {
		f.Flush()
	}
}

// code is the status the reply stands for: an error event before any
// content counts as the error it names.
func (h *holdWriter) code() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.errStatus != 0 {
		return h.errStatus
	}
	return h.status
}

// reason is what the held error said.
func (h *holdWriter) reason() (body []byte, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.errStatus != 0 {
		return h.errBody, h.errMsg
	}
	return h.held.Bytes(), ""
}

// failed reports a held error another provider could answer instead.
func (h *holdWriter) failed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.passing {
		return false
	}
	if h.errStatus != 0 {
		return retryable(h.errStatus, h.errBody)
	}
	return h.status >= 400 && retryable(h.status, h.held.Bytes())
}

// discard drops what is held: the next try answers instead.
func (h *holdWriter) discard() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	if h.timer != nil {
		h.timer.Stop()
	}
	h.held.Reset()
}

// release sends a held reply after all: nobody else is left to try.
func (h *holdWriter) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	if h.timer != nil {
		h.timer.Stop()
	}
	if h.passing || h.status == 0 {
		return
	}
	h.pass()
	h.w.Write(h.held.Bytes())
	h.held.Reset()
}

const (
	statusCanceled = 499
	failCanceled   = "canceled"
	kindFailover   = "failover"
	kindRetry      = "retry"
	kindCanceled   = "canceled"

	maxRetries    = 2
	longestRetryW = 8 * time.Second // a longer Retry-After is not waited for
)

// retryWait says whether the last candidate's failure may mend itself, and
// how long to wait before asking again: retry counts those made already.
func (s *Server) retryWait(retry, status int, h http.Header) (time.Duration, bool) {
	if retry >= maxRetries {
		return 0, false
	}
	var ra time.Duration
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			ra = time.Duration(n * float64(time.Second))
		} else if t, err := http.ParseTime(v); err == nil {
			ra = time.Until(t)
		}
	}
	switch status {
	case 408, 500, 502, 503, 504, 529:
	case 429:
		if ra <= 0 {
			return 0, false
		}
	default:
		return 0, false
	}
	if ra > longestRetryW {
		return 0, false
	}
	if ra > 0 {
		return ra, true
	}
	base := s.retryBase
	if base <= 0 {
		base = time.Second
	}
	return base << retry, true
}
