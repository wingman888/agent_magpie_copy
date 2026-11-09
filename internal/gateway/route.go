package gateway

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// cooldown is how long a credential that just failed waits before magpie
// tries it first again. Other candidates go ahead of it meanwhile.
const cooldown = time.Minute

// attempt is one try at a provider: the credential it sends, the protocol
// the request is spoken in upstream, and whether the client's own protocol
// goes through untranslated.
type attempt struct {
	p       provider.Provider // the provider, carrying the credential
	model   string            // the vendor's own model id this try sends
	cred    provider.Credential
	to      provider.Protocol
	relay   bool
	rank    int  // lower is tried first; see rankOf
	cooling bool // failed within the cooldown; tried last
}

// label names the candidate in a call's Fallback: the provider id, plus the
// key's name when the key has one.
func (a attempt) label() string {
	if a.cred.Name != "" {
		return a.p.ID + " (" + a.cred.Name + ")"
	}
	return a.p.ID
}

var oSeries = regexp.MustCompile(`^o\d`)

// modelFamily sorts models by the protocol they are usually sold under:
// "claude", "openai", or "" for everything else.
func modelFamily(model string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "claude"):
		return "claude"
	case strings.HasPrefix(m, "gpt"), strings.Contains(m, "codex"), oSeries.MatchString(m):
		return "openai"
	}
	return ""
}

// rankOf orders credentials for a model. A Claude model wants an Anthropic
// key first, a GPT or o-series model an OpenAI key. Any other model wants a
// key that speaks the client's protocol directly, so nothing is translated.
// Keys that fit badly still come last, as a last resort.
func rankOf(family string, a attempt) int {
	k := a.cred.Protocol
	switch family {
	case "claude":
		switch k {
		case provider.Anthropic:
			return 0
		case "":
			return 1
		}
		return 2
	case "openai":
		switch k {
		case provider.Chat, provider.Responses:
			return 0
		case "":
			return 1
		}
		return 2
	}
	if a.relay {
		return 0
	}
	return 1
}

// attempts lists every try the request can make against the provider, in
// the order to make them. A credential limited to one protocol only goes to
// that protocol's endpoint, and is left out when the provider has none.
func (s *Server) attempts(p provider.Provider, from provider.Protocol, model string, body []byte) []attempt {
	family := modelFamily(model)
	var out []attempt
	for _, c := range p.Credentials() {
		a := attempt{p: p.WithCredential(c), model: model, cred: c}
		switch {
		case c.Protocol != "":
			if !c.Serves(p, c.Protocol) {
				continue
			}
			a.to, a.relay = c.Protocol, c.Protocol == from
		case p.Base(from) != "" && (p.Account == nil || !p.Account.Stream || streamOf(body)):
			a.to, a.relay = from, true
		default:
			sp := p.Speaks()
			if len(sp) == 0 {
				continue
			}
			a.to = sp[0]
		}
		a.rank = rankOf(family, a)
		a.cooling = s.isCooling(a)
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].cooling != out[j].cooling {
			return !out[i].cooling
		}
		return out[i].rank < out[j].rank
	})
	return out
}

// chain lists every try a request makes, in order: the provider's own enabled
// keys, then each fallback entry's keys in list order. Entries that do not
// resolve are skipped silently, as is a Claude subscription, which only runs
// through the Claude Code binary. Candidates that failed recently go to the
// back, so a provider that just failed is passed over until its rest is up.
func (s *Server) chain(p provider.Provider, from provider.Protocol, model string, body []byte) []attempt {
	out := s.attempts(p, from, model, body)
	seen := map[string]bool{p.ID + "\x00" + model: true}
	for _, entry := range p.Fallback {
		fp, fm, ok := provider.Resolve(entry)
		if !ok || seen[fp.ID+"\x00"+fm] {
			continue
		}
		seen[fp.ID+"\x00"+fm] = true
		if fp.Account != nil && fp.Account.Agent == "claude" {
			continue
		}
		out = append(out, s.attempts(fp, from, fm, body)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return !out[i].cooling && out[j].cooling
	})
	return out
}

func coolKey(a attempt) string { return a.p.ID + "\x00" + a.cred.Ref }

func (s *Server) isCooling(a attempt) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Before(s.cooled[coolKey(a)])
}

// markFailed sends a credential to the back of the line for a while.
func (s *Server) markFailed(a attempt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cooled == nil {
		s.cooled = make(map[string]time.Time)
	}
	s.cooled[coolKey(a)] = time.Now().Add(cooldown)
}

// failover reports whether a failed reply should move on to the next
// candidate: out of quota, rate limited, overloaded or unavailable. Such
// replies come before anything reaches the client, so the switch is
// invisible to it. Other errors, such as a bad request, are returned as is.
func failover(status int, body []byte) bool {
	switch status {
	case 402, 429, 502, 503, 504, 529:
		return true
	case 403:
		return quotaText(body, "exceeded")
	}
	return false
}

// quotaText reports whether an error body names a quota, balance or rate
// limit, the words gateways use for a 403 that means "out of quota". extra
// adds words of its own.
func quotaText(body []byte, extra ...string) bool {
	msg := strings.ToLower(string(body))
	words := append([]string{"quota", "insufficient", "balance", "credit", "exhausted", "额度", "余额", "用完"}, extra...)
	for _, w := range words {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

// failoverText reports whether an error that arrived inside a 200 event
// stream, before any output, should move on to the next candidate.
func failoverText(msg string) bool {
	m := strings.ToLower(msg)
	for _, w := range []string{"rate limit", "rate_limit", "overload", "unavailable", "429", "529", "限流", "过载"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return quotaText([]byte(msg))
}
