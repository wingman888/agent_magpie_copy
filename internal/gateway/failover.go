package gateway

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// restPeriod is how long a provider that just failed with a switchable error
// is moved behind the others. The memory lives in the gateway process only.
const restPeriod = time.Minute

// fallbackWords are the phrases that mark a 400, 401 or 403 as a quota, rate
// or overload problem rather than a bad request. Matched lowercased.
var fallbackWords = []string{
	"quota", "insufficient", "balance", "credit", "billing", "exceeded",
	"rate limit", "usage limit", "too many requests", "overloaded",
	"余额", "额度", "欠费", "限流", "频率", "套餐", "用量", "上限",
}

// switchable reports whether a failed reply may be retried on the next
// provider. body must be the upstream's raw reply body, never the text the
// client would see, so a provider's display name cannot match.
func switchable(status int, body string) bool {
	switch {
	case status == 402, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 401, status == 403:
		return hasFallbackWord(body)
	}
	return false
}

// hasFallbackWord reports whether text speaks of quota, balance, rate limit
// or overload, ignoring case.
func hasFallbackWord(text string) bool {
	msg := strings.ToLower(text)
	for _, w := range fallbackWords {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

// fallbackStep is one candidate: a provider and the model to ask it for.
type fallbackStep struct {
	p     provider.Provider
	model string
}

// fallbackChain lists the primary, then the provider's own fallback entries in
// order. Unresolvable entries and repeats are skipped. Providers resting after
// a recent failure are moved behind the others, but never removed.
func (s *Server) fallbackChain(primary provider.Provider, model string) []fallbackStep {
	steps := []fallbackStep{{p: primary, model: model}}
	seen := map[string]bool{primary.ID + "\x00" + model: true}
	for _, ref := range primary.Fallback {
		fp, fm, ok := provider.Resolve(ref)
		if !ok {
			continue
		}
		key := fp.ID + "\x00" + fm
		if seen[key] {
			continue
		}
		seen[key] = true
		steps = append(steps, fallbackStep{p: fp, model: fm})
	}
	s.fbMu.Lock()
	defer s.fbMu.Unlock()
	now := time.Now()
	var ready, resting []fallbackStep
	for _, st := range steps {
		if until, ok := s.resting[st.p.ID]; ok && now.Before(until) {
			resting = append(resting, st)
		} else {
			ready = append(ready, st)
		}
	}
	return append(ready, resting...)
}

// rest puts a provider behind the others for restPeriod.
func (s *Server) rest(providerID string) {
	s.fbMu.Lock()
	defer s.fbMu.Unlock()
	if s.resting == nil {
		s.resting = make(map[string]time.Time)
	}
	s.resting[providerID] = time.Now().Add(restPeriod)
}

// attemptWriter sits between one candidate and the client. An error reply is
// held back, so it can be dropped if the next candidate is tried. A success
// reply is sent on its first body byte; once sent, the attempt is final.
type attemptWriter struct {
	w         http.ResponseWriter
	hdr       http.Header
	status    int
	body      bytes.Buffer // held error body, or nothing
	committed bool
	// classified is set when the provider code judged the failure from the
	// upstream's own reply (or the CLI's error text); retry is that verdict.
	// Without it only the status code decides.
	classified bool
	retry      bool
}

// isAttempt reports whether w is a candidate's writer under fallback.
func isAttempt(w http.ResponseWriter) bool {
	_, ok := w.(*attemptWriter)
	return ok
}

// markUpstream classifies a failed upstream reply by its status and raw body.
// It does nothing when w is not a fallback attempt.
func markUpstream(w http.ResponseWriter, status int, raw []byte) {
	if a, ok := w.(*attemptWriter); ok {
		a.classified = true
		a.retry = switchable(status, string(raw))
	}
}

// markVerdict records a verdict for a failure that carries no upstream status.
// It does nothing when w is not a fallback attempt.
func markVerdict(w http.ResponseWriter, retry bool) {
	if a, ok := w.(*attemptWriter); ok {
		a.classified = true
		a.retry = retry
	}
}

// retryable reports whether the held failure may move to the next candidate.
func (a *attemptWriter) retryable() bool {
	if a.classified {
		return a.retry
	}
	return switchable(a.status, "")
}

func newAttemptWriter(w http.ResponseWriter) *attemptWriter {
	return &attemptWriter{w: w, hdr: make(http.Header)}
}

func (a *attemptWriter) Header() http.Header { return a.hdr }

func (a *attemptWriter) WriteHeader(code int) {
	if a.status != 0 {
		return
	}
	a.status = code
}

func (a *attemptWriter) Write(b []byte) (int, error) {
	if a.status == 0 {
		a.status = http.StatusOK
	}
	if a.status >= 400 && !a.committed {
		return a.body.Write(b)
	}
	if !a.committed {
		a.commit()
	}
	return a.w.Write(b)
}

func (a *attemptWriter) Flush() {
	if !a.committed {
		return
	}
	if f, ok := a.w.(http.Flusher); ok {
		f.Flush()
	}
}

// failed reports whether the attempt produced an error reply that is still held.
func (a *attemptWriter) failed() bool {
	return a.status >= 400 && !a.committed
}

// commit sends the status and headers to the client.
func (a *attemptWriter) commit() {
	h := a.w.Header()
	for k, v := range a.hdr {
		h[k] = v
	}
	a.w.WriteHeader(a.status)
	a.committed = true
}

// finish sends whatever is still held: a held error, or an empty success.
func (a *attemptWriter) finish() {
	if a.committed {
		return
	}
	if a.status == 0 {
		a.status = http.StatusOK
	}
	a.commit()
	if a.body.Len() > 0 {
		a.w.Write(a.body.Bytes())
	}
}

// attempt runs one candidate, the same dispatch serve uses for a provider
// without fallbacks. It returns the protocol the provider spoke, the status
// and the error message, if any.
func (s *Server) attempt(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte, st fallbackStep, u *Usage) (provider.Protocol, int, string) {
	p := st.p
	if p.Account != nil && p.Account.Agent == "claude" {
		status, msg := s.serveClaudeSubscription(w, r, from, st.model, body, u)
		return provider.Anthropic, status, msg
	}
	relay := p.Base(from) != "" && (p.Account == nil || !p.Account.Stream || streamOf(body))
	if relay {
		status, msg := s.passthrough(w, r, p, from, st.model, body, u)
		return from, status, msg
	}
	to := p.Speaks()
	if len(to) == 0 {
		msg := p.Name + " has no endpoint configured"
		return from, writeError(w, from, 502, msg), msg
	}
	status, msg := s.translate(w, r, p, from, to[0], st.model, body, u)
	return to[0], status, msg
}

// serveFallback serves a request whose provider has fallbacks. It tries the
// candidates in order and answers with the first that does not fail
// switchably. The call record names the provider that answered.
func (s *Server) serveFallback(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte, primary provider.Provider, model string, call *Call, start time.Time, finish func()) {
	chain := s.fallbackChain(primary, model)
	var (
		cur      fallbackStep
		curIndex int
		to       provider.Protocol
		status   int
		msg      string
		u        Usage
		aw       *attemptWriter
		trail    []string
	)
	for i, st := range chain {
		// a client that has gone away gets no further attempts
		if i > 0 && r.Context().Err() != nil {
			break
		}
		cur, curIndex = st, i
		aw = newAttemptWriter(w)
		u = Usage{}
		call.Provider = st.p.ID
		to, status, msg = s.attempt(aw, r, from, body, st, &u)
		if aw.failed() && aw.retryable() {
			s.rest(st.p.ID)
			if i < len(chain)-1 && r.Context().Err() == nil {
				reason := fmt.Sprintf("HTTP %d", aw.status)
				if summary := strings.TrimPrefix(msg, st.p.Name+": "); summary != "" {
					reason += ": " + summary
				}
				trail = append(trail, st.p.ID+": "+reason)
				continue
			}
		}
		break
	}
	aw.finish()
	call.Provider = cur.p.ID
	if curIndex > 0 {
		// the record names the model that actually answered
		call.Model = cur.p.ID + "/" + cur.model
	}
	call.To = to
	call.Status, call.Error = status, msg
	call.Usage = u
	call.Fallback = strings.Join(trail, "; ")
	call.Millis = time.Since(start).Milliseconds()
	finish()
	s.record(*call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: cur.p.ID, Model: cur.model,
		Input: u.Input, Output: u.Output, CacheRead: u.CacheRead,
		CacheWrite: u.CacheWrite, Reasoning: u.Reasoning, Millis: call.Millis, Status: status})
}
