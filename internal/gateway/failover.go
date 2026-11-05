package gateway

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// restFor is how long a key that failed sits out: later requests try the
// other keys first, and come back to it once the time is up.
const restFor = time.Minute

// failoverStatus reports whether an upstream error means "try the next
// candidate": the rate limit is hit, the quota is spent, or the service is
// down or overloaded. Anything else (a 400, say) is the caller's own error and
// goes back as is.
func failoverStatus(status int, body []byte) bool {
	switch status {
	case 402, 429, 502, 503, 504, 529:
		return true
	case 403:
		msg := strings.ToLower(string(body))
		// only words that name the quota itself: "exceeded max tokens" is the
		// caller's own error, not a spent quota
		for _, w := range []string{"quota", "credit", "balance", "insufficient", "余额", "额度", "用完"} {
			if strings.Contains(msg, w) {
				return true
			}
		}
	}
	return false
}

// attemptWriter holds back an attempt's reply until it is known to succeed.
// A success (any status below 400) goes straight through; an error is kept
// aside, so it can be dropped and the next candidate tried without the
// client ever seeing it.
type attemptWriter struct {
	dst       http.ResponseWriter
	hdr       http.Header
	status    int
	body      bytes.Buffer
	committed bool
}

func (a *attemptWriter) Header() http.Header {
	if a.committed {
		return a.dst.Header()
	}
	if a.hdr == nil {
		a.hdr = http.Header{}
	}
	return a.hdr
}

func (a *attemptWriter) WriteHeader(code int) {
	if a.status != 0 {
		return
	}
	a.status = code
	if code < 400 {
		a.commit()
	}
}

func (a *attemptWriter) Write(b []byte) (int, error) {
	if a.status == 0 {
		a.WriteHeader(http.StatusOK)
	}
	if !a.committed {
		return a.body.Write(b)
	}
	return a.dst.Write(b)
}

// Flush passes a flush on once the reply is going to the client.
func (a *attemptWriter) Flush() {
	if !a.committed {
		return
	}
	if f, ok := a.dst.(http.Flusher); ok {
		f.Flush()
	}
}

// commit sends the held headers and status to the client.
func (a *attemptWriter) commit() {
	if a.committed {
		return
	}
	a.committed = true
	for k, v := range a.hdr {
		a.dst.Header()[k] = v
	}
	code := a.status
	if code == 0 {
		code = http.StatusOK
	}
	a.dst.WriteHeader(code)
}

// retryable reports whether the held reply is an error worth another candidate.
func (a *attemptWriter) retryable() bool {
	return !a.committed && a.status >= 400 && failoverStatus(a.status, a.body.Bytes())
}

// flush sends a held error reply to the client; nothing held means nothing to do.
func (a *attemptWriter) flush() {
	if a.committed || a.status == 0 {
		return
	}
	a.commit()
	a.dst.Write(a.body.Bytes())
}

// restKey names a candidate for the rest list: the provider and the key.
func restKey(c provider.Choice) string {
	return c.Provider.ID + "\x00" + c.Ref
}

// candidateLabel names a candidate in a call's fallback note.
func candidateLabel(c provider.Choice) string {
	if c.Name != "" {
		return c.Provider.ID + " (" + c.Name + ")"
	}
	return c.Provider.ID
}

// rest sets a candidate aside for restFor.
func (s *Server) rest(c provider.Choice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rests == nil {
		s.rests = make(map[string]time.Time)
	}
	s.rests[restKey(c)] = time.Now().Add(restFor)
}

// order puts the candidates that are not resting first, keeping their order;
// resting ones follow, so they are tried only when nothing else is left.
func (s *Server) order(cs []provider.Choice) []provider.Choice {
	return orderBy(s, cs, func(c provider.Choice) string { return restKey(c) })
}

// orderBy is order for any candidate type, keyed by key.
func orderBy[T any](s *Server, xs []T, key func(T) string) []T {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var ready, resting []T
	for _, x := range xs {
		k := key(x)
		if t, ok := s.rests[k]; ok {
			if now.Before(t) {
				resting = append(resting, x)
				continue
			}
			delete(s.rests, k)
		}
		ready = append(ready, x)
	}
	return append(ready, resting...)
}

// candidate is one attempt: a key of a provider, and the model it is asked for.
// A fallback target has its own model; the primary's is the requested one.
type candidate struct {
	choice provider.Choice
	model  string
}

// candidates lists what a request may try: the provider's own keys for the
// requested model, then the keys of each fallback target, in list order.
// Entries that do not resolve (no "/", unknown provider, no key) are skipped.
func candidates(p provider.Provider, from provider.Protocol, model string) []candidate {
	var out []candidate
	for _, c := range p.Choices(from, model) {
		out = append(out, candidate{c, model})
	}
	for _, entry := range p.Fallback {
		pid, fmodel, ok := strings.Cut(entry, "/")
		pid, fmodel = strings.TrimSpace(pid), strings.TrimSpace(fmodel)
		if !ok || pid == "" || fmodel == "" {
			continue
		}
		q, err := provider.Find(pid)
		if err != nil || !q.Ready() {
			continue
		}
		for _, c := range q.Choices(from, fmodel) {
			out = append(out, candidate{c, fmodel})
		}
	}
	return out
}

// orderCandidates is order for candidates.
func (s *Server) orderCandidates(cs []candidate) []candidate {
	return orderBy(s, cs, func(c candidate) string { return restKey(c.choice) })
}
