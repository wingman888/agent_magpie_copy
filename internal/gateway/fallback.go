package gateway

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// cooldownFor is how long a provider that failed with a retryable error is
// moved behind the others. It lives in memory only.
const cooldownFor = time.Minute

// candidate is one provider and model a request may be answered by.
type candidate struct {
	p     provider.Provider
	model string
	// cool is the reason this candidate is resting after a recent retryable
	// failure; empty when it is ready.
	cool string
}

// cooldown records the last retryable failure of a provider.
type cooldown struct {
	until  time.Time
	reason string
}

// answer is one attempt at one provider.
type answer struct {
	to        provider.Protocol
	status    int
	msg       string
	usage     Usage
	attempted bool // false when the provider has no endpoint to call
}

// candidates lists the provider and model first, then the provider's own
// fallback list in order. Unresolvable and repeated entries are dropped;
// fallbacks are not expanded recursively. Candidates resting after a recent
// retryable failure move behind the ready ones.
func (s *Server) candidates(p provider.Provider, model string) []candidate {
	cands := []candidate{{p: p, model: model}}
	seen := map[string]bool{p.ID + "\x00" + model: true}
	for _, id := range p.Fallback {
		q, m, ok := provider.Resolve(id)
		if !ok {
			continue
		}
		key := q.ID + "\x00" + m
		if seen[key] {
			continue
		}
		seen[key] = true
		cands = append(cands, candidate{p: q, model: m})
	}
	if len(cands) == 1 {
		return cands
	}
	var ready, resting []candidate
	for _, c := range cands {
		c.cool = s.coolingReason(c.p.ID)
		if c.cool == "" {
			ready = append(ready, c)
		} else {
			resting = append(resting, c)
		}
	}
	return append(ready, resting...)
}

// failover answers from the first candidate that can take the request. It
// returns the answer, the candidate that gave it, and the "<id>: <reason>"
// list of earlier failures ("; " separated), empty when nothing was switched.
func (s *Server) failover(w http.ResponseWriter, r *http.Request, from provider.Protocol, cands []candidate, body []byte) (answer, candidate, string) {
	var failed []string
	for i, c := range cands {
		g := &deferredWriter{ResponseWriter: w}
		a := s.attempt(g, r, from, c.p, c.model, body)
		if !g.committed && retryable(c.p, a.status, a.msg) {
			s.markCooling(c.p, a.status, a.msg)
		}
		last := i == len(cands)-1
		if g.committed || last || !retryable(c.p, a.status, a.msg) || r.Context().Err() != nil {
			g.finish(from, a)
			// candidates skipped for resting are earlier failures too
			for _, skipped := range cands[i+1:] {
				if skipped.cool != "" {
					failed = append(failed, skipped.p.ID+": "+skipped.cool)
				}
			}
			return a, c, strings.Join(failed, "; ")
		}
		failed = append(failed, c.p.ID+": "+reasonOf(c.p, a.status, a.msg))
	}
	return answer{}, candidate{}, "" // unreachable: the last candidate always returns above
}

// attempt sends the request to one provider and model, through the path the
// provider's configuration calls for.
func (s *Server) attempt(w http.ResponseWriter, r *http.Request, from provider.Protocol, p provider.Provider, model string, body []byte) answer {
	var a answer
	if p.Account != nil && p.Account.Agent == "claude" {
		a.to = provider.Anthropic
		a.status, a.msg = s.serveClaudeSubscription(w, r, from, model, body, &a.usage)
		a.attempted = true
		return a
	}
	// a backend that only streams gets a non-streaming request translated
	// (the provider is always streamed on that path) rather than relayed
	relay := p.Base(from) != "" && (p.Account == nil || !p.Account.Stream || streamOf(body))
	if relay {
		a.to = from
		a.status, a.msg = s.passthrough(w, r, p, from, model, body, &a.usage)
		a.attempted = true
		return a
	}
	to := p.Speaks()
	if len(to) == 0 {
		a.status, a.msg = 502, p.Name+" has no endpoint configured"
		writeError(w, from, a.status, a.msg)
		return a
	}
	a.to = to[0]
	a.status, a.msg = s.translate(w, r, p, from, to[0], model, body, &a.usage)
	a.attempted = true
	return a
}

