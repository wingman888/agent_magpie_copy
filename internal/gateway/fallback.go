package gateway

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// restFor is how long a provider that just failed with a switchable error is
// sent to the back of the candidate list. The rest is in memory only, so a
// restart starts every provider fresh.
const restFor = time.Minute

// candidate is one provider and model a request may be answered by.
type candidate struct {
	p     provider.Provider
	model string
}

// plan lists who may answer a request, in the order to try them: the primary
// first, then its configured fallbacks. Fallbacks that do not resolve are
// skipped, and so are repeats of an earlier entry. Fallbacks are not expanded:
// a fallback provider's own list is never consulted. Providers that are
// resting after a recent switchable failure move behind the others, and stay
// in the list so they are still tried when nothing else is left.
func (s *Server) plan(primary provider.Provider, model string) []candidate {
	cands := []candidate{{primary, model}}
	if len(primary.Fallback) == 0 {
		return cands
	}
	seen := map[string]bool{primary.ID + "\x00" + model: true}
	for _, id := range primary.Fallback {
		bp, bm, ok := provider.Resolve(id)
		if !ok {
			continue
		}
		key := bp.ID + "\x00" + bm
		if seen[key] {
			continue
		}
		seen[key] = true
		cands = append(cands, candidate{bp, bm})
	}
	if len(cands) == 1 {
		return cands
	}
	var up, down []candidate
	for _, c := range cands {
		if s.resting(c.p.ID) {
			down = append(down, c)
		} else {
			up = append(up, c)
		}
	}
	return append(up, down...)
}

// rest starts a provider's rest period after a switchable failure.
func (s *Server) rest(id string) {
	s.restMu.Lock()
	defer s.restMu.Unlock()
	if s.restUntil == nil {
		s.restUntil = make(map[string]time.Time)
	}
	s.restUntil[id] = time.Now().Add(restFor)
}

// resting reports whether a provider is inside its rest period.
func (s *Server) resting(id string) bool {
	s.restMu.Lock()
	defer s.restMu.Unlock()
	until, ok := s.restUntil[id]
	return ok && time.Now().Before(until)
}

// attempt sends a request to one candidate and writes its reply to w. It
// returns the protocol the provider answered in, the status, and the error
// text ("" on success). The provider name is the prefix of that text.
func (s *Server) attempt(w http.ResponseWriter, r *http.Request, from provider.Protocol, p provider.Provider, model string, body []byte, u *Usage) (provider.Protocol, int, string) {
	// A Claude Code subscription must run through the genuine binary. Direct
	// OAuth HTTP requests are content-classified as third-party traffic when
	// they carry another agent's harness (Pi, OpenCode, and others).
	if p.Account != nil && p.Account.Agent == "claude" {
		status, msg := s.serveClaudeSubscription(w, r, from, model, body, u)
		return provider.Anthropic, status, msg
	}
	// a backend that only streams gets a non-streaming request translated
	// (the provider is always streamed on that path) rather than relayed
	relay := p.Base(from) != "" && (p.Account == nil || !p.Account.Stream || streamOf(body))
	if relay {
		status, msg := s.passthrough(w, r, p, from, model, body, u)
		return from, status, msg
	}
	to := p.Speaks()
	if len(to) == 0 {
		msg := p.Name + " has no endpoint configured"
		return "", writeError(w, from, 502, msg), msg
	}
	status, msg := s.translate(w, r, p, from, to[0], model, body, u)
	return to[0], status, msg
}

// quotaWords mark a 400, 401 or 403 as a quota, balance, rate or overload
// problem, which another provider may not share. Matching ignores case.
var quotaWords = []string{
	"quota", "insufficient", "balance", "credit", "billing", "exceeded",
	"rate limit", "usage limit", "too many requests", "overloaded",
	"余额", "额度", "欠费", "限流", "频率", "套餐", "用量", "上限",
}

// retryable reports whether a failed attempt may be sent on to the next
// candidate. detail is the upstream's error text, without the provider name.
func retryable(status int, detail string) bool {
	switch {
	case status == 402, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 401, status == 403:
		low := strings.ToLower(detail)
		for _, w := range quotaWords {
			if strings.Contains(low, w) {
				return true
			}
		}
	}
	return false
}

// detail strips the "<provider name>: " prefix that passthrough and translate
// put in front of upstream errors.
func (c candidate) detail(msg string) string {
	return strings.TrimPrefix(msg, c.p.Name+": ")
}

// attemptWriter holds back a reply until the attempt is known to stand. A
// success status goes straight through, so streamed output is never delayed
// or held. An error status is kept in memory: it is either sent to the client
// when no candidate is left, or dropped when the request moves on.
type attemptWriter struct {
	dst       http.ResponseWriter
	hdr       http.Header
	held      bool
	status    int
	buf       bytes.Buffer
	committed bool
}

func newAttemptWriter(dst http.ResponseWriter) *attemptWriter {
	return &attemptWriter{dst: dst, hdr: http.Header{}}
}

func (a *attemptWriter) Header() http.Header { return a.hdr }

func (a *attemptWriter) WriteHeader(code int) {
	if a.committed || a.held {
		return
	}
	if code >= 400 {
		a.held, a.status = true, code
		return
	}
	a.commit(code)
}

func (a *attemptWriter) Write(p []byte) (int, error) {
	if !a.committed && !a.held {
		a.commit(http.StatusOK)
	}
	if a.committed {
		return a.dst.Write(p)
	}
	return a.buf.Write(p)
}

// Flush passes flushes through once the reply is out; a held error has
// nothing to flush yet.
func (a *attemptWriter) Flush() {
	if !a.committed {
		return
	}
	if f, ok := a.dst.(http.Flusher); ok {
		f.Flush()
	}
}

func (a *attemptWriter) commit(code int) {
	h := a.dst.Header()
	for k, v := range a.hdr {
		h[k] = v
	}
	a.dst.WriteHeader(code)
	a.committed = true
}

// flush sends a held error to the client. It does nothing when the reply
// was already committed or nothing was written.
func (a *attemptWriter) flush() {
	if !a.held {
		return
	}
	a.commit(a.status)
	a.dst.Write(a.buf.Bytes())
	a.held = false
}
