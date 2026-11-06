package gateway

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// restPeriod is how long a provider that just failed with a switchable error
// is passed over by later requests.
const restPeriod = time.Minute

// candidate is one provider/model a request may be sent to.
type candidate struct {
	p     provider.Provider
	model string
	ref   string // "provider/model", as the config spells it
	main  bool   // the model the agent asked for
}

// candidates lists the agent's own target followed by the fallbacks the
// provider configures, in order. Entries that do not resolve are skipped, as
// are repeats of a provider/model already listed. Fallbacks are not expanded:
// a fallback's own list plays no part.
func candidates(p provider.Provider, model string) []candidate {
	out := []candidate{{p: p, model: model, ref: p.ID + "/" + model, main: true}}
	seen := map[string]bool{p.ID + "\x00" + model: true}
	for _, ref := range p.Fallback {
		if !provider.Known(ref) {
			continue
		}
		pid, m, _ := provider.SplitRef(ref)
		fp, err := provider.Find(pid)
		if err != nil || !fp.Ready() {
			continue
		}
		key := fp.ID + "\x00" + m
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, candidate{p: *fp, model: m, ref: fp.ID + "/" + m})
	}
	return out
}

// quotaWords mark a 400, 401 or 403 reply as a limit rather than a bad
// request. Matching is done on the lowercased message.
var quotaWords = []string{
	"quota", "insufficient", "balance", "credit", "billing", "exceeded",
	"rate limit", "usage limit", "too many requests", "overloaded",
	"余额", "额度", "欠费", "限流", "频率", "套餐", "用量", "上限",
}

// switchable reports whether a failed reply means the provider cannot take
// the request now, so another one may.
func switchable(status int, msg string) bool {
	switch {
	case status == 402, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 401, status == 403:
		m := strings.ToLower(msg)
		for _, w := range quotaWords {
			if strings.Contains(m, w) {
				return true
			}
		}
	}
	return false
}

// nextCandidate picks the index of the next candidate to try: the first one
// not resting, or the first one when all are resting.
func (s *Server) nextCandidate(cands []candidate) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for i, c := range cands {
		if !now.Before(s.cooling[c.p.ID].until) {
			return i
		}
	}
	return 0
}

// restProvider sends a provider to its rest period after a switchable failure.
func (s *Server) restProvider(providerID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cooling == nil {
		s.cooling = make(map[string]restEntry)
	}
	s.cooling[providerID] = restEntry{until: time.Now().Add(restPeriod), reason: reason}
}

// restReason is why a provider is resting: its last switchable failure.
func (s *Server) restReason(providerID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cooling[providerID].reason
}

// serveFallbacks tries the candidates in turn until one answers without a
// switchable error. The reply of the last one tried goes to the agent, and
// call records who answered and which providers failed before.
func (s *Server) serveFallbacks(w http.ResponseWriter, r *http.Request, from provider.Protocol, cands []candidate, body []byte, call *Call) candidate {
	var failures []string
	noted := map[string]bool{}
	for {
		i := s.nextCandidate(cands)
		// candidates passed over because they are resting count as failed
		// here, so the record says why another provider answered
		for _, skipped := range cands[:i] {
			if !noted[skipped.p.ID] {
				noted[skipped.p.ID] = true
				failures = append(failures, skipped.p.ID+": resting after "+s.restReason(skipped.p.ID))
			}
		}
		c := cands[i]
		rest := append(append([]candidate{}, cands[:i]...), cands[i+1:]...)
		// the reply is held back until it is known to be a success
		aw := newAttemptWriter(w)
		var u Usage
		to, status, msg := s.attempt(aw, r, c.p, from, c.model, body, &u)
		retry := status >= 400 && switchable(status, msg)
		reason := fmt.Sprintf("HTTP %d %s", status, oneLine(msg))
		if retry {
			s.restProvider(c.p.ID, reason)
		}
		if !retry || len(rest) == 0 || r.Context().Err() != nil {
			aw.release()
			call.To, call.Status, call.Error, call.Usage = to, status, msg, u
			call.Provider = c.p.ID
			if !c.main {
				call.Model = c.ref
			}
			if len(failures) > 0 {
				call.Fallback = strings.Join(failures, "; ")
			}
			return c
		}
		failures = append(failures, c.p.ID+": "+reason)
		cands = rest
	}
}

// oneLine makes a failure reason fit one "; "-separated item.
func oneLine(msg string) string {
	s := strings.NewReplacer("\r", " ", "\n", " ", ";", ",").Replace(strings.TrimSpace(msg))
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return s
}

// attemptWriter sits between one attempt and the client. An error reply is
// kept in memory, so it can be dropped if another candidate answers. A
// success reply is passed through from its status line on, and once any of
// it has gone out no switch is possible.
type attemptWriter struct {
	dst    http.ResponseWriter
	hdr    http.Header
	status int
	held   bool // an error reply, kept back
	sent   bool // headers and status have reached dst
	body   bytes.Buffer
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
	if code >= 400 {
		a.held = true
		return
	}
	a.commit()
}

// commit sends the headers and status of a success reply to the client.
func (a *attemptWriter) commit() {
	if a.sent {
		return
	}
	a.sent = true
	dh := a.dst.Header()
	for k, v := range a.hdr {
		dh[k] = v
	}
	a.dst.WriteHeader(a.status)
}

func (a *attemptWriter) Write(p []byte) (int, error) {
	if a.status == 0 {
		a.WriteHeader(http.StatusOK)
	}
	if a.held {
		return a.body.Write(p)
	}
	return a.dst.Write(p)
}

func (a *attemptWriter) Flush() {
	if a.sent {
		if f, ok := a.dst.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// release sends a held error reply to the client. It does nothing once a
// reply has been sent or when there is no held error.
func (a *attemptWriter) release() {
	if !a.held || a.sent {
		return
	}
	a.sent = true
	dh := a.dst.Header()
	for k, v := range a.hdr {
		dh[k] = v
	}
	a.dst.WriteHeader(a.status)
	a.dst.Write(a.body.Bytes())
}

// restEntry is a provider's rest period and the failure that started it.
type restEntry struct {
	until  time.Time
	reason string
}