// retryable reports whether a failure means the provider cannot take this
// request now, so another candidate should. Request-level errors do not.
func retryable(p provider.Provider, status int, msg string) bool {
	switch {
	case status == 402, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 401, status == 403:
		return quotaWording(strings.ReplaceAll(msg, p.Name, ""))
	}
	return false
}

// quotaWords mark a 400, 401 or 403 as a quota, balance, rate or overload
// refusal. Matching is case-insensitive.
var quotaWords = []string{
	"quota", "insufficient", "balance", "credit", "billing", "exceeded",
	"rate limit", "usage limit", "too many requests", "overloaded",
	"余额", "额度", "欠费", "限流", "频率", "套餐", "用量", "上限",
}

func quotaWording(msg string) bool {
	m := strings.ToLower(msg)
	for _, w := range quotaWords {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// reasonOf is the failure text without the provider's own name in front.
func reasonOf(p provider.Provider, status int, msg string) string {
	return fmt.Sprintf("%d %s", status, strings.TrimPrefix(msg, p.Name+": "))
}

func (s *Server) markCooling(p provider.Provider, status int, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cooling == nil {
		s.cooling = make(map[string]cooldown)
	}
	s.cooling[p.ID] = cooldown{until: time.Now().Add(cooldownFor), reason: reasonOf(p, status, msg)}
}

// coolingReason returns why a provider is resting, or "" when it is ready.
func (s *Server) coolingReason(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cooling[id]
	if !ok || !time.Now().Before(c.until) {
		return ""
	}
	return c.reason
}

// deferredWriter holds back an error reply so that it can be replaced by
// another candidate's answer. A success status is committed at once and
// passes through unchanged, so streamed output is never held or mixed with
// a fallback's output.
type deferredWriter struct {
	http.ResponseWriter // the client's writer
	hdr                 http.Header
	status              int
	buf                 bytes.Buffer
	committed           bool
}

func (g *deferredWriter) Header() http.Header {
	if g.committed {
		return g.ResponseWriter.Header()
	}
	if g.hdr == nil {
		g.hdr = make(http.Header)
	}
	return g.hdr
}

func (g *deferredWriter) WriteHeader(code int) {
	if g.status != 0 || g.committed {
		return
	}
	g.status = code
	if code < 400 {
		g.commit()
	}
}

func (g *deferredWriter) Write(p []byte) (int, error) {
	if !g.committed && g.status >= 400 {
		return g.buf.Write(p)
	}
	if !g.committed {
		g.commit()
	}
	return g.ResponseWriter.Write(p)
}

func (g *deferredWriter) Flush() {
	if !g.committed {
		return
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// commit sends the held headers and status, then any held body, to the client.
func (g *deferredWriter) commit() {
	if g.committed {
		return
	}
	g.committed = true
	h := g.ResponseWriter.Header()
	for k, v := range g.hdr {
		h[k] = v
	}
	if g.status == 0 {
		g.status = http.StatusOK
	}
	g.ResponseWriter.WriteHeader(g.status)
	if g.buf.Len() > 0 {
		g.ResponseWriter.Write(g.buf.Bytes())
	}
}

// finish settles a writer whose attempt was the last one: a held reply goes
// out as it was, and an attempt that wrote nothing is answered with its error.
func (g *deferredWriter) finish(from provider.Protocol, a answer) {
	if g.committed {
		return
	}
	if g.status != 0 {
		g.commit()
		return
	}
	if a.status >= 400 {
		writeError(g.ResponseWriter, from, a.status, a.msg)
	}
}
