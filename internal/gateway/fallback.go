package gateway

// Fallback: when a provider's candidates fail for a reason the client can be
// spared (see retryable), the request moves on to the provider's fallback
// targets, in the order the user listed them.

import (
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// candidates lists every way to answer a request for model, in the order they
// are tried. The provider's own keys and logins come first (keyOrder decides
// among them), then each fallback target's, each asked for the target's own
// model. A block with a candidate not resting after a failure is tried before a
// block whose candidates are all resting, so a provider that just failed is
// skipped for about keyCooldown while others answer.
func (s *Server) candidates(p provider.Provider, from provider.Protocol, model string) []attemptKey {
	blocks := [][]attemptKey{s.blockOf(p, from, model)}
	for _, t := range p.FallbackTargets() {
		if t.Provider.ID == p.ID && t.Model == model {
			continue // the same model again is no fallback
		}
		blocks = append(blocks, s.blockOf(t.Provider, from, t.Model))
	}
	var live, resting []attemptKey
	for _, b := range blocks {
		if len(b) == 0 {
			continue
		}
		if s.allResting(b) {
			resting = append(resting, b...)
		} else {
			live = append(live, b...)
		}
	}
	return append(live, resting...)
}

// blockOf is the keyOrder of one provider, each candidate tagged with the
// model it is asked for.
func (s *Server) blockOf(p provider.Provider, from provider.Protocol, model string) []attemptKey {
	out := s.keyOrder(p, from, model)
	for i := range out {
		out[i].model = model
	}
	return out
}

// allResting reports whether every candidate in a block failed within keyCooldown.
func (s *Server) allResting(b []attemptKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, a := range b {
		if t, ok := s.cool[a.id()]; !ok || !now.Before(t) {
			return false
		}
	}
	return true
}
