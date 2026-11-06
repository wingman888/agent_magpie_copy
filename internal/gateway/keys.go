package gateway

// Choosing among a provider's keys and endpoints.
//
// A request becomes a list of candidates: one per enabled key that can reach
// an endpoint, best first. The gateway tries them in order and moves on when
// one is refused for lack of quota, rate, or health (see retryable). Only
// the reply that actually goes out is written to the client.

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// candidate is one way to send a request: a provider set up with the key to
// use, the endpoint it goes to, and whether the request is relayed as it is
// or translated.
type candidate struct {
	p      provider.Provider
	key    string // the key's name; "" when it has none (or the provider has no keys)
	keyRef string // the key's fingerprint; "" when the provider has no keys
	proto  provider.Protocol
	relay  bool
	model  string // the model name the upstream is sent; a fallback's own name
}

// label names a candidate in Call.Fallback: the provider, and its key by name.
func (c candidate) label() string {
	if c.key == "" {
		return c.p.ID
	}
	return c.p.ID + " (" + c.key + ")"
}

// candidates lists how a request from client protocol from, for model, can be
// sent to p. sticky is the fingerprint of the key that last succeeded for p;
// among equally good candidates it goes first.
func candidates(p provider.Provider, from provider.Protocol, model string, canRelay bool, sticky string) []candidate {
	keys := p.EnabledKeys()
	if len(keys) == 0 {
		proto, relay, ok := plainEndpoint(p, from, canRelay)
		if !ok {
			return nil
		}
		return []candidate{{p: p, proto: proto, relay: relay, model: model}}
	}
	family := modelFamily(model)
	type ranked struct {
		c      candidate
		rank   int
		sticky bool
	}
	var out []ranked
	for _, k := range keys {
		var c candidate
		if k.Protocol != "" {
			// a key limited to a protocol only reaches that protocol's endpoint
			if p.Base(k.Protocol) == "" {
				continue
			}
			c = candidate{proto: k.Protocol, relay: k.Protocol == from}
		} else {
			proto, relay, ok := plainEndpoint(p, from, canRelay)
			if !ok {
				continue
			}
			c = candidate{proto: proto, relay: relay}
		}
		c.p = p.WithKey(k)
		c.key = k.Name
		c.keyRef = provider.KeyRef(k.Key)
		c.model = model
		out = append(out, ranked{c: c, rank: rankOf(family, from, c.proto), sticky: sticky != "" && c.keyRef == sticky})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].rank != out[j].rank {
			return out[i].rank < out[j].rank
		}
		return out[i].sticky && !out[j].sticky
	})
	var cands []candidate
	for _, r := range out {
		cands = append(cands, r.c)
	}
	return cands
}

// plainEndpoint is where a provider without key limits takes a request: its
// own endpoint for the client's protocol when there is one (and the request
// may be relayed), else the first protocol it speaks, by translation.
func plainEndpoint(p provider.Provider, from provider.Protocol, canRelay bool) (provider.Protocol, bool, bool) {
	if canRelay && p.Base(from) != "" {
		return from, true, true
	}
	speaks := p.Speaks()
	if len(speaks) == 0 {
		return "", false, false
	}
	return speaks[0], false, true
}

var oSeries = regexp.MustCompile(`^o[0-9]`)

// modelFamily sorts models by who they come from, for choosing keys: "claude"
// for Claude models, "openai" for GPT, o-series and codex models, else "".
func modelFamily(model string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "claude"):
		return "claude"
	case strings.Contains(m, "gpt"), strings.Contains(m, "codex"), oSeries.MatchString(m):
		return "openai"
	}
	return ""
}

// rankOf is 0 when an endpoint suits the model family, else 1. Claude models
// want Anthropic, OpenAI-family models want Chat Completions or Responses; any
// other model wants the client's own protocol, so nothing is translated.
func rankOf(family string, from, proto provider.Protocol) int {
	var want []provider.Protocol
	switch family {
	case "claude":
		want = []provider.Protocol{provider.Anthropic}
	case "openai":
		want = []provider.Protocol{provider.Chat, provider.Responses}
	default:
		want = []provider.Protocol{from}
	}
	for _, w := range want {
		if w == proto {
			return 0
		}
	}
	return 1
}

// failoverStatus says whether a refused reply should send the request on to
// the next candidate: rate limits, quota (402, or a 403 that says so), and
// server trouble. Other client errors are the caller's to fix and come back as
// they are.
func failoverStatus(status int, body []byte) bool {
	switch {
	case status == http.StatusTooManyRequests, status == http.StatusPaymentRequired, status >= 500:
		return true
	case status == http.StatusForbidden:
		return quotaText(body)
	}
	return false
}

// quotaText recognizes the wording gateways use for an exhausted quota.
func quotaText(body []byte) bool {
	msg := strings.ToLower(string(body))
	for _, w := range []string{"quota", "credit", "balance", "insufficient", "exhaust", "exceeded", "额度", "余额", "用完"} {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

// retryable reports whether an attempt should be abandoned for the next
// candidate. A refused reply is read whole and put back, so it can still be
// sent to the client if it is the last try.
func retryable(res *http.Response, err error) bool {
	if err != nil {
		return true
	}
	if res.StatusCode < 400 {
		return false
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(b))
	return failoverStatus(res.StatusCode, b)
}

// outcome is what trying a request's candidates came to.
type outcome struct {
	status int
	msg    string
	used   candidate // the candidate whose reply went out
	failed []string  // labels of the candidates tried and refused before it
}

// try sends the request to each candidate in turn until one answers. Only the
// last candidate's reply, or the first that is not refused, is written. A
// refused candidate rests (see fallback.go) so later requests try others first.
func (s *Server) try(w http.ResponseWriter, r *http.Request, from provider.Protocol, model string, body []byte, cands []candidate, u *Usage) outcome {
	var out outcome
	var request *Request
	var perr error
	var stream, parsed bool
	for i, c := range cands {
		last := i == len(cands)-1
		m := c.model
		if m == "" {
			m = model
		}
		var res *http.Response
		var err error
		actual := c.proto
		if c.relay {
			res, err = s.fetchRelay(r, c.p, c.proto, m, body)
		} else {
			if !parsed {
				parsed = true
				request, perr = parse(from, body)
				if perr == nil {
					stream = request.Stream
					request.Stream = true
				}
			}
			if perr != nil {
				out.status, out.msg, out.used = writeError(w, from, 400, perr.Error()), perr.Error(), c
				return out
			}
			res, actual, err = s.forwardTranslated(r.Context(), c.p, c.proto, request, m, r.Header)
		}
		refused := r.Context().Err() == nil && retryable(res, err)
		s.markRefused(c, refused)
		if refused && !last {
			out.failed = append(out.failed, c.label())
			if res != nil {
				res.Body.Close()
			}
			continue
		}
		if c.relay {
			out.status, out.msg = relayReply(w, res, err, c.p, c.proto, u)
		} else {
			out.status, out.msg = translateReply(w, res, err, c.p, from, actual, request, stream, u)
		}
		out.used = c
		if out.status < 400 {
			s.remember(c.p.ID, c.keyRef)
		}
		return out
	}
	return out
}

// remember notes the key that last answered for a provider; the next request
// starts with it.
func (s *Server) remember(providerID, keyRef string) {
	if keyRef == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastKey == nil {
		s.lastKey = map[string]string{}
	}
	s.lastKey[providerID] = keyRef
}

func (s *Server) lastKeyRef(providerID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastKey[providerID]
}
