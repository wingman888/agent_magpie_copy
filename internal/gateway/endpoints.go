package gateway

// Endpoints: which upstream API a request goes to. A vendor's model list may
// say which APIs serve a model (provider.Provider.APIs); a request then goes
// only to those. Where the list says nothing, every API the vendor has is
// tried, and one that refuses the model ("not on this endpoint") is
// remembered for that model, so later requests skip it.

import (
	"slices"

	"github.com/yetone/magpie/internal/provider"
)

// endpoints lists the protocols a request for model goes to upstream, in
// the order to try them. The agent's own protocol comes first when the
// model is served on it (the request is relayed as it is); otherwise the
// translations the vendor prefers. Endpoints remembered as refusing the
// model are left out, unless that leaves none.
func (s *Server) endpoints(p provider.Provider, model string, from provider.Protocol) []provider.Protocol {
	served := servedAPIs(p, model)
	var ordered []provider.Protocol
	if slices.Contains(served, from) {
		ordered = append(ordered, from)
	}
	for _, pr := range preferred(p, model) {
		if pr != from && slices.Contains(served, pr) {
			ordered = append(ordered, pr)
		}
	}
	var live []provider.Protocol
	for _, pr := range ordered {
		if !s.refusedEndpoint(p.ID, model, pr) {
			live = append(live, pr)
		}
	}
	if len(live) == 0 {
		return ordered
	}
	return live
}

// servedAPIs are the endpoints a model may be sent to: the APIs its list
// declares that the vendor has a base for, or, when the list declares none,
// every endpoint the vendor has.
func servedAPIs(p provider.Provider, model string) []provider.Protocol {
	declared := p.APIs(model)
	if len(declared) == 0 {
		return p.Speaks()
	}
	var out []provider.Protocol
	for _, pr := range declared {
		if p.Base(pr) != "" && !slices.Contains(out, pr) {
			out = append(out, pr)
		}
	}
	return out
}

// declaresAPI says a model may be served on proto: its list says so, or its
// list says nothing and the vendor has the endpoint.
func declaresAPI(p provider.Provider, model string, proto provider.Protocol) bool {
	if declared := p.APIs(model); len(declared) > 0 {
		return slices.Contains(declared, proto)
	}
	return p.Base(proto) != ""
}

// preferred is the order translations are tried in: OpenAI's own models
// (on api.openai.com, or the OpenAI models on Copilot) go to Responses
// first, others to Chat Completions first.
func preferred(p provider.Provider, model string) []provider.Protocol {
	openAI := p.Host() == "api.openai.com" ||
		((p.Host() == "api.githubcopilot.com" || p.Account != nil && p.Account.Agent == "copilot") && modelFamily(model) == provider.Chat)
	if openAI {
		return []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	}
	return provider.Protocols
}

// refusalKey names a (provider, model, endpoint) triple in the refusal memory.
func refusalKey(providerID, model string, proto provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(proto)
}

// refusedEndpoint says the upstream refused the model on that endpoint
// before. It holds for that model on that provider's endpoint only.
func (s *Server) refusedEndpoint(providerID, model string, proto provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused[refusalKey(providerID, model, proto)]
}

// markRefused remembers that the model is not served on the endpoint.
func (s *Server) markRefused(providerID, model string, proto provider.Protocol) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused[refusalKey(providerID, model, proto)] = true
}
