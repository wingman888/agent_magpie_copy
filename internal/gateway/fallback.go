package gateway

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// restPeriod is how long a provider that just failed retryably is tried last.
const restPeriod = time.Minute

// candidate is one provider/model the gateway may send a request to.
type candidate struct {
	p     provider.Provider
	model string
}

// limitWords mark a 400/401/403 body as quota, balance, rate-limit or
// overload trouble, which another provider may not share.
var limitWords = []string{
	"quota", "insufficient", "balance", "credit", "billing", "exceeded", "rate limit",
	"usage limit", "too many requests", "overloaded",
	"余额", "额度", "欠费", "限流", "频率", "套餐", "用量", "上限",
}

// retryable reports whether an error response justifies trying the next
// candidate.
func retryable(status int, body []byte) bool {
	switch {
	case status == 402, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 401, status == 403:
		low := strings.ToLower(string(body))
		for _, w := range limitWords {
			if strings.Contains(low, w) {
				return true
			}
		}
	}
	return false
}

// holdable statuses are kept back until the gateway knows whether to switch.
func holdable(status int) bool {
	switch status {
	case 400, 401, 402, 403, 408, 429:
		return true
	}
	return status >= 500
}

// attemptWriter holds back an error response so a later candidate can take
// over; anything else (a success header, the first stream chunk) goes
// straight to the client and ends the chance to switch.
type attemptWriter struct {
	dst       http.ResponseWriter
	hdr       http.Header
	status    int
	held      bool
	committed bool
	buf       bytes.Buffer
}

func newAttemptWriter(dst http.ResponseWriter) *attemptWriter {
	return &attemptWriter{dst: dst, hdr: http.Header{}}
}

func (a *attemptWriter) Header() http.Header { return a.hdr }

func (a *attemptWriter) WriteHeader(code int) {
	if a.status != 0 {
		return
	}
	a.status = code
	if holdable(code) {
		a.held = true
		return
	}
	a.commit()
}

func (a *attemptWriter) commit() {
	if a.committed {
		return
	}
	a.committed = true
	h := a.dst.Header()
	for k, v := range a.hdr {
		h[k] = append([]string(nil), v...)
	}
	a.dst.WriteHeader(a.status)
}

func (a *attemptWriter) Write(b []byte) (int, error) {
	if a.status == 0 {
		a.WriteHeader(http.StatusOK)
	}
	if a.held && !a.committed {
		return a.buf.Write(b)
	}
	return a.dst.Write(b)
}

func (a *attemptWriter) Flush() {
	if !a.committed {
		return
	}
	if f, ok := a.dst.(http.Flusher); ok {
		f.Flush()
	}
}

// release sends a held response to the client.
func (a *attemptWriter) release() {
	if a.held && !a.committed {
		a.commit()
		a.dst.Write(a.buf.Bytes())
	}
}

// pending is true while an error response is held and unsent.
func (a *attemptWriter) pending() bool { return a.held && !a.committed }

func (s *Server) resting(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.rest[id]
	if ok && !time.Now().Before(t) {
		delete(s.rest, id)
		return false
	}
	return ok
}

func (s *Server) markRest(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rest == nil {
		s.rest = map[string]time.Time{}
	}
	s.rest[id] = time.Now().Add(restPeriod)
}

// candidates is the primary followed by its configured fallbacks, with
// providers in their rest period moved behind the others.
func (s *Server) candidates(p provider.Provider, model string) []candidate {
	out := []candidate{{p, model}}
	seen := map[string]bool{p.ID + "\x00" + model: true}
	for _, name := range p.Fallback {
		fp, fm, ok := provider.Resolve(name)
		if !ok {
			continue
		}
		k := fp.ID + "\x00" + fm
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, candidate{fp, fm})
	}
	if len(out) == 1 {
		return out
	}
	var fresh, tired []candidate
	for _, c := range out {
		if s.resting(c.p.ID) {
			tired = append(tired, c)
		} else {
			fresh = append(fresh, c)
		}
	}
	return append(fresh, tired...)
}

// failureNote is one "<id>: <reason>" entry of Call.Fallback.
func failureNote(id string, status int, msg string) string {
	msg = strings.Join(strings.Fields(strings.ReplaceAll(msg, ";", ",")), " ")
	if r := []rune(msg); len(r) > 120 {
		msg = string(r[:120]) + "…"
	}
	if msg == "" {
		return id + ": " + http.StatusText(status) + " (" + itoa(status) + ")"
	}
	return id + ": " + itoa(status) + " " + msg
}

func itoa(n int) string { return strconv.Itoa(n) }
