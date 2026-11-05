package gateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Failover. A request is sent one way at a time: each candidate is a provider
// carrying one of its keys, and the protocol the request goes out on. When a
// candidate is spent (quota gone, rate limited, overloaded, down), the next
// one is tried, as long as nothing of the reply has reached the client yet.
// A candidate that just failed is tried last for a while.

// cooldown is how long a failed candidate waits before it is tried first again.
const cooldown = time.Minute

// candidate is one way of sending a request.
type candidate struct {
	p     provider.Provider // carries the key in use
	to    provider.Protocol // the protocol the request goes out on
	relay bool              // sent as the client sent it, not translated
}

// key names the candidate for the cooldown table.
func (c candidate) key() string {
	if c.p.Account != nil {
		return c.p.ID + "\x00account\x00" + strings.ToLower(c.p.Account.User)
	}
	if c.p.Key == "" {
		return c.p.ID
	}
	return c.p.ID + "\x00" + provider.KeyRef(c.p.Key)
}

// label names the candidate in a call's Fallback: its provider, and its key
// when the key has a name.
func (c candidate) label() string {
	if c.p.Account != nil {
		return fmt.Sprintf("%s (account %s)", c.p.ID, c.p.Account.User)
	}
	if c.p.KeyName == "" {
		return c.p.ID
	}
	return fmt.Sprintf("%s (key %s)", c.p.ID, c.p.KeyName)
}

// accountOK reports whether a request may be relayed as it is: a backend
// that only streams takes a streaming request only.
func accountOK(p provider.Provider, body []byte) bool {
	return p.Account == nil || !p.Account.Stream || streamOf(body)
}

// plan lists the candidates for a request, in the order to try them. Those
// that failed within the cooldown go to the back.
func (s *Server) plan(p provider.Provider, from provider.Protocol, model string, body []byte) []candidate {
	var out []candidate
	for _, c := range p.Candidates(model, from) {
		switch {
		case c.KeyProtocol != "":
			out = append(out, candidate{c, c.KeyProtocol, c.KeyProtocol == from && accountOK(p, body)})
		case p.Base(from) != "" && accountOK(p, body):
			out = append(out, candidate{c, from, true})
		default:
			// a backend that only streams gets a non-streaming request translated
			// (the provider is always streamed on that path) rather than relayed
			speaks := c.Speaks()
			if len(speaks) == 0 {
				continue
			}
			out = append(out, candidate{c, speaks[0], false})
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, until := range s.cooling {
		if !until.After(now) {
			delete(s.cooling, k) // expired; keep the table from growing
		}
	}
	var fresh, cooling []candidate
	for _, c := range out {
		if s.cooling[c.key()].After(now) {
			cooling = append(cooling, c)
		} else {
			fresh = append(fresh, c)
		}
	}
	return append(fresh, cooling...)
}

// cool marks a candidate as failed for the cooldown.
func (s *Server) cool(c candidate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cooling == nil {
		s.cooling = map[string]time.Time{}
	}
	s.cooling[c.key()] = time.Now().Add(cooldown)
}

// failover reports whether an upstream error means this way of sending is
// spent (quota, rate limit, overload, outage), so the next one should be
// tried. A 400 is a mistake in the request itself and is returned as it is.
// Some gateways answer an exhausted quota with 403; their wording decides.
func failover(status int, body []byte) bool {
	switch status {
	case 402, 429, 502, 503, 504, 529:
		return true
	case 403:
		return quotaText(string(body))
	}
	return false
}

// spentWords are the explicit quota wordings, in English and Chinese. They are
// deliberately specific: a bare "limit" or "exceeded" is not a quota error.
var spentWords = []string{
	"quota",
	"insufficient balance", "insufficient_balance", "insufficient credit",
	"余额不足",
	"额度已用完", "额度用完", "额度已用尽", "额度用尽",
}

// quotaText reports whether a reply's wording says the quota is used up.
func quotaText(msg string) bool {
	msg = strings.ToLower(msg)
	for _, w := range spentWords {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

// failoverText classifies an error that arrives inside a 200 event stream,
// where there is no status code: the wording alone decides.
func failoverText(msg string) bool {
	if quotaText(msg) {
		return true
	}
	msg = strings.ToLower(msg)
	for _, w := range []string{"overloaded", "rate limit", "rate_limit", "too many requests"} {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}
