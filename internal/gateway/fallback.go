package gateway

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// restFor is how long a provider that just failed with a retryable error is
// moved behind its alternatives. It is kept in memory only.
const restFor = time.Minute

// quotaWords mark a 400, 401 or 403 as a money, rate or load refusal rather
// than a bad request. Matching ignores case (the text is lowered first).
var quotaWords = []string{
	"quota", "insufficient", "balance", "credit", "billing", "exceeded",
	"rate limit", "usage limit", "too many requests", "overloaded",
	"余额", "额度", "欠费", "限流", "频率", "套餐", "用量", "上限",
}

// hasQuotaWords reports whether text says the provider is out of quota,
// rate limited or overloaded.
func hasQuotaWords(text string) bool {
	low := strings.ToLower(text)
	for _, w := range quotaWords {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// retryable reports whether an upstream HTTP error means another provider
// may still answer the request.
func retryable(status int, body string) bool {
	switch {
	case status == 402, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 401, status == 403:
		return hasQuotaWords(body)
	}
	return false
}

// candidate is one provider and vendor model a request may be sent to.
type candidate struct {
	p     provider.Provider
	model string
}

func (c candidate) key() string { return c.p.ID + "\x00" + c.model }

// chain is the order a request tries: the model the agent asked for, then the
// provider's own fallback list. Unresolvable and repeated entries are skipped.
// Fallbacks are not expanded further.
func chain(main provider.Provider, model string) []candidate {
	first := candidate{main, model}
	out := []candidate{first}
	seen := map[string]bool{first.key(): true}
	for _, id := range main.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok {
			continue
		}
		c := candidate{fp, fm}
		if seen[c.key()] {
			continue
		}
		seen[c.key()] = true
		out = append(out, c)
	}
	return out
}

// restState is a provider's recent retryable failure.
type restState struct {
	until  time.Time
	reason string
}

// order puts resting providers' candidates after the others, keeping each
// group in chain order. It also returns the reasons of the resting providers
// it demoted, so the call record can still name them.
func (s *Server) order(cands []candidate) ([]candidate, map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var up, down []candidate
	resting := map[string]string{}
	for _, c := range cands {
		if r, ok := s.resting[c.p.ID]; ok && now.Before(r.until) {
			down = append(down, c)
			resting[c.p.ID] = r.reason
			continue
		}
		up = append(up, c)
	}
	return append(up, down...), resting
}

// setRest records that a provider just failed with a retryable error.
func (s *Server) setRest(providerID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resting[providerID] = restState{until: time.Now().Add(restFor), reason: reason}
}

// priorFailures names, in chain order, the candidates before the one that
// answered (or the last one tried) that failed. A resting provider skipped in
// this request counts with the reason it failed with earlier.
func priorFailures(cands []candidate, answered candidate, failed, resting map[string]string) string {
	var parts []string
	for _, c := range cands {
		if c.key() == answered.key() {
			break
		}
		if reason, ok := failed[c.key()]; ok {
			parts = append(parts, c.p.ID+": "+reason)
		} else if reason, ok := resting[c.p.ID]; ok {
			parts = append(parts, c.p.ID+": "+reason)
		}
	}
	return strings.Join(parts, "; ")
}

// attemptResult is the outcome of sending a request to one candidate.
type attemptResult struct {
	status int    // HTTP status the agent sees (or would see)
	err    string // text for the call record
	client string // error text for the agent when status >= 400; not yet written
	retry  bool   // true when another candidate may answer
	to     provider.Protocol
}

// failure builds the outcome of a transport error, which is retryable unless
// the client has gone.
func failure(p provider.Provider, err error) attemptResult {
	return attemptResult{status: http.StatusBadGateway, err: err.Error(), client: p.Name + ": " + err.Error(), retry: true}
}

// upstreamFailure builds the outcome of an HTTP error reply.
func upstreamFailure(status int, msg string, body []byte) attemptResult {
	return attemptResult{status: status, err: msg, client: msg, retry: retryable(status, string(body))}
}

// reasonOf is how a failed candidate is described in the call record and in
// the resting state.
func reasonOf(res attemptResult) string {
	return fmt.Sprintf("%d %s", res.status, res.err)
}
