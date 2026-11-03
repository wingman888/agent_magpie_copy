package gateway

// Fallback: a provider can name models to use when it can't take a request
// — a coding plan out of quota, a rate limit, an overloaded or failing
// vendor. The request goes to the next one only while none of the reply has
// been sent, so an agent sees one clean answer from whoever gave it, never a
// half from each. A provider that just failed that way waits at the back of
// the line for a minute, rather than costing every request a doomed try.

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const fallbackCooldown = time.Minute

// candidate is one attempt: a provider carrying only the key this attempt
// uses, and the model to ask it for.
type candidate struct {
	p     provider.Provider
	model string
	ref   string // provider and key: what a failure rests under
	label string // how a failure is named in the call record
}

// keyed expands a provider into one candidate per key the gateway may use,
// in attempt order. Off keys are never among them. A provider with a single
// enabled key keeps the record label it always had: just its ID.
func keyed(p provider.Provider, model string) []candidate {
	on := p.KeysOn()
	if p.Account != nil || len(on) == 0 {
		return []candidate{{p: p, model: model, ref: p.ID, label: p.ID}}
	}
	out := make([]candidate, 0, len(on))
	for _, k := range on {
		q := p
		q.Key, q.KeyName, q.Keys = k.Key, k.Name, nil
		c := candidate{p: q, model: model, ref: p.ID + "/" + provider.KeyRef(k.Key), label: p.ID}
		if len(on) > 1 {
			who := k.Name
			if who == "" {
				who = provider.Mask(k.Key)
			}
			c.label = p.ID + "(" + who + ")"
		}
		out = append(out, c)
	}
	return out
}

// candidates is the primary's keys, then each fallback's keys, group by
// group (one group per provider). Within a group, keys resting after a recent
// failure move behind its ready keys but stay in the group, so they are spent
// before the next provider is asked. A single-key provider with its key
// resting goes behind the others, as a resting provider always has; a
// multi-key provider with every key resting keeps its place in line, its keys
// tried in order before the next provider.
func (s *Server) candidates(p provider.Provider, model string) []candidate {
	var out, deferred []candidate
	add := func(group []candidate) {
		if len(group) == 1 && s.allResting(group) {
			deferred = append(deferred, group...)
			return
		}
		out = append(out, s.inOrder(group)...)
	}
	add(keyed(p, model))
	seen := map[string]bool{p.ID + "/" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		add(keyed(fp, fm))
	}
	return append(out, deferred...)
}

// inOrder moves the keys of one provider that are resting behind the ready
// ones. It only reorders: resting keys remain in the group, tried last.
func (s *Server) inOrder(group []candidate) []candidate {
	if len(group) <= 1 {
		return group
	}
	var ready, resting []candidate
	for _, c := range group {
		if s.resting(c.ref) {
			resting = append(resting, c)
		} else {
			ready = append(ready, c)
		}
	}
	return append(ready, resting...)
}

// allResting says whether every key of a provider's group is resting.
func (s *Server) allResting(group []candidate) bool {
	for _, c := range group {
		if !s.resting(c.ref) {
			return false
		}
	}
	return true
}

// minSecret is the shortest key redaction hides. Real API keys are far longer;
// a short string would match ordinary words in a reply.
const minSecret = 8

// secretsOf lists the full keys a request may touch: the primary's keys and
// the key of every candidate. Each is hidden wherever it could reach a
// caller, a call record or a log.
func secretsOf(p provider.Provider, cands []candidate) []string {
	var keys []string
	add := func(k string) {
		if len(k) >= minSecret {
			keys = append(keys, k)
		}
	}
	add(p.Key)
	for _, k := range p.Keys {
		add(k.Key)
	}
	for _, c := range cands {
		add(c.p.Key)
	}
	return keys
}

// redact hides every one of the keys wherever an error message repeats them.
func redact(msg string, keys []string) string {
	for _, k := range keys {
		msg = strings.ReplaceAll(msg, k, provider.Mask(k))
	}
	return msg
}

// redactWriter hides the keys in everything written on its way to the
// caller, so an upstream error that echoes a key never reaches the client
// or the call record in full. Keys split across two writes are not caught;
// error replies, which the gateway writes whole, always are.
type redactWriter struct {
	http.ResponseWriter
	keys []string
}

func (r *redactWriter) Write(b []byte) (int, error) {
	if len(r.keys) == 0 {
		return r.ResponseWriter.Write(b)
	}
	_, err := r.ResponseWriter.Write([]byte(redact(string(b), r.keys)))
	return len(b), err
}

func (r *redactWriter) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

var restingUntil = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func (s *Server) resting(id string) bool {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	return time.Now().Before(restingUntil.m[id])
}

func (s *Server) rest(id string) {
	restingUntil.Lock()
	restingUntil.m[id] = time.Now().Add(fallbackCooldown)
	restingUntil.Unlock()
}

// quotaWords are how vendors say "out of quota" or "slow down" when their
// status code doesn't: some answer 400 or 403 with it.
var quotaWords = regexp.MustCompile(`(?i)quota|insufficient|balance|credit|billing|exceeded|rate.?limit|usage.?limit|limit.?reached|too many requests|overloaded|余额|额度|欠费|限流|频率|套餐|用量|上限`)

// retryable says whether another provider may do better with a request
// that failed this way.
func retryable(status int, body []byte) bool {
	switch {
	case status == 402, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 401, status == 403:
		return quotaWords.Match(body)
	}
	return false
}

// holdWriter keeps an error reply back while another provider may still
// answer: headers and body wait until release, or are dropped for the next
// try. Anything else goes straight through.
type holdWriter struct {
	w       http.ResponseWriter
	hold    bool
	header  http.Header
	status  int
	passing bool
	held    bytes.Buffer
}

func newHoldWriter(w http.ResponseWriter, hold bool) *holdWriter {
	return &holdWriter{w: w, hold: hold, header: http.Header{}}
}

func (h *holdWriter) Header() http.Header { return h.header }

func (h *holdWriter) WriteHeader(code int) {
	if h.status != 0 {
		return
	}
	h.status = code
	if h.hold && code >= 400 {
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

func (h *holdWriter) Write(b []byte) (int, error) {
	if h.status == 0 {
		h.WriteHeader(http.StatusOK)
	}
	if !h.passing {
		return h.held.Write(b)
	}
	return h.w.Write(b)
}

func (h *holdWriter) Flush() {
	if f, ok := h.w.(http.Flusher); ok && h.passing {
		f.Flush()
	}
}

// failed reports a held error another provider could answer instead.
func (h *holdWriter) failed() bool {
	return !h.passing && h.status >= 400 && retryable(h.status, h.held.Bytes())
}

// release sends a held reply after all: nobody else is left to try.
func (h *holdWriter) release() {
	if h.passing || h.status == 0 {
		return
	}
	h.pass()
	h.w.Write(h.held.Bytes())
}
