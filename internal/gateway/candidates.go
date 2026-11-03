package gateway

import (
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// restFor is how long a candidate that just failed is passed over in favor of
// the others.
const restFor = time.Minute

// candidates lists what a request to a provider may be sent with, in order:
// its enabled keys, the ones that have not just failed first. A key that
// failed recently is still tried after the rest, so a lone key is never
// skipped.
func (s *Server) candidates(p provider.Provider) []provider.Provider {
	cands := p.KeyCandidates()
	if len(cands) < 2 {
		return cands
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var ready, resting []provider.Provider
	for _, c := range cands {
		if until, ok := s.resting[restKey(c)]; ok && now.Before(until) {
			resting = append(resting, c)
			continue
		}
		ready = append(ready, c)
	}
	return append(ready, resting...)
}

// rest marks a candidate as having just failed.
func (s *Server) rest(c provider.Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resting == nil {
		s.resting = map[string]time.Time{}
	}
	s.resting[restKey(c)] = time.Now().Add(restFor)
}

// restKey names a candidate: its provider and the key it is sent with.
func restKey(c provider.Provider) string {
	return c.ID + "\x00" + provider.KeyRef(c.Key)
}

// retryable says whether a failed reply should send the request on to the
// next candidate: the quota is used up or the call was rate-limited, or the
// service is overloaded or down. Anything else, such as a bad request, is the
// client's to see.
func retryable(status int, body []byte) bool {
	switch status {
	case 402, 429, 502, 503, 504, 529:
		return true
	case 403:
		// some gateways answer an exhausted quota with 403 and a message
		msg := strings.ToLower(string(body))
		for _, w := range []string{"quota", "insufficient", "balance", "credit", "exhaust", "limit", "余额", "额度", "用完", "用尽"} {
			if strings.Contains(msg, w) {
				return true
			}
		}
	}
	return false
}
