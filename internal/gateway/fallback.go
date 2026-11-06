package gateway

import (
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// providerCooldown is how long a provider that just failed as a whole is
// passed over in favour of the other candidates.
const providerCooldown = time.Minute

// candidate is one provider and the model it is asked for: the provider a
// request names, or one of its fallbacks.
type candidate struct {
	p     provider.Provider
	model string
}

// candidates lists what may answer a request for p's model: p itself, then
// the entries of its fallback list in order. A provider that failed within
// the cooldown goes last; the rest keep their order. Entries that do not
// parse, name no ready provider, or repeat an earlier candidate are skipped.
func (s *Server) candidates(p provider.Provider, model string) []candidate {
	out := []candidate{{p, model}}
	for _, entry := range p.Fallback {
		fp, fm, ok := parseFallback(entry)
		if !ok || hasCandidate(out, fp.ID, fm) {
			continue
		}
		out = append(out, candidate{fp, fm})
	}
	sort.SliceStable(out, func(a, b int) bool {
		return !s.providerCooling(out[a].p.ID) && s.providerCooling(out[b].p.ID)
	})
	return out
}

func hasCandidate(cs []candidate, id, model string) bool {
	for _, c := range cs {
		if c.p.ID == id && c.model == model {
			return true
		}
	}
	return false
}

// parseFallback reads a "provider/model" entry. The provider is the part
// before the first slash, as Resolve reads an id. ok is false when either part
// is missing or no ready provider has that id.
func parseFallback(entry string) (provider.Provider, string, bool) {
	pid, model, found := strings.Cut(strings.TrimSpace(entry), "/")
	pid, model = strings.TrimSpace(pid), strings.TrimSpace(model)
	if !found || pid == "" || model == "" {
		return provider.Provider{}, "", false
	}
	p, err := provider.Find(pid)
	if err != nil || !p.Ready() {
		return provider.Provider{}, "", false
	}
	return *p, model, true
}

// providerCoolKey names a provider's cooldown in s.cool. Key cooldowns are
// stored under the provider id, a NUL and the key's ref; this prefix cannot
// collide with them, since a provider id never starts with \x01.
func providerCoolKey(id string) string { return "\x01" + id }

// providerCooling reports whether a provider failed within the cooldown.
func (s *Server) providerCooling(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Before(s.cool[providerCoolKey(id)])
}

// markProviderCooling records that a provider failed as a whole.
func (s *Server) markProviderCooling(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cool == nil {
		s.cool = make(map[string]time.Time)
	}
	s.cool[providerCoolKey(id)] = time.Now().Add(providerCooldown)
}
