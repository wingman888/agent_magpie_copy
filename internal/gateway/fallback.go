package gateway

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// fallbackCooldown is how long a provider that just failed with a retryable
// error is moved behind the other candidates. It lives in memory only.
const fallbackCooldown = time.Minute

// quotaWords mark a 400, 401 or 403 as a quota, rate or overload problem
// rather than a bad request. Matching ignores case.
var quotaWords = []string{
	"quota", "insufficient", "balance", "credit", "billing", "exceeded",
	"rate limit", "usage limit", "too many requests", "overloaded",
	"余额", "额度", "欠费", "限流", "频率", "套餐", "用量", "上限",
}

// retryable reports whether an error reply means this provider cannot take
// the request now, so the next candidate may.
func retryable(status int, body []byte) bool {
	switch {
	case status == 402, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 401, status == 403:
		msg := strings.ToLower(string(body))
		for _, w := range quotaWords {
			if strings.Contains(msg, w) {
				return true
			}
		}
	}
	return false
}

// candidate is one provider and model a request may be sent to.
type candidate struct {
	p     provider.Provider
	model string
}

// candidates lists what a request tries, in order: the chosen model, then the
// provider's own fallback list. Only the provider's own list is used, never
// the fallbacks' lists. Entries that do not resolve and repeats are skipped.
// Providers that failed within the cooldown move behind the rest.
func (s *Server) candidates(p provider.Provider, model string) []candidate {
	out := []candidate{{p, model}}
	seen := map[string]bool{p.ID + "\x00" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok {
			continue
		}
		key := fp.ID + "\x00" + fm
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, candidate{fp, fm})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var fresh, cooling []candidate
	for _, c := range out {
		if until, ok := s.cooling[c.p.ID]; ok && now.Before(until) {
			cooling = append(cooling, c)
		} else {
			fresh = append(fresh, c)
		}
	}
	return append(fresh, cooling...)
}

// cool starts the cooldown of a provider that just failed retryably.
func (s *Server) cool(providerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cooling == nil {
		s.cooling = make(map[string]time.Time)
	}
	s.cooling[providerID] = time.Now().Add(fallbackCooldown)
}

// failure words one failed attempt for the call record, as "HTTP 429: msg".
func failure(status int, msg, name string) string {
	return fmt.Sprintf("HTTP %d: %s", status, strings.TrimPrefix(msg, name+": "))
}

// pendingReply holds one candidate's reply until it is known to be final. A
// success, or any error that is not retryable, goes to the client as it
// happens. A retryable error is kept back, so it can be dropped unseen when
// the next candidate is tried. Once any success status is sent, the reply
// is committed and cannot be switched.
type pendingReply struct {
	dst       http.ResponseWriter
	hdr       http.Header
	status    int
	held      bytes.Buffer
	committed bool
}

func newPendingReply(dst http.ResponseWriter) *pendingReply {
	return &pendingReply{dst: dst, hdr: http.Header{}}
}

func (a *pendingReply) Header() http.Header {
	if a.committed {
		return a.dst.Header()
	}
	return a.hdr
}

func (a *pendingReply) WriteHeader(code int) {
	if a.status != 0 {
		return
	}
	a.status = code
	if code < 400 {
		a.commit()
	}
}

func (a *pendingReply) Write(p []byte) (int, error) {
	if a.status == 0 {
		a.WriteHeader(http.StatusOK)
	}
	if a.committed {
		return a.dst.Write(p)
	}
	return a.held.Write(p)
}

// Flush passes flushes through once the reply is going to the client.
func (a *pendingReply) Flush() {
	if !a.committed {
		return
	}
	if f, ok := a.dst.(http.Flusher); ok {
		f.Flush()
	}
}

// commit sends the status, headers and any held body to the client.
func (a *pendingReply) commit() {
	if a.committed {
		return
	}
	a.committed = true
	h := a.dst.Header()
	for k, v := range a.hdr {
		h[k] = v
	}
	a.dst.WriteHeader(a.status)
	if a.held.Len() > 0 {
		a.dst.Write(a.held.Bytes())
	}
	a.held.Reset()
}

// release sends the reply as it stands, for when no candidate follows.
func (a *pendingReply) release() {
	if a.committed {
		return
	}
	if a.status == 0 {
		a.status = http.StatusOK
	}
	a.commit()
}
