package gateway

// Endpoints: a vendor may serve a model on some of its APIs only — Copilot
// serves its newest GPT models on /responses alone and Claude on Chat and
// Messages, OpenAI keeps some models to /responses. When the vendor's model
// list says which, a request goes only there; when it doesn't, the APIs are
// tried in turn, a refusal that says "not on this endpoint" moving the
// request to the next one, and the refusal remembered so the next request
// goes straight to what works.

import (
	"regexp"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/provider"
)

// endpointsOff remembers which (provider, model, endpoint) turned a model
// away as not served there. The endpoint's base URL is part of the key, so
// two providers saved under one id over time don't share what was learned.
var endpointsOff = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

func offKey(p provider.Provider, model string, proto provider.Protocol) string {
	return p.ID + "\x00" + p.Base(proto) + "\x00" + model + "\x00" + string(proto)
}

// markOff remembers that the provider does not serve model at proto.
func markOff(p provider.Provider, model string, proto provider.Protocol) {
	endpointsOff.Lock()
	endpointsOff.m[offKey(p, model, proto)] = true
	endpointsOff.Unlock()
}

func isOff(p provider.Provider, model string, proto provider.Protocol) bool {
	endpointsOff.Lock()
	defer endpointsOff.Unlock()
	return endpointsOff.m[offKey(p, model, proto)]
}

// servable lists the APIs a request for model may go to at p: those it has
// an endpoint for, narrowed to those its model list declares when it does,
// less those that turned the model away before. When every one of them has
// turned it away, they are all tried again rather than none: what was
// learned may be out of date.
func servable(p provider.Provider, model string) []provider.Protocol {
	declared := p.APIs(model)
	var all, on []provider.Protocol
	for _, pr := range p.Speaks() {
		if len(declared) > 0 && !containsProto(declared, pr) {
			continue // never probe an API the vendor says the model isn't on
		}
		all = append(all, pr)
		if !isOff(p, model, pr) {
			on = append(on, pr)
		}
	}
	if len(on) == 0 {
		return all
	}
	return on
}

// servedOn reports whether model may be asked for at proto's endpoint of p.
func servedOn(p provider.Provider, model string, proto provider.Protocol) bool {
	return containsProto(servable(p, model), proto)
}

// endpoints is the order the APIs are tried in for one request: the
// agent's own first when it may be relayed as it is, then the rest in the
// order translation prefers them for this provider and model.
func endpoints(p provider.Provider, model string, from provider.Protocol, relay bool) []provider.Protocol {
	can := servable(p, model)
	var out []provider.Protocol
	if relay && containsProto(can, from) {
		out = append(out, from)
	}
	for _, pr := range preference(p, model) {
		if containsProto(can, pr) && !containsProto(out, pr) {
			out = append(out, pr)
		}
	}
	return out
}

// preference is the order translation prefers the APIs in: Responses
// first for OpenAI's own models at OpenAI or Copilot, where the newest are
// on /responses alone; Chat first for everyone else.
func preference(p provider.Provider, model string) []provider.Protocol {
	if prefersResponses(p, model) {
		return []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	}
	return []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
}

// prefersResponses: the official OpenAI API, or an OpenAI model (gpt-*,
// codex*, o1…o9…) at Copilot.
func prefersResponses(p provider.Provider, model string) bool {
	for _, pr := range provider.Protocols {
		if u := p.Base(pr); u != "" && provider.HostOf(u) == "api.openai.com" {
			return true
		}
	}
	if p.ID == "copilot" || (p.Account != nil && p.Account.Agent == "copilot") {
		return openAIModel(model)
	}
	return false
}

// openAIModel reports whether a model is OpenAI's by its name, any
// "vendor/" prefix aside.
func openAIModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "codex") ||
		(len(m) > 1 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9')
}

func containsProto(xs []provider.Protocol, x provider.Protocol) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// wrongEndpointWords are how vendors say a model is not served at the
// endpoint asked, and which one to use instead.
var wrongEndpointWords = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`not accessible via the .{0,64}endpoint`,
	`unsupported_api_for_model`,
	`not a chat model`,
	`use /?v1/responses`,
	`use /?v1/chat/completions`,
	`use /?v1/completions`,
	`use /?v1/messages`,
	`use the responses api`,
	`use the chat completions api`,
	`not supported (in|on|via|by|for) (the )?/?v1/(responses|chat/completions|messages)`,
	`only supported (in|on|via|by) (the )?/?v1/(responses|chat/completions|messages)`,
	`not supported (in|on|via|by|with) (the )?(responses|chat completions|messages) (api|endpoint)`,
}, "|"))

// wrongEndpoint reports a refusal that says the model is not served at the
// endpoint asked: a 4xx whose message says so. A server error, however
// worded, says nothing about where the model lives.
func wrongEndpoint(status int, body []byte) bool {
	return status >= 400 && status < 500 && wrongEndpointWords.Match(body)
}

// notChatModel is wrongEndpoint, under the name it had when Chat was the
// only endpoint a model could be turned away from.
func notChatModel(status int, body []byte) bool { return wrongEndpoint(status, body) }
