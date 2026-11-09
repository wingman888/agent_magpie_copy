package gateway

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyCooldown is how long a key that was out of quota, rate limited or
// overloaded is passed over in favour of the provider's other keys.
const keyCooldown = time.Minute

// holdWriter passes a reply through to the client, except that when hold is
// set an error status is kept back. The caller can then discard it and try
// another key without the client having seen anything.
type holdWriter struct {
	dst       http.ResponseWriter
	hold      bool
	hdr       http.Header
	status    int
	held      bool
	committed bool
	buf       bytes.Buffer
}

func newHoldWriter(dst http.ResponseWriter, hold bool) *holdWriter {
	return &holdWriter{dst: dst, hold: hold, hdr: http.Header{}}
}

func (w *holdWriter) Header() http.Header {
	if w.committed {
		return w.dst.Header()
	}
	return w.hdr
}

func (w *holdWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	if w.hold && code >= 400 {
		w.held = true
		return
	}
	w.commit()
}

// commit sends the status and headers to the client, once.
func (w *holdWriter) commit() {
	if w.committed {
		return
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.committed = true
	h := w.dst.Header()
	for k, v := range w.hdr {
		h[k] = v
	}
	w.dst.WriteHeader(w.status)
}

func (w *holdWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.held {
		return w.buf.Write(p)
	}
	return w.dst.Write(p)
}

func (w *holdWriter) Flush() {
	if !w.committed || w.held {
		return
	}
	if f, ok := w.dst.(http.Flusher); ok {
		f.Flush()
	}
}

// release sends a held error reply to the client as it was.
func (w *holdWriter) release() {
	if !w.held {
		return
	}
	w.held = false
	w.commit()
	w.dst.Write(w.buf.Bytes())
}

// retryableKey reports whether a failed reply means this key is out of
// quota, rate limited or overloaded, so another key may still get through.
func retryableKey(status int, body []byte) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusPaymentRequired, 529, http.StatusServiceUnavailable:
		return true
	case http.StatusForbidden:
		msg := strings.ToLower(string(body))
		for _, w := range []string{"quota", "credit", "balance", "insufficient", "exhaust", "limit", "额度", "余额"} {
			if strings.Contains(msg, w) {
				return true
			}
		}
	}
	return false
}

// coolKey records that a key just failed; it is passed over for a while.
func (s *Server) coolKey(providerID, ref string) {
	if ref == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keyCool == nil {
		s.keyCool = make(map[string]time.Time)
	}
	s.keyCool[providerID+"\x00"+ref] = time.Now()
}

func (s *Server) cooling(providerID, ref string) bool {
	if ref == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.keyCool[providerID+"\x00"+ref]
	return ok && time.Since(t) < keyCooldown
}

// coolLast moves the keys that failed recently behind the others, keeping
// their order. They are still tried if nothing else works.
func (s *Server) coolLast(providerID string, plans []provider.KeyPlan) []provider.KeyPlan {
	var hot, cold []provider.KeyPlan
	for _, pl := range plans {
		if s.cooling(providerID, pl.Ref) {
			cold = append(cold, pl)
		} else {
			hot = append(hot, pl)
		}
	}
	return append(hot, cold...)
}

// planLabel names a key in the call record: the provider, and the key's name
// when it has one.
func planLabel(pl provider.KeyPlan) string {
	if pl.Name != "" {
		return pl.Provider.ID + " (key " + pl.Name + ")"
	}
	return pl.Provider.ID
}

// tryKeys sends the request with each key in turn. A key that is out of quota,
// rate limited or overloaded hands the request to the next one, as long as
// the client has not seen the reply yet. The last key's reply is final.
// It returns the upstream protocol used, the status, the error message, and
// the keys that failed on the way ("" when none did).
func (s *Server) tryKeys(w http.ResponseWriter, r *http.Request, from provider.Protocol, model string, body []byte, plans []provider.KeyPlan, u *Usage) (provider.Protocol, int, string, string) {
	var failed []string
	var to provider.Protocol
	var status int
	var msg string
	for i, pl := range plans {
		last := i == len(plans)-1
		to = pickUpstream(pl, from, body)
		hw := newHoldWriter(w, !last)
		if to == from {
			status, msg = s.passthrough(hw, r, pl.Provider, from, model, body, u)
		} else {
			status, msg = s.translate(hw, r, pl.Provider, from, to, model, body, u)
		}
		if hw.held && !last && retryableKey(hw.status, hw.buf.Bytes()) {
			s.coolKey(pl.Provider.ID, pl.Ref)
			failed = append(failed, planLabel(pl))
			continue
		}
		hw.release()
		break
	}
	return to, status, msg, strings.Join(failed, ", ")
}

// pickUpstream chooses the endpoint a key's request goes to: the client's own
// protocol when the key can take it as it is, else the first endpoint the key
// may use (translated).
func pickUpstream(pl provider.KeyPlan, from provider.Protocol, body []byte) provider.Protocol {
	relay := false
	for _, u := range pl.Upstreams {
		relay = relay || u == from
	}
	if acc := pl.Provider.Account; acc != nil && acc.Stream && !streamOf(body) {
		relay = false
	}
	if relay {
		return from
	}
	return pl.Upstreams[0]
}
