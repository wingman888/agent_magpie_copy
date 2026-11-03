package gateway

// Endpoint choice: which wire API a request for a model is sent to. A
// provider's model list may say which APIs serve each model; a model the
// list says nothing about may be asked of any API the provider has. A
// request goes to the client's own API when the model has it, else is
// translated to another; an endpoint that turns the model away is
// remembered, and the request goes to the next one.

import (
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// servedAPIs are the endpoints a model may be asked of: the APIs its
// provider's list declares for it that the provider has a base for; or,
// when the list declares none, every endpoint the provider has.
func servedAPIs(p provider.Provider, model string) []provider.Protocol {
	declared := p.APIs(model)
	if len(declared) == 0 {
		return p.Speaks()
	}
	var out []provider.Protocol
	for _, pr := range declared {
		if p.Base(pr) != "" {
			out = append(out, pr)
		}
	}
	return out
}

// translateOrder is the order endpoints are tried in when a request has to
// be translated: Chat first, as most compatible vendors want it.
var translateOrder = []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}

// openAIOrder is the same for OpenAI's own models, which are at home on
// Responses: OpenAI's API, and the OpenAI models Copilot serves.
var openAIOrder = []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}

// preference is the order translation tries endpoints in for a model.
func preference(p provider.Provider, model string) []provider.Protocol {
	if p.Host() == "api.openai.com" || (p.Account != nil && p.Account.Agent == "copilot" && modelFamily(model) == provider.Chat) {
		return openAIOrder
	}
	return translateOrder
}

// endpoints lists the endpoints a request for model is tried on, in order.
// The client's own API comes first when relay allows passing the request
// through and the model has it. Endpoints remembered as refusing the model
// are left out, unless that would leave none.
func (s *Server) endpoints(p provider.Provider, model string, from provider.Protocol, relay bool) []provider.Protocol {
	all := servedAPIs(p, model)
	live := slices.DeleteFunc(slices.Clone(all), func(pr provider.Protocol) bool { return s.isRefused(p.ID, model, pr) })
	if len(live) > 0 {
		all = live
	}
	if len(all) == 0 {
		return nil
	}
	var order []provider.Protocol
	if relay && slices.Contains(all, from) {
		order = append(order, from)
	}
	for _, pr := range preference(p, model) {
		if slices.Contains(all, pr) && !slices.Contains(order, pr) {
			order = append(order, pr)
		}
	}
	return order
}

// servesVia is whether a model may be asked of an endpoint at all right now.
func (s *Server) servesVia(p provider.Provider, model string, pr provider.Protocol) bool {
	return slices.Contains(servedAPIs(p, model), pr) && !s.isRefused(p.ID, model, pr)
}

func refusalKey(providerID, model string, pr provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(pr)
}

// markRefused remembers that an endpoint does not serve a model of a
// provider. Other models and other providers are not affected.
func (s *Server) markRefused(providerID, model string, pr provider.Protocol) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused[refusalKey(providerID, model, pr)] = true
}

func (s *Server) isRefused(providerID, model string, pr provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused[refusalKey(providerID, model, pr)]
}

// clearRefused forgets a refusal once the endpoint has answered for the model.
func (s *Server) clearRefused(providerID, model string, pr provider.Protocol) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.refused, refusalKey(providerID, model, pr))
}

// refusalPhrases are how upstreams say a model is not served by the
// endpoint a request went to, while the status is a client error.
var refusalPhrases = []string{
	"not a chat model",
	"not supported in the v1/chat/completions",
	"not supported in /v1/chat/completions",
	"not supported in the v1/responses",
	"not supported in /v1/responses",
	"not supported in the v1/completions",
	"use v1/completions",
	"use /v1/completions",
	"use v1/responses",
	"use /v1/responses",
	"use v1/chat/completions",
	"use /v1/chat/completions",
	"unsupported_api_for_model",
}

var notAccessibleVia = regexp.MustCompile(`not accessible via the .{0,40}endpoint`)

// notServedHere recognizes a 4xx reply that says the model is not served
// through the endpoint the request went to, so another endpoint may serve
// it. Server errors never count: those are the provider failing, not the
// endpoint being wrong.
func notServedHere(status int, body []byte) bool {
	if status < 400 || status >= 500 {
		return false
	}
	msg := strings.ToLower(string(body))
	for _, phrase := range refusalPhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return notAccessibleVia.MatchString(msg)
}

// notChatModel is notServedHere under the name the Chat-only fallback had.
func notChatModel(status int, body []byte) bool {
	return notServedHere(status, body)
}
