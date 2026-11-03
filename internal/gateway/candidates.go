package gateway

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
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
	model string            // the model's name at this provider, sent upstream
	relay bool              // send as the client speaks
	to    provider.Protocol // when not relaying: the protocol sent upstream
	rest  bool              // failed lately: tried after the others
}

// chain lists every way a request may be sent, in the order to try them: the
// primary provider's candidates first, then those of each fallback entry in
// turn. Candidates that are not resting go before the resting ones, so a
// failed one is only tried again when nothing else is left. A model already
// listed for a provider is not repeated.
func (s *Server) chain(p provider.Provider, from provider.Protocol, model string, body []byte) []attempt {
	var all []attempt
	seen := map[string]bool{}
	add := func(pv provider.Provider, m string) {
		k := pv.ID + "\x00" + m
		if seen[k] {
			return
		}
		seen[k] = true
		all = append(all, s.attempts(pv, from, m, body)...)
	}
	add(p, model)
	for _, ref := range p.Fallback {
		if fp, fm, ok := provider.ResolveFallback(ref); ok {
			add(fp, fm)
		}
	}
	var ready, resting []attempt
	for _, a := range all {
		if a.rest {
			resting = append(resting, a)
		} else {
			ready = append(ready, a)
		}
	}
	return append(ready, resting...)
}

// failedName names a candidate that failed, for the call record: its provider,
// and its key's name when the key has one.
func failedName(c provider.Provider) string {
	if c.Account != nil {
		return c.ID + " (" + c.Account.User + ")"
	}
	if c.KeyName != "" {
		return c.ID + " (" + c.KeyName + ")"
	}
	return c.ID
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
		a := attempt{p: c, model: model, relay: c.Base(from) != "" && (c.Account == nil || !c.Account.Stream || streamOf(body))}
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
			r.a.rest = true
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

// restKey names a candidate: its provider, the key it is sent with, and for a
// signed-in account, which account it is (they share a provider and no key).
func restKey(c provider.Provider) string {
	k := c.ID + "\x00" + provider.KeyRef(c.Key)
	if c.Account != nil {
		k += "\x00" + c.Account.User
	}
	return k
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

// streamFailure says whether an error payload, arriving inside a stream before
// any of its content, is one another candidate may fix: the quota is used up,
// the call was rate-limited, or the service is overloaded or down. payload is
// the error's JSON data, or a plain message. Bad requests are never retried.
func streamFailure(payload string) bool {
	types, status := errorFields(payload)
	if status == 400 || containsString(types, "invalid_request_error") {
		return false
	}
	if retryableStatus(status) {
		return true
	}
	for _, t := range types {
		if quotaType(t) {
			return true
		}
	}
	return retryText(payload)
}

// retryable says whether a failed reply should send the request on to the
// next candidate: the quota is used up or the call was rate-limited, or the
// service is overloaded or down. Anything else, such as a bad request, is the
// client's to see.
func retryable(status int, body []byte) bool {
	if retryableStatus(status) {
		return true
	}
	if status != 403 {
		return false
	}
	// some gateways answer an exhausted quota with 403 and a message
	types, _ := errorFields(string(body))
	for _, t := range types {
		if quotaType(t) {
			return true
		}
	}
	msg := strings.ToLower(string(body))
	for _, w := range []string{"quota", "insufficient", "balance", "credit", "exhaust", "rate limit", "余额", "额度", "用完", "用尽"} {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

// retryableStatus is the HTTP status codes that mean "try another candidate":
// payment required or quota, rate limit, and the service being down or busy.
func retryableStatus(status int) bool {
	switch status {
	case 402, 429, 502, 503, 504, 529:
		return true
	}
	return false
}

// quotaType recognizes the machine-readable error types and codes that say the
// quota is used up, the call was rate-limited, or the service is overloaded.
func quotaType(t string) bool {
	switch t {
	case "rate_limit_error", "rate_limit_exceeded", "overloaded_error", "overloaded", "server_overloaded",
		"insufficient_quota", "insufficient_balance", "resource_exhausted", "unavailable", "service_unavailable",
		"usage_limit_reached", "too_many_requests":
		return true
	}
	return strings.Contains(t, "quota") || strings.Contains(t, "rate_limit") || strings.Contains(t, "overload")
}

// errorFields reads the error type, code and numeric status out of an error
// payload's JSON, both from the top level and from a nested "error" object. A
// payload that is not JSON gives nothing.
func errorFields(payload string) (types []string, status int) {
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(payload)))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil, 0
	}
	m, _ := v.(map[string]any)
	objs := []map[string]any{m}
	if e, ok := m["error"].(map[string]any); ok {
		objs = append(objs, e)
	}
	for _, o := range objs {
		for _, k := range []string{"type", "code", "status"} {
			switch x := o[k].(type) {
			case string:
				x = strings.ToLower(strings.TrimSpace(x))
				if n, err := strconv.Atoi(x); err == nil {
					if status == 0 {
						status = n
					}
				} else if x != "" {
					types = append(types, x)
				}
			case json.Number:
				if n, err := x.Int64(); err == nil && status == 0 {
					status = int(n)
				}
			}
		}
	}
	return types, status
}

// retryText is the fallback for a message that carries no machine-readable
// type: a standalone status code, or a phrase that plainly names the condition.
// Bare words such as "unavailable" or digits inside "4290" do not count.
func retryText(msg string) bool {
	if codeInText.MatchString(msg) {
		return true
	}
	msg = strings.ToLower(msg)
	for _, p := range []string{"rate limit", "rate_limit", "usage limit", "quota exceeded", "exceeded your quota",
		"exceeded your current quota", "insufficient_quota", "insufficient quota", "insufficient balance",
		"overloaded", "service unavailable", "temporarily unavailable", "resource_exhausted",
		"额度不足", "额度已用完", "额度已用尽", "余额不足", "限流", "过载"} {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// codeInText matches a retryable status code standing alone, not as part of a
// longer number.
var codeInText = regexp.MustCompile(`(^|[^0-9])(402|429|502|503|504|529)([^0-9]|$)`)

func containsString(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}
