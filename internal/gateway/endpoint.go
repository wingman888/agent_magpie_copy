package gateway

// Endpoints: a provider serves a model on some of its APIs, not always all.
// A request goes to the APIs that serve its model, the client's own first
// when it can be relayed as it is; an API that refuses the model is
// remembered for that model alone, and the request moves on to the next.

import (
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// refusedKey names one endpoint of one provider as it serves one model.
func refusedKey(providerID, model string, pr provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(pr)
}

// refusedEndpoint reports whether an endpoint has refused the model before.
func (s *Server) refusedEndpoint(providerID, model string, pr provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused[refusedKey(providerID, model, pr)]
}

// markRefused remembers that an endpoint does not serve the model.
func (s *Server) markRefused(providerID, model string, pr provider.Protocol) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused[refusedKey(providerID, model, pr)] = true
}

// usable reports whether a request for model may be sent to p's endpoint pr:
// p has one, the APIs the model's list names include it (a list that names
// none for the model leaves every one open: declared is nil), and it has not
// refused the model before.
func (s *Server) usable(p provider.Provider, model string, pr provider.Protocol, declared []provider.Protocol) bool {
	if p.Base(pr) == "" || s.refusedEndpoint(p.ID, model, pr) {
		return false
	}
	return declared == nil || slices.Contains(declared, pr)
}

// endpoints lists the protocols a request for model is sent upstream on, in
// the order to try them. When relay is set and p serves the model on the
// client's own protocol, that one comes first and is passed through as it
// is; the rest are translations, in the order translationOrder gives.
func (s *Server) endpoints(p provider.Provider, model string, from provider.Protocol, relay bool) []provider.Protocol {
	order := translationOrder(p, model)
	if relay {
		rest := slices.DeleteFunc(slices.Clone(order), func(x provider.Protocol) bool { return x == from })
		order = append([]provider.Protocol{from}, rest...)
	}
	declared := p.APIs(model)
	var out []provider.Protocol
	for _, pr := range order {
		if s.usable(p, model, pr, declared) {
			out = append(out, pr)
		}
	}
	return out
}

// translationOrder is the order translations are tried in. OpenAI's own API,
// and Copilot's OpenAI models, are at home on Responses; other vendors on
// Chat Completions. Anthropic Messages comes last for both.
func translationOrder(p provider.Provider, model string) []provider.Protocol {
	host := p.Host()
	if host == "api.openai.com" || (host == "api.githubcopilot.com" && openAIModel(model)) {
		return []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	}
	return []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
}

// openAIModel reports whether a model is one of OpenAI's, by its name: gpt-*,
// codex*, or o followed by a digit, with any vendor/ prefix ignored.
func openAIModel(model string) bool {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "codex") ||
		(len(m) > 1 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9')
}

// endpointRefused reports a 4xx whose text says the model is not served on
// the endpoint it was asked on, so another endpoint of the provider may serve
// it. Other statuses, and other 4xx, are not that.
func endpointRefused(status int, body []byte) bool {
	if status < 400 || status >= 500 {
		return false
	}
	if notChatModel(status, body) {
		return true
	}
	msg := strings.ToLower(string(body))
	if strings.Contains(msg, "not accessible via the") && strings.Contains(msg, "endpoint") {
		return true
	}
	for _, phrase := range []string{
		"unsupported_api_for_model",
		"use /v1/chat/completions",
		"use v1/chat/completions",
		"not supported in the v1/responses",
		"not supported in /v1/responses",
	} {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}
