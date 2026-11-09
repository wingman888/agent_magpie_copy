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

// keyLabel names a key in the call record: its name, else its fingerprint.
func keyLabel(pl provider.KeyPlan) string {
	if pl.Name != "" {
		return pl.Name
	}
	return pl.Ref
}

// pickUpstream chooses the endpoint a key's request goes to, and whether it is
// relayed as it is: the client's own protocol when the key can take it, else
// the first endpoint the key may use, translated. A backend that only streams
// gets a non-streaming request translated, never relayed.
func pickUpstream(pl provider.KeyPlan, from provider.Protocol, body []byte) (provider.Protocol, bool) {
	relay := false
	for _, u := range pl.Upstreams {
		relay = relay || u == from
	}
	if acc := pl.Provider.Account; acc != nil && acc.Stream && !streamOf(body) {
		relay = false
	}
	if relay {
		return from, true
	}
	return pl.Upstreams[0], false
}
