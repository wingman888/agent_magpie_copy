package gateway

// Fallback chains and cooldowns.
//
// A provider may list fallbacks: "provider/model" entries tried in order once
// its own candidates have all been refused. They join the same candidate list,
// so the refusal rules (retryable) and the rule that only the reply going out
// is written apply to them unchanged. A candidate that was refused rests for
// cooldownFor: later requests try the others first, and it comes back only
// when nothing else is left.

import (
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// cooldownFor is how long a refused candidate rests.
const cooldownFor = time.Minute

// fallbackCandidates lists the candidates of p's fallback entries, in the
// order given. Entries that do not resolve are skipped without a word. A
// signed-in Claude Code is left out: its requests run through the Claude Code
// binary (serveClaudeSubscription), not through this path.
func (s *Server) fallbackCandidates(p provider.Provider, from provider.Protocol, body []byte) []candidate {
	var out []candidate
	for _, entry := range p.Fallback {
		fp, model, ok := provider.ResolveFallback(entry)
		if !ok || (fp.Account != nil && fp.Account.Agent == "claude") {
			continue
		}
		canRelay := fp.Account == nil || !fp.Account.Stream || streamOf(body)
		out = append(out, candidates(fp, from, model, canRelay, s.lastKeyRef(fp.ID))...)
	}
	return out
}

// cooldownKey names a candidate for cooldowns: its provider and its key. A
// provider without keys is named by its id alone.
func (c candidate) cooldownKey() string {
	return c.p.ID + "\x00" + c.keyRef
}

func (s *Server) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

// markRefused starts a candidate's rest when it was refused, and ends it when
// it answered.
func (s *Server) markRefused(c candidate, refused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refusedAt == nil {
		s.refusedAt = map[string]time.Time{}
	}
	k := c.cooldownKey()
	if refused {
		s.refusedAt[k] = s.now()
	} else {
		delete(s.refusedAt, k)
	}
}

// restedLast moves the candidates still resting to the end of the list, in
// their own order. They are still tried when nothing else answers.
func (s *Server) restedLast(cands []candidate) []candidate {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var ready, resting []candidate
	for _, c := range cands {
		if t, ok := s.refusedAt[c.cooldownKey()]; ok && now.Sub(t) < cooldownFor {
			resting = append(resting, c)
			continue
		}
		ready = append(ready, c)
	}
	return append(ready, resting...)
}
