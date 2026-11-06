package gateway

// Endpoints: which of a provider's APIs a request for a model goes to. A
// provider serves a model on some of Chat Completions, Responses and
// Anthropic Messages, and its model list may say which. Where it does not,
// the request is sent on the API the agent spoke when the provider serves
// the model there (as is), else translated, to the API the model suits best.
// An API that says the model is not on it is skipped next time, for that
// provider and model alone.

import (
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// refusedKey is what a refusal is remembered under: one model on one API of
// one provider.
func refusedKey(providerID, model string, proto provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(proto)
}

// refuse remembers that a provider does not serve a model on an API.
func (s *Server) refuse(providerID, model string, proto provider.Protocol) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused[refusedKey(providerID, model, proto)] = true
}

// isRefused reports whether a model was refused on an API before.
func (s *Server) isRefused(providerID, model string, proto provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused[refusedKey(providerID, model, proto)]
}

// servedOn reports whether a model may be sent to a provider on an API: the
// provider speaks it, its model list does not leave it out, and it has not
// refused the model before.
func (s *Server) servedOn(p provider.Provider, model string, proto provider.Protocol) bool {
	if p.Base(proto) == "" {
		return false
	}
	if declared := p.APIs(model); declared != nil && !slices.Contains(declared, proto) {
		return false
	}
	return !s.isRefused(p.ID, model, proto)
}

// endpoints orders the APIs a request for model is tried on: the agent's own
// first when it is usable there, then the rest in the order translationOrder
// gives. APIs the model is refused on are left out, unless that leaves none:
// then they are all tried, so the last reply is still the provider's own.
func (s *Server) endpoints(p provider.Provider, model string, from provider.Protocol) []provider.Protocol {
	declared := p.APIs(model)
	usable := func(proto provider.Protocol) bool {
		return p.Base(proto) != "" && (declared == nil || slices.Contains(declared, proto))
	}
	var all, fresh []provider.Protocol
	for _, proto := range append([]provider.Protocol{from}, translationOrder(p, model)...) {
		if !usable(proto) || slices.Contains(all, proto) {
			continue
		}
		all = append(all, proto)
		if !s.isRefused(p.ID, model, proto) {
			fresh = append(fresh, proto)
		}
	}
	if len(fresh) == 0 {
		return all
	}
	return fresh
}

// translationOrder is the order a model's requests are translated to the
// APIs of a provider: Responses then Chat for an OpenAI model on OpenAI's own
// API or on Copilot, else Chat then Responses. Anthropic comes last.
func translationOrder(p provider.Provider, model string) []provider.Protocol {
	if openAIFirst(p, model) {
		return []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	}
	return []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
}

// openAIFirst reports whether an OpenAI model is to be translated to Responses
// before Chat: on api.openai.com or Copilot, where the model is OpenAI's.
func openAIFirst(p provider.Provider, model string) bool {
	official := p.Host() == "api.openai.com"
	copilot := p.ID == "copilot" || p.Account != nil && p.Account.Agent == "copilot"
	return (official || copilot) && isOpenAIModel(model)
}

// isOpenAIModel reports whether a model is one of OpenAI's by name, ignoring
// a "vendor/" prefix: gpt-*, codex*, and o followed by a digit. gpt-oss is
// open-weight and other vendors serve it under the same name, so it is not.
func isOpenAIModel(model string) bool {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.HasPrefix(m, "gpt-oss"):
		return false
	case strings.HasPrefix(m, "gpt-"), strings.HasPrefix(m, "codex"):
		return true
	case len(m) > 1 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9':
		return true
	}
	return false
}
