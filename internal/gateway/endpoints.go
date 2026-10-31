package gateway

// Endpoints: a vendor's models are not all served at every endpoint it has.
// Which ones a model can use comes from the vendor's own model list when it
// says (Copilot does); otherwise every endpoint the provider has is tried in
// turn, and one that turns the model away as not belonging there is
// remembered, so it costs one doomed request, once.

import (
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

func rejectKey(providerID, model string, proto provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(proto)
}

// endpointRejected reports whether the provider turned the model away at
// that endpoint before.
func (s *Server) endpointRejected(providerID, model string, proto provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rejected[rejectKey(providerID, model, proto)]
}

func (s *Server) rejectEndpoint(providerID, model string, proto provider.Protocol) {
	s.mu.Lock()
	if s.rejected == nil {
		s.rejected = map[string]bool{}
	}
	s.rejected[rejectKey(providerID, model, proto)] = true
	s.mu.Unlock()
}

// endpoints lists the protocols to try p at for model, best first. The
// protocol the agent spoke comes first when the model can use it, so the
// request goes as it is; the rest follow in the order they suit a
// translation.
func (s *Server) endpoints(p provider.Provider, model string, from provider.Protocol) []provider.Protocol {
	speaks := p.Speaks()
	if len(speaks) == 0 {
		return nil
	}
	allowed := speaks
	if declared := p.APIs(model); len(declared) > 0 {
		var both []provider.Protocol
		for _, pr := range speaks {
			for _, d := range declared {
				if pr == d {
					both = append(both, pr)
					break
				}
			}
		}
		if len(both) > 0 {
			allowed = both
		}
	}
	var open []provider.Protocol
	for _, pr := range allowed {
		if !s.endpointRejected(p.ID, model, pr) {
			open = append(open, pr)
		}
	}
	if len(open) == 0 {
		open = allowed // all turned away: the memory is wrong, not the model
	}
	var out []provider.Protocol
	for _, pr := range open {
		if pr == from {
			out = append(out, pr)
		}
	}
	for _, pr := range translationOrder(p, model) {
		if pr == from {
			continue
		}
		for _, o := range open {
			if o == pr {
				out = append(out, pr)
			}
		}
	}
	return out
}

// translationOrder is the order protocols suit a request that has to be
// translated: OpenAI's own models are Responses first, everything else Chat.
func translationOrder(p provider.Provider, model string) []provider.Protocol {
	if prefersResponses(p, model) {
		return []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	}
	return []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
}

func prefersResponses(p provider.Provider, model string) bool {
	m := strings.ToLower(model)
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	if strings.HasPrefix(m, "gpt-oss") {
		return false // open weights, served by whoever hosts them
	}
	openai := strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "codex") ||
		(len(m) >= 2 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9')
	if p.Account != nil && p.Account.Agent == "copilot" {
		return openai
	}
	for _, proto := range []provider.Protocol{provider.Chat, provider.Responses} {
		if provider.HostOf(p.Base(proto)) == "api.openai.com" {
			return true
		}
	}
	return false
}

// wrongEndpoint recognizes a 4xx in which the vendor says the model is not
// served at the endpoint asked, rather than anything wrong with the request.
func wrongEndpoint(status int, body []byte) bool {
	if status < 400 || status >= 500 {
		return false
	}
	msg := strings.ToLower(string(body))
	if strings.Contains(msg, "not accessible via") && strings.Contains(msg, "endpoint") {
		return true
	}
	for _, phrase := range []string{
		"unsupported_api_for_model",
		"not a chat model",
		"not supported in the v1/chat/completions",
		"not supported in /v1/chat/completions",
		"not supported in the /v1/chat/completions",
		"not supported in the v1/responses",
		"not supported in /v1/responses",
		"not supported in the /v1/responses",
		"not supported in the responses",
		"not supported in the chat",
		"use v1/completions",
		"use /v1/completions",
		"use v1/responses",
		"use /v1/responses",
		"use v1/chat/completions",
		"use /v1/chat/completions",
		"use the v1/responses",
		"use the /v1/responses",
		"use the v1/chat/completions",
		"use the /v1/chat/completions",
	} {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

func notChatModel(status int, body []byte) bool { return wrongEndpoint(status, body) }
