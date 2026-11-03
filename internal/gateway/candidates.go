package gateway

import (
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// restFor is how long a candidate that just failed is passed over in favor of
// the others.
const restFor = time.Minute

// attempt is one way to send a request: with one key, either relayed in the
// client's own protocol or translated to another one.
type attempt struct {
	p     provider.Provider
	relay bool              // send as the client speaks
	to    provider.Protocol // when not relaying: the protocol sent upstream
}

// attempts lists the ways a request may be sent to a provider, in the order to
// try them. Keys are the provider's enabled ones; a key with no endpoint for
// this request is left out. Among those, the ones the model prefers come first
// (see rank). A key that just failed rests and is tried after the others, so a
// lone key is never skipped.
func (s *Server) attempts(p provider.Provider, from provider.Protocol, model string, body []byte) []attempt {
	fam := familyOf(model)
	type ranked struct {
		a    attempt
		rank int
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var ready, resting []ranked
	for _, c := range p.KeyCandidates() {
		a := attempt{p: c, relay: c.Base(from) != "" && (c.Account == nil || !c.Account.Stream || streamOf(body))}
		route := from
		if !a.relay {
			to := c.Speaks()
			if len(to) == 0 {
				continue
			}
			a.to, route = to[0], to[0]
		}
		r := ranked{a: a, rank: rank(fam, from, route, c.KeyProtocol != "")}
		if until, ok := s.resting[restKey(c)]; ok && now.Before(until) {
			resting = append(resting, r)
			continue
		}
		ready = append(ready, r)
	}
	sort.SliceStable(ready, func(i, j int) bool { return ready[i].rank < ready[j].rank })
	sort.SliceStable(resting, func(i, j int) bool { return resting[i].rank < resting[j].rank })
	var out []attempt
	for _, r := range append(ready, resting...) {
		out = append(out, r.a)
	}
	return out
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

// family groups models by the wire they are native to.
type family int

const (
	famOther  family = iota
	famClaude        // Anthropic's models: Anthropic keys first
	famOpenAI        // GPT, o-series and codex models: OpenAI keys first
)

// familyOf tells a model's family from its name. A "provider/" prefix is
// ignored.
func familyOf(model string) family {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "claude"):
		return famClaude
	case strings.HasPrefix(m, "gpt"), strings.Contains(m, "codex"):
		return famOpenAI
	case len(m) >= 2 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9': // o1, o3-mini, o4 …
		return famOpenAI
	}
	return famOther
}

// rank orders attempts for a model, lower first. An attempt whose route is the
// one the model prefers ranks 0: Anthropic for Claude, Chat or Responses for
// GPT/o/codex, and the client's own protocol (no translation) for the rest. A
// key limited to another protocol is a last resort (2); an unlimited key that
// has to translate sits between (1).
func rank(f family, from, route provider.Protocol, limited bool) int {
	var preferred bool
	switch f {
	case famClaude:
		preferred = route == provider.Anthropic
	case famOpenAI:
		preferred = route == provider.Chat || route == provider.Responses
	default:
		preferred = route == from
	}
	switch {
	case preferred:
		return 0
	case limited:
		return 2
	}
	return 1
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
