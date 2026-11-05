package gateway

// Endpoints: which of a provider's API endpoints a model is served on. A
// provider's model list may say so (provider.APIs); where it doesn't, every
// protocol the provider has is a candidate, and an endpoint that answers that
// the model is not served there is remembered for that provider and model
// alone, so later requests skip it.

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// refusal is an upstream's 4xx reply as it came. The agent's copy is worded
// again on the way (provider.APIError keeps only the message), which can drop
// the words an endpoint refusal is told by, so the decision reads this.
type refusal struct {
	status int
	body   []byte
}

type refusalKey struct{}

// recordRefusal keeps an upstream 4xx reply for the endpoint decision, when
// the request was sent by attempt's loop over endpoints.
func recordRefusal(ctx context.Context, status int, body []byte) {
	if rf, ok := ctx.Value(refusalKey{}).(*refusal); ok {
		rf.status, rf.body = status, body
	}
}

// endpoints lists the protocols a request for model may be sent over to
// provider p, in the order to try them. The agent's own protocol comes first
// when the provider serves the model there (relayOK says it may be relayed
// as-is); the rest are translations, in the order preferred for the model.
// An endpoint remembered as not serving the model is left out, unless that
// leaves none, in which case the list is the same and the upstream decides.
func (s *Server) endpoints(p provider.Provider, model string, from provider.Protocol, relayOK bool) []provider.Protocol {
	declared := p.APIs(model)
	allowed := func(pr provider.Protocol) bool {
		if p.Base(pr) == "" {
			return false
		}
		// a model whose list names its APIs is sent to those only, never probed elsewhere
		return len(declared) == 0 || slices.Contains(declared, pr)
	}
	var out []provider.Protocol
	add := func(pr provider.Protocol) {
		if allowed(pr) && !slices.Contains(out, pr) {
			out = append(out, pr)
		}
	}
	if relayOK {
		add(from)
	}
	for _, pr := range preferred(p, model) {
		add(pr)
	}
	var fresh []provider.Protocol
	for _, pr := range out {
		if !s.unavailableOn(p.ID, model, pr) {
			fresh = append(fresh, pr)
		}
	}
	if len(fresh) == 0 {
		return out
	}
	return fresh
}

// preferred is the order translations take: OpenAI's own models want the
// Responses API first where OpenAI serves them (its own API, or Copilot's),
// other vendors' models Chat Completions first.
func preferred(p provider.Provider, model string) []provider.Protocol {
	if openAIFirst(p) && modelFamily(model) == provider.Chat {
		return []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	}
	return []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
}

// openAIFirst reports whether the provider is OpenAI's API or Copilot.
func openAIFirst(p provider.Provider) bool {
	if p.Account != nil && p.Account.Agent == "copilot" {
		return true
	}
	for _, pr := range provider.Protocols {
		if provider.HostOf(p.Base(pr)) == "api.openai.com" {
			return true
		}
	}
	return false
}

// unavailableKey names one provider's endpoint for one model.
func unavailableKey(providerID, model string, proto provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(proto)
}

// unavailableOn reports whether an endpoint has answered that the model is
// not served there.
func (s *Server) unavailableOn(providerID, model string, proto provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unavailable[unavailableKey(providerID, model, proto)]
}

// markUnavailable remembers that an endpoint does not serve the model.
func (s *Server) markUnavailable(providerID, model string, proto provider.Protocol) {
	s.mu.Lock()
	s.unavailable[unavailableKey(providerID, model, proto)] = true
	s.mu.Unlock()
}

// unreachableEndpoint matches the wording vendors use when a model exists but
// not on the endpoint asked, e.g. "not accessible via the /chat/completions endpoint".
var unreachableEndpoint = regexp.MustCompile(`not accessible via the .{1,40}? endpoint`)

// notChatModel reports whether an upstream error means the model is not
// served on the endpoint the request went to, so another endpoint may serve
// it. Only client errors qualify; a 5xx says nothing about the endpoint.
func notChatModel(status int, body []byte) bool {
	if status < http.StatusBadRequest || status >= http.StatusInternalServerError {
		return false
	}
	msg := strings.ToLower(string(body))
	for _, phrase := range endpointPhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return unreachableEndpoint.MatchString(msg)
}

// endpointPhrases are how OpenAI-compatible servers say a model is served on
// the other endpoint (lower case).
var endpointPhrases = []string{
	"not a chat model",
	"not supported in the v1/chat/completions",
	"not supported in /v1/chat/completions",
	"not supported in the v1/responses",
	"not supported in /v1/responses",
	"unsupported_api_for_model",
	"use v1/completions",
	"use /v1/completions",
	"use v1/responses",
	"use /v1/responses",
	"use v1/chat/completions",
	"use /v1/chat/completions",
}
