package gateway

// Endpoints: which of a provider's APIs a model's requests go to. A model
// list that says which APIs a model is served on is trusted, and only those
// are asked. Otherwise every API the provider has is a candidate, and one
// that answers that it does not serve the model is remembered for that model
// (only), so the request goes to the next API before any of the refused
// reply reaches the agent, and later requests skip the refused one.

import (
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// endpoint is one API a request is sent to: relay sends it as the agent sent
// it, otherwise the request is translated to it.
type endpoint struct {
	proto provider.Protocol
	relay bool
}

// notOnEndpoint matches a refusal naming the endpoint it came from, as in
// "model x is not accessible via the /chat/completions endpoint".
var notOnEndpoint = regexp.MustCompile(`not accessible via the\s+\S+\s+endpoint`)

// servedAPIs are the APIs of p a model may be asked on: those the provider
// has a base URL for, narrowed to the ones its model list names for the model
// when the list names any.
func servedAPIs(p provider.Provider, model string) []provider.Protocol {
	declared := p.APIs(model)
	var out []provider.Protocol
	for _, pr := range provider.Protocols {
		if p.Base(pr) == "" || len(declared) > 0 && !slices.Contains(declared, pr) {
			continue
		}
		out = append(out, pr)
	}
	return out
}

// translationOrder is the order APIs are tried in when a request has to be
// translated: Responses first, then Chat, for OpenAI's own API and for OpenAI
// models on Copilot; Chat first for every other vendor.
func translationOrder(p provider.Provider, model string) []provider.Protocol {
	h := p.Host()
	if h == "api.openai.com" || (strings.HasSuffix(h, "githubcopilot.com") && modelFamily(model) == provider.Chat) {
		return []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	}
	return []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
}

// endpoints are the APIs a request for model goes to, in the order to try
// them. The agent's own API comes first when relay allows it and the provider
// serves the model on it, so the request is forwarded as it is; the rest
// follow in translationOrder. APIs remembered as refusing the model are left
// out, unless that leaves none, in which case they are tried after all.
func (s *Server) endpoints(p provider.Provider, model string, from provider.Protocol, relay bool) []endpoint {
	avail := servedAPIs(p, model)
	var out []endpoint
	if relay && slices.Contains(avail, from) {
		out = append(out, endpoint{from, true})
	}
	relayed := len(out) > 0
	for _, pr := range translationOrder(p, model) {
		if slices.Contains(avail, pr) && !(relayed && pr == from) {
			out = append(out, endpoint{pr, false})
		}
	}
	var ready, refused []endpoint
	for _, e := range out {
		if s.isRefused(p.ID, model, e.proto) {
			refused = append(refused, e)
		} else {
			ready = append(ready, e)
		}
	}
	if len(ready) == 0 {
		return refused
	}
	return ready
}

func refusalKey(providerID, model string, proto provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(proto)
}

// isRefused is whether the provider's model was refused on proto before.
func (s *Server) isRefused(providerID, model string, proto provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused[refusalKey(providerID, model, proto)]
}

// markRefused remembers that the provider's model is not served on proto.
func (s *Server) markRefused(providerID, model string, proto provider.Protocol) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refused == nil {
		s.refused = map[string]bool{}
	}
	s.refused[refusalKey(providerID, model, proto)] = true
}
