package gateway

// Endpoints: a model is served on some of a provider's APIs, not all of
// them. OpenAI's newest models answer only /responses, Claude's only Chat
// Completions or Messages. A request goes to an API the model is served on:
// the one the agent spoke when that serves it (relayed as-is), else one
// the request is translated to. The provider's model list says which APIs
// a model has when it says; when it doesn't, the APIs are tried in turn,
// and an API that turns the model away is remembered, so the next request
// goes straight to the one that serves it.

import (
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// declaredAPIs is what the provider's last model list says the model is
// served on, nil when it says nothing. A gateway names models
// "vendor/model", which the list may hold by their bare id: that is tried
// too, as catalog.Decorate does.
func declaredAPIs(p provider.Provider, model string) []provider.Protocol {
	if a := p.APIs(model); a != nil {
		return a
	}
	if i := strings.LastIndexByte(model, '/'); i >= 0 {
		return p.APIs(model[i+1:])
	}
	return nil
}

// servedOn says whether a provider can take a model's request on an API:
// it has the endpoint, the model's list does not say otherwise, and the
// API was not refused for the model.
func (s *Server) servedOn(p provider.Provider, model string, api provider.Protocol) bool {
	if !declares(p, model, api) {
		return false
	}
	return !s.refusedAPI(p.ID, model, api)
}

// declares is servedOn without the refusals: the endpoint is there, and the
// model's list, when it has one, includes the API.
func declares(p provider.Provider, model string, api provider.Protocol) bool {
	if p.Base(api) == "" {
		return false
	}
	d := declaredAPIs(p, model)
	return d == nil || slices.Contains(d, api)
}

// preferred is the order a request is translated to APIs in. Models of
// OpenAI's family (gpt-*, codex*, o-series) on OpenAI's own API, or on
// Copilot, go to /responses first; anything else to Chat Completions
// first, Responses after it.
func preferred(p provider.Provider, model string) []provider.Protocol {
	openAI := modelFamily(model) == provider.Chat
	if p.Host() == "api.openai.com" || openAI && p.Account != nil && p.Account.Agent == "copilot" {
		return []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	}
	return []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
}

// endpoints lists the APIs a model's request is sent to, in the order to try
// them: the agent's own first when the provider serves the model on it and
// relay allows (sent as-is), then the translations in preferred order.
// Those refused before for the model come last, and are dropped when
// others are left; when every one was refused before, all are tried again,
// so the upstream's word stays the answer. Empty when none serves it.
func (s *Server) endpoints(p provider.Provider, model string, from provider.Protocol, relay bool) []provider.Protocol {
	var all []provider.Protocol
	if relay && declares(p, model, from) {
		all = append(all, from)
	}
	for _, api := range preferred(p, model) {
		if !slices.Contains(all, api) && declares(p, model, api) {
			all = append(all, api)
		}
	}
	var ready []provider.Protocol
	for _, api := range all {
		if !s.refusedAPI(p.ID, model, api) {
			ready = append(ready, api)
		}
	}
	if len(ready) == 0 {
		return all
	}
	return ready
}

// refusedKey names a (provider, model, API) triple: a refusal holds for that
// model on that provider's API only.
func refusedKey(providerID, model string, api provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(api)
}

func (s *Server) refusedAPI(providerID, model string, api provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused[refusedKey(providerID, model, api)]
}

// setRefused remembers, or forgets, that an API turned a model away.
func (s *Server) setRefused(providerID, model string, api provider.Protocol, refused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if refused {
		s.refused[refusedKey(providerID, model, api)] = true
	} else {
		delete(s.refused, refusedKey(providerID, model, api))
	}
}
