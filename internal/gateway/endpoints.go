package gateway

// Endpoints: a provider's models are not all served on all of its APIs —
// Copilot's newer GPT models answer on /responses only, its Claude models
// on /chat/completions and /v1/messages. Where the provider's model list
// says so, a request goes to those APIs alone. Where it does not, every API
// the provider speaks may be tried, and an upstream that answers 4xx that
// the model is not served at this endpoint hands the request to the next
// API; the miss is remembered per (provider, model, endpoint) so the next
// request does not repeat it.

import (
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

func rejectKey(providerID, model string, proto provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(proto)
}

func (s *Server) isRejected(providerID, model string, proto provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rejected[rejectKey(providerID, model, proto)]
}

// reject remembers that providerID does not serve model at proto's endpoint.
func (s *Server) reject(providerID, model string, proto provider.Protocol) {
	s.mu.Lock()
	s.rejected[rejectKey(providerID, model, proto)] = true
	s.mu.Unlock()
}

// openAIModel is whether a model id is OpenAI's by its name: gpt-*, codex*,
// or o and a digit, whatever vendor/ prefix it carries.
func openAIModel(model string) bool {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "codex") ||
		(len(m) > 1 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9')
}

func isCopilot(p provider.Provider) bool {
	if p.Account != nil && p.Account.Agent == "copilot" {
		return true
	}
	h := p.Host()
	return h == "githubcopilot.com" || strings.HasSuffix(h, ".githubcopilot.com")
}

// responsesFirst is whether a request that has to be translated goes to
// Responses before Chat: OpenAI's own API, and OpenAI's models on Copilot.
func responsesFirst(p provider.Provider, model string) bool {
	if p.Host() == "api.openai.com" {
		return true
	}
	return isCopilot(p) && openAIModel(model)
}

// endpoints lists the protocols to try for a request of the from API, in
// order: the agent's own when the model is served there (nothing is
// translated), then the rest as the provider's kind prefers. Declared APIs
// are the only ones ever listed; endpoints that turned the model away are
// left out (unless that would leave none).
func (s *Server) endpoints(p provider.Provider, model string, from provider.Protocol) []provider.Protocol {
	var speaks []provider.Protocol
	declared := p.APIs(model)
	for _, pr := range p.Speaks() {
		if len(declared) == 0 || containsProto(declared, pr) {
			speaks = append(speaks, pr)
		}
	}
	open := speaks[:0:0]
	for _, pr := range speaks {
		if !s.isRejected(p.ID, model, pr) {
			open = append(open, pr)
		}
	}
	if len(open) == 0 {
		open = speaks // all turned it away once: ask again rather than give up
	}
	order := []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
	if responsesFirst(p, model) {
		order = []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	}
	var out []provider.Protocol
	if containsProto(open, from) {
		out = append(out, from)
	}
	for _, pr := range order {
		if pr != from && containsProto(open, pr) {
			out = append(out, pr)
		}
	}
	return out
}

// servesOn is whether the model can go to proto's endpoint of p: it has one,
// the provider's list does not place the model elsewhere only, and the
// endpoint has not turned it away.
func (s *Server) servesOn(p provider.Provider, model string, proto provider.Protocol) bool {
	if p.Base(proto) == "" {
		return false
	}
	if d := p.APIs(model); len(d) > 0 && !containsProto(d, proto) {
		return false
	}
	return !s.isRejected(p.ID, model, proto)
}

func containsProto(ps []provider.Protocol, x provider.Protocol) bool {
	for _, p := range ps {
		if p == x {
			return true
		}
	}
	return false
}

// wrongEndpoint recognizes the 4xx answers of an upstream that has the model
// but not at the endpoint asked: OpenAI-compatible servers ("not a chat
// model", "use v1/responses"), Copilot ("not accessible via the … endpoint",
// unsupported_api_for_model). A 5xx or any other status is not one.
func wrongEndpoint(status int, body []byte) bool {
	if status < 400 || status >= 500 {
		return false
	}
	msg := strings.ToLower(string(body))
	if strings.Contains(msg, "not accessible via the") && strings.Contains(msg, "endpoint") {
		return true
	}
	for _, phrase := range []string{
		"unsupported_api_for_model",
		"not a chat model",
		"not supported in the v1/chat/completions",
		"not supported in /v1/chat/completions",
		"not supported in the v1/responses",
		"not supported in /v1/responses",
		"use v1/completions",
		"use /v1/completions",
		"use v1/responses",
		"use /v1/responses",
		"use v1/chat/completions",
		"use /v1/chat/completions",
	} {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// notChatModel recognizes the errors used by OpenAI-compatible servers when a
// model can only be called through /responses.
func notChatModel(status int, body []byte) bool { return wrongEndpoint(status, body) }
