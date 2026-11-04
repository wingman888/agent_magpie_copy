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

// restNote is a provider's rest period and the failure that started it.
type restNote struct {
	until  time.Time
	reason string
}

// rest starts a provider's rest period after a switchable failure.
func (s *Server) rest(id, reason string) {
	s.restMu.Lock()
	defer s.restMu.Unlock()
	if s.rests == nil {
		s.rests = make(map[string]restNote)
	}
	s.rests[id] = restNote{until: time.Now().Add(restFor), reason: reason}
}

// restingNote returns the rest period of a provider, if it is still running.
func (s *Server) restingNote(id string) (restNote, bool) {
	s.restMu.Lock()
	defer s.restMu.Unlock()
	n, ok := s.rests[id]
	return n, ok && time.Now().Before(n.until)
}

// resting reports whether a provider is inside its rest period.
func (s *Server) resting(id string) bool {
	_, ok := s.restingNote(id)
	return ok
}

// restingNotes explains, for the call record, why the resting candidates in
// cands were not tried. They are the ones behind the candidate that answered.
func (s *Server) restingNotes(cands []candidate) []string {
	var out []string
	for _, c := range cands {
		if n, ok := s.restingNote(c.p.ID); ok {
			out = append(out, c.p.ID+": resting after "+n.reason)
		}
	}
	return out
}

// attempt sends a request to one candidate and writes its reply to w. It
// returns the protocol the provider answered in, the status, the error text
// ("" on success; the provider name is its prefix), and the upstream's error
// body verbatim ("" when there was none).
func (s *Server) attempt(w http.ResponseWriter, r *http.Request, from provider.Protocol, p provider.Provider, model string, body []byte, u *Usage) (provider.Protocol, int, string, string) {
	// A Claude Code subscription must run through the genuine binary. Direct
	// OAuth HTTP requests are content-classified as third-party traffic when
	// they carry another agent's harness (Pi, OpenCode, and others).
	if p.Account != nil && p.Account.Agent == "claude" {
		status, msg := s.serveClaudeSubscription(w, r, from, model, body, u)
		return provider.Anthropic, status, msg, ""
	}
	// a backend that only streams gets a non-streaming request translated
	// (the provider is always streamed on that path) rather than relayed
	relay := p.Base(from) != "" && (p.Account == nil || !p.Account.Stream || streamOf(body))
	if relay {
		status, msg, raw := s.passthroughRaw(w, r, p, from, model, body, u)
		return from, status, msg, raw
	}
	to := p.Speaks()
	if len(to) == 0 {
		msg := p.Name + " has no endpoint configured"
		return "", writeError(w, from, 502, msg), msg, ""
	}
	status, msg, raw := s.translateRaw(w, r, p, from, to[0], model, body, u)
	return to[0], status, msg, raw
}

// quotaWords mark a 400, 401 or 403 as a quota, balance, rate or overload
// problem, which another provider may not share. Matching ignores case.
var quotaWords = []string{
	"quota", "insufficient", "balance", "credit", "billing", "exceeded",
	"rate limit", "usage limit", "too many requests", "overloaded",
	"余额", "额度", "欠费", "限流", "频率", "套餐", "用量", "上限",
}

// retryable reports whether a failed attempt may be sent on to the next
// candidate. detail is the upstream's error text, without the provider name,
// and raw its error body; a quota word in either counts.
func retryable(status int, detail, raw string) bool {
	switch {
	case status == 402, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 401, status == 403:
		low := strings.ToLower(detail + "\n" + raw)
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

// attemptWriter holds back a reply until the attempt is known to stand. An
// error status is kept in memory: it is either sent to the client when no
// candidate is left, or dropped when the request moves on. A success status
// is also held until the first byte of body is written, so an attempt that
// fails before any output can still be switched without the client having
// seen a 200. Once the reply is committed, everything passes straight through.
type attemptWriter struct {
	dst       http.ResponseWriter
	hdr       http.Header
	held      bool
	status    int
	buf       bytes.Buffer
	committed bool
	code      int
	hasCode   bool
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
		a.hasCode = false
		return
	}
	if !a.hasCode {
		a.code, a.hasCode = code, true
	}
}

func (a *attemptWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if !a.committed && !a.held {
		code := http.StatusOK
		if a.hasCode {
			code = a.code
		}
		a.commit(code)
	}
	if a.committed {
		return a.dst.Write(p)
	}
	return a.buf.Write(p)
}

// Flush passes flushes through once the reply is out; a held error or a
// success still waiting for its first byte has nothing to flush yet.
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

// flush sends a held error to the client, or a success status that never got
// a body byte. It does nothing when the reply was already committed.
func (a *attemptWriter) flush() {
	if a.committed {
		return
	}
	if a.held {
		a.commit(a.status)
		a.dst.Write(a.buf.Bytes())
		a.held = false
		return
	}
	if a.hasCode {
		a.commit(a.code)
	}
}
