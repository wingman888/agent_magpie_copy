package gateway

import (
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// offered lists the protocols a provider may be asked for model on, in the
// provider's own order. When the vendor's model list says which APIs serve
// the model, only those count, and nothing is probed on the others; when the
// list says nothing about it, every protocol the provider speaks does.
func offered(p provider.Provider, model string) []provider.Protocol {
	speaks := p.Speaks()
	declared := p.APIs(model)
	if declared == nil {
		return speaks
	}
	var out []provider.Protocol
	for _, pr := range speaks {
		if slices.Contains(declared, pr) {
			out = append(out, pr)
		}
	}
	return out
}

// usable is offered without the endpoints that refused model before.
func (s *Server) usable(p provider.Provider, model string) []provider.Protocol {
	var out []provider.Protocol
	for _, pr := range offered(p, model) {
		if !s.refusedBefore(p.ID, model, pr) {
			out = append(out, pr)
		}
	}
	return out
}

// endpoints orders the protocols a request for model is tried on. When the
// client's own protocol is among them it goes first, so the request is relayed
// as it came; the rest follow the provider's translation preference. An
// endpoint is left out for good once it has refused the model. When every
// endpoint has refused it, the offered ones are tried again rather than none.
// prefer may be "" to leave the client's protocol out of the first place.
func (s *Server) endpoints(p provider.Provider, model string, prefer provider.Protocol) []provider.Protocol {
	set := s.usable(p, model)
	if len(set) == 0 {
		set = offered(p, model)
	}
	var out []provider.Protocol
	if prefer != "" && slices.Contains(set, prefer) {
		out = append(out, prefer)
	}
	for _, pr := range preference(p, model) {
		if slices.Contains(set, pr) && !slices.Contains(out, pr) {
			out = append(out, pr)
		}
	}
	return out
}

// preference is the order a translated request tries the protocols in.
// OpenAI's own API and Copilot's OpenAI models prefer Responses; other
// vendors prefer Chat Completions. Anthropic Messages comes last.
func preference(p provider.Provider, model string) []provider.Protocol {
	if p.Host() == "api.openai.com" || (isCopilot(p) && openAIModel(model)) {
		return []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	}
	return []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
}

func isCopilot(p provider.Provider) bool {
	return p.ID == "copilot" || (p.Account != nil && p.Account.Agent == "copilot")
}

// openAIModel reports whether a model name is one of OpenAI's: gpt-*, codex*,
// or o followed by a digit (o4-mini). A "vendor/" prefix is ignored.
func openAIModel(model string) bool {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	model = strings.ToLower(model)
	if strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "codex") {
		return true
	}
	return len(model) >= 2 && model[0] == 'o' && model[1] >= '0' && model[1] <= '9'
}

// refusedBefore reports whether endpoint proto already refused model at p.
func (s *Server) refusedBefore(providerID, model string, proto provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused[refusalKey(providerID, model, proto)]
}

// markRefused remembers that endpoint proto does not serve model at p, for
// this provider and this model only.
func (s *Server) markRefused(providerID, model string, proto provider.Protocol) {
	s.mu.Lock()
	s.refused[refusalKey(providerID, model, proto)] = true
	s.mu.Unlock()
}

func refusalKey(providerID, model string, proto provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(proto)
}

var notOnEndpointRE = regexp.MustCompile(`not accessible via the [a-z0-9_/. -]*endpoint`)

// notOnEndpointPhrases are the ways OpenAI-compatible servers say a model is
// not served on the endpoint asked, and that another endpoint would serve it.
var notOnEndpointPhrases = []string{
	"unsupported_api_for_model",
	"not a chat model",
	"not supported in the v1/chat/completions",
	"not supported in /v1/chat/completions",
	"not supported in the v1/responses",
	"not supported in v1/responses",
	"not supported in /v1/responses",
	"use v1/responses",
	"use /v1/responses",
	"use v1/chat/completions",
	"use /v1/chat/completions",
	"use v1/completions",
	"use /v1/completions",
}

// notChatModel reports whether a reply says the model is not served on the
// endpoint it was asked on. Only a 4xx counts: a server error says nothing
// about which endpoint the model is on.
func notChatModel(status int, body []byte) bool {
	if status < 400 || status >= 500 {
		return false
	}
	msg := strings.ToLower(string(body))
	if notOnEndpointRE.MatchString(msg) {
		return true
	}
	for _, phrase := range notOnEndpointPhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}
