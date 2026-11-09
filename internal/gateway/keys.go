package gateway

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// magKeyCooldown is how long a key that was out of quota, rate limited or
// overloaded is passed over in favour of the provider's other keys.
const magKeyCooldown = time.Minute

// magHoldWriter passes a reply through to the client, except that when hold is
// set an error status is kept back. The caller can then discard it and try
// another key without the client having seen anything.
type magHoldWriter struct {
	dst       http.ResponseWriter
	hold      bool
	hdr       http.Header
	status    int
	held      bool
	committed bool
	buf       bytes.Buffer
}

func magNewHoldWriter(dst http.ResponseWriter, hold bool) *magHoldWriter {
	return &magHoldWriter{dst: dst, hold: hold, hdr: http.Header{}}
}

func (w *magHoldWriter) Header() http.Header {
	if w.committed {
		return w.dst.Header()
	}
	return w.hdr
}

func (w *magHoldWriter) WriteHeader(code int) {
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
func (w *magHoldWriter) commit() {
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

func (w *magHoldWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.held {
		return w.buf.Write(p)
	}
	return w.dst.Write(p)
}

func (w *magHoldWriter) Flush() {
	if !w.committed || w.held {
		return
	}
	if f, ok := w.dst.(http.Flusher); ok {
		f.Flush()
	}
}

// release sends a held error reply to the client as it was.
func (w *magHoldWriter) release() {
	if !w.held {
		return
	}
	w.held = false
	w.commit()
	w.dst.Write(w.buf.Bytes())
}

// magRetryableKey reports whether a failed reply means this key is out of
// quota, rate limited or overloaded, so another key may still get through.
func magRetryableKey(status int, body []byte) bool {
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
	return ok && time.Since(t) < magKeyCooldown
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

// magPlanLabel names a key in the call record: the provider, and the key's name
// when it has one.
func magPlanLabel(pl provider.KeyPlan) string {
	if pl.Name != "" {
		return pl.Provider.ID + " (key " + pl.Name + ")"
	}
	return pl.Provider.ID
}

// magKeyLabel names a key in the call record: its name, else its fingerprint.
func magKeyLabel(pl provider.KeyPlan) string {
	if pl.Name != "" {
		return pl.Name
	}
	return pl.Ref
}

// magPickUpstream chooses the endpoint a key's request goes to, and whether it is
// relayed as it is: the client's own protocol when the key can take it, else
// the first endpoint the key may use, translated. A backend that only streams
// gets a non-streaming request translated, never relayed.
func magPickUpstream(pl provider.KeyPlan, from provider.Protocol, body []byte) (provider.Protocol, bool) {
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
