package gateway

import (
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// serveAccounts sends a request through the accounts of one agent in turn:
// the account the agent is signed in as first, then the remembered ones that
// are switched on, in the order they were remembered. An account that is out
// of allowance, rate limited or down hands the request to the next one, as
// long as no reply has started; any other error goes back to the client as
// it is. An account that just failed is tried last for keyCooldown, as keys
// are. It returns the endpoint and status of the last try, and a description
// of the accounts that failed on the way ("" when none did).
func (s *Server) serveAccounts(w http.ResponseWriter, r *http.Request, p provider.Provider, from provider.Protocol, model string, body []byte, u *Usage) (provider.Protocol, int, string, string) {
	cands := append([]provider.Provider{p}, p.Account.Alternates()...)
	refs := make([]string, len(cands))
	for i, c := range cands {
		refs[i] = accountRef(c)
	}
	order := s.coolOrder(p.ID, refs)
	var failed []string
	var to provider.Protocol
	var status int
	var msg string
	for n, i := range order {
		c := cands[i]
		last := n == len(order)-1
		hw := newHoldWriter(w, !last)
		// as for the account's own endpoint: a stream-only backend gets a
		// non-streaming request translated, not relayed
		relay := c.Base(from) != "" && (c.Account == nil || !c.Account.Stream || streamOf(body))
		if relay {
			to = from
			status, msg = s.passthrough(hw, r, c, from, model, body, u)
		} else if speaks := c.Speaks(); len(speaks) > 0 {
			to = speaks[0]
			status, msg = s.translate(hw, r, c, from, to, model, body, u)
		} else {
			to = from
			msg = c.Name + " has no endpoint configured"
			status = writeError(hw, from, 502, msg)
		}
		if hw.isHeld() && keyRetryable(status, hw.body()) {
			s.cool(p.ID, refs[i])
			failed = append(failed, accountFailedDesc(p.ID, c))
			continue
		}
		hw.release()
		break
	}
	return to, status, msg, strings.Join(failed, "; ")
}

// accountRef is the cooldown key of an account of a subscription provider.
func accountRef(p provider.Provider) string { return "account:" + p.Account.User }

// accountFailedDesc names a failed account for the call record.
func accountFailedDesc(providerID string, p provider.Provider) string {
	return providerID + " (account " + p.Account.User + ")"
}

// coolOrder lists the candidates in the order to try them: the ones that did
// not fail within keyCooldown keep their order, and those that did go last.
func (s *Server) coolOrder(providerID string, refs []string) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ready, cooled []int
	for i, ref := range refs {
		if t, ok := s.keyCool[providerID+"\x00"+ref]; ok && time.Since(t) < keyCooldown {
			cooled = append(cooled, i)
		} else {
			ready = append(ready, i)
		}
	}
	return append(ready, cooled...)
}
