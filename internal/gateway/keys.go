package gateway

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyCooldown is how long a key that failed is tried after the others.
const keyCooldown = time.Minute

// serveKeys sends a request through a provider's keys in turn. A key that
// is rate-limited, out of quota, overloaded or down hands the request to the
// next key, as long as no reply has started; any other error goes back to
// the client as it is. It returns the endpoint and status of the last try,
// a description of the keys that failed on the way ("" when none did), and
// the key that answered when the request succeeded ("" otherwise).
func (s *Server) serveKeys(w http.ResponseWriter, r *http.Request, p provider.Provider, from provider.Protocol, model string, body []byte, u *Usage) (provider.Protocol, int, string, string, string) {
	atts := s.coolLast(p.ID, p.Attempts(from, model))
	if len(atts) == 0 {
		msg := p.Name + " has no key for this endpoint"
		if len(p.Speaks()) == 0 {
			msg = p.Name + " has no endpoint configured"
		}
		return "", writeError(w, from, 502, msg), msg, "", ""
	}
	var failed []string
	var to provider.Protocol
	var status int
	var msg, key string
	for i, a := range atts {
		last := i == len(atts)-1
		to = a.To
		hw := newHoldWriter(w, !last)
		status, msg = s.attempt(hw, r, a.View, a.To, from, model, body, u)
		if hw.isHeld() && keyRetryable(status, hw.body()) {
			s.cool(p.ID, a.Key.Ref)
			failed = append(failed, failedDesc(p.ID, a.Key.Name))
			continue
		}
		hw.release()
		if status < 400 {
			key = keyLabel(a.Key)
		}
		break
	}
	return to, status, msg, strings.Join(failed, "; "), key
}

// keyLabel names a key in the call record: its name, else its fingerprint.
func keyLabel(k provider.KeyChoice) string {
	if k.Name != "" {
		return k.Name
	}
	return k.Ref
}

// attempt makes one try through a provider view, relaying when the endpoint
// is the client's own and translating otherwise.
func (s *Server) attempt(w http.ResponseWriter, r *http.Request, view provider.Provider, to, from provider.Protocol, model string, body []byte, u *Usage) (int, string) {
	if to == from && view.Base(from) != "" {
		return s.passthrough(w, r, view, from, model, body, u)
	}
	return s.translate(w, r, view, from, to, model, body, u)
}

// failedDesc names a failed candidate for the call record.
func failedDesc(providerID, keyName string) string {
	if keyName == "" {
		return providerID
	}
	return providerID + " (key " + keyName + ")"
}

// keyRetryable says whether a failure from one key is worth trying the next
// key for: rate limits, quota, overload and outages. A 400 is the request's
// own fault and is not retried.
func keyRetryable(status int, body string) bool {
	switch {
	case status == 402, status == 429, status >= 500:
		return true
	case status == 403:
		return quotaText(body)
	}
	return false
}

// quotaText recognizes the wording gateways use for a spent quota. Only
// explicit quota or balance wording counts; a bare "limit" or "exceed" does
// not, so an ordinary 403 is never taken for a spent key.
func quotaText(body string) bool {
	msg := strings.ToLower(body)
	for _, w := range []string{"quota", "insufficient", "balance", "exhaust", "额度", "余额", "用完"} {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

// cool marks a key as having just failed.
func (s *Server) cool(providerID, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keyCool == nil {
		s.keyCool = map[string]time.Time{}
	}
	s.keyCool[providerID+"\x00"+ref] = time.Now()
}

// coolLast drops the keys that failed within keyCooldown while others are
// ready. When every key is cooling, they are all kept, in their order.
func (s *Server) coolLast(providerID string, atts []provider.Attempt) []provider.Attempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ready, cooled []provider.Attempt
	for _, a := range atts {
		if t, ok := s.keyCool[providerID+"\x00"+a.Key.Ref]; ok && time.Since(t) < keyCooldown {
			cooled = append(cooled, a)
		} else {
			ready = append(ready, a)
		}
	}
	if len(ready) > 0 {
		return ready
	}
	return cooled
}

// countView is the provider view a count_tokens request is sent through. A
// provider with a single key is used as it is; with several, the first key
// that reaches Anthropic is used, so an OpenAI-only key is never asked.
func countView(p provider.Provider, ok bool, model string) (provider.Provider, bool) {
	if !p.Routed() {
		return p, ok && p.Anthropic != ""
	}
	if !ok {
		return p, false
	}
	for _, a := range p.Attempts(provider.Anthropic, model) {
		if a.To == provider.Anthropic {
			return a.View, true
		}
	}
	return p, false
}

// holdWriter stands between one attempt and the client. With hold set, an
// error reply is kept back so the next key can answer instead; release
// sends a kept reply on when there is no next key.
type holdWriter struct {
	dst     http.ResponseWriter
	hdr     http.Header
	hold    bool
	code    int
	started bool
	buf     bytes.Buffer
}

func newHoldWriter(dst http.ResponseWriter, hold bool) *holdWriter {
	return &holdWriter{dst: dst, hdr: http.Header{}, hold: hold}
}

func (h *holdWriter) Header() http.Header { return h.hdr }

func (h *holdWriter) WriteHeader(code int) {
	if h.code != 0 {
		return
	}
	h.code = code
	if h.hold && code >= 400 {
		return
	}
	h.start()
}

// start sends the status and headers to the client.
func (h *holdWriter) start() {
	if h.started {
		return
	}
	h.started = true
	if h.code == 0 {
		h.code = http.StatusOK
	}
	dh := h.dst.Header()
	for k, v := range h.hdr {
		dh[k] = v
	}
	h.dst.WriteHeader(h.code)
}

func (h *holdWriter) Write(b []byte) (int, error) {
	if h.code == 0 {
		h.WriteHeader(http.StatusOK)
	}
	if !h.started {
		return h.buf.Write(b)
	}
	return h.dst.Write(b)
}

func (h *holdWriter) Flush() {
	if !h.started {
		return
	}
	if f, ok := h.dst.(http.Flusher); ok {
		f.Flush()
	}
}

// isHeld is true when an error reply is being kept back.
func (h *holdWriter) isHeld() bool { return h.code >= 400 && !h.started }

// body is the kept-back reply's body so far.
func (h *holdWriter) body() string { return h.buf.String() }

// release sends whatever was kept back to the client.
func (h *holdWriter) release() {
	if h.code == 0 {
		return
	}
	h.hold = false
	h.start()
	if h.buf.Len() > 0 {
		h.dst.Write(h.buf.Bytes())
		h.buf.Reset()
	}
}
