package gateway

import (
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyRest is how long a key that ran out is passed over before it is tried again.
const keyRest = time.Minute

// spent reports whether a reply says this key has run out or is rate-limited,
// so another key of the same provider may still serve the request.
func spent(status int, body []byte) bool {
	switch status {
	case 429, 402:
		return true
	case 403:
		msg := strings.ToLower(string(body))
		for _, w := range []string{"quota", "insufficient", "balance", "credit", "exhausted", "exceeded", "limit", "额度", "余额"} {
			if strings.Contains(msg, w) {
				return true
			}
		}
	}
	return false
}

// routes lists the ways a request can be served, in the order to try them.
// A signed-in account has no keys: it is relayed when it can be, else
// translated to the first protocol it speaks.
func (s *Server) routes(p provider.Provider, from provider.Protocol, model string, body []byte) []provider.Candidate {
	if p.Account != nil {
		relay := p.Base(from) != "" && (!p.Account.Stream || streamOf(body))
		to := from
		if !relay {
			to = ""
			if sp := p.Speaks(); len(sp) > 0 {
				to = sp[0]
			}
		}
		if to == "" {
			return nil
		}
		return []provider.Candidate{{Provider: p, To: to}}
	}
	return s.inOrder(p.Candidates(from, model))
}

// attempt sends a request down one candidate. When canRetry is set and the
// failure is one another key can get past, nothing has been written to w and
// retry is true.
func (s *Server) attempt(w http.ResponseWriter, r *http.Request, c provider.Candidate, from provider.Protocol, model string, body []byte, u *Usage, canRetry bool) (int, string, bool) {
	if c.To == from {
		return s.passthrough(w, r, c.Provider, from, model, body, u, canRetry)
	}
	return s.translate(w, r, c.Provider, from, c.To, model, body, u, canRetry)
}

// restKey marks a key as spent for keyRest.
func (s *Server) restKey(c provider.Candidate) {
	if c.Ref == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resting == nil {
		s.resting = map[string]time.Time{}
	}
	s.resting[c.ID+"\x00"+c.Ref] = time.Now().Add(keyRest)
}

// inOrder moves keys that recently ran out behind the others, keeping the
// order within each group.
func (s *Server) inOrder(cs []provider.Candidate) []provider.Candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var ready, resting []provider.Candidate
	for _, c := range cs {
		if t, ok := s.resting[c.ID+"\x00"+c.Ref]; ok && now.Before(t) {
			resting = append(resting, c)
		} else {
			ready = append(ready, c)
		}
	}
	return append(ready, resting...)
}

// failedName names a candidate in a call's Fallback: its provider, and its
// key's name when the key has one.
func failedName(c provider.Candidate) string {
	if c.KeyName != "" {
		return c.ID + " (key " + c.KeyName + ")"
	}
	return c.ID
}

// anthropicRoutes lists the candidates that can answer an Anthropic
// count_tokens request: those that reach the Anthropic endpoint. Keys limited
// to another protocol are not used.
func anthropicRoutes(p provider.Provider, model string) []provider.Candidate {
	var out []provider.Candidate
	for _, c := range p.Candidates(provider.Anthropic, model) {
		if c.To == provider.Anthropic {
			out = append(out, c)
		}
	}
	return out
}
