package gateway

import (
	"net/http"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// endpointRoute is one endpoint a request may be sent to: the agent's own API as it
// is (relay), or a translation of the request into another API.
type endpointRoute struct {
	to    provider.Protocol
	relay bool
}

// refusal is an upstream reply saying the model is not served at the endpoint
// asked. Nothing has been written to the agent when one is returned.
type refusal struct {
	status int
	msg    string
	header http.Header
}

// answerRefusal writes the last refusal as the answer, the way an ordinary
// upstream error is written.
func answerRefusal(w http.ResponseWriter, from provider.Protocol, ref *refusal) (int, string) {
	keepRetry(w.Header(), ref.header)
	return writeError(w, from, ref.status, ref.msg), ref.msg
}

// routes lists the endpoints a request for model may go to, first choice
// first: the agent's own API when the provider serves the model there, then
// translations to the other APIs the provider serves the model at. Endpoints
// remembered as refusing the model are left out, unless that leaves nothing
// to try: a memory must not make the model unreachable for good.
func (s *Server) routes(p provider.Provider, model string, from provider.Protocol, body []byte) []endpointRoute {
	if out := s.routesWith(p, model, from, body, true); len(out) > 0 {
		return out
	}
	return s.routesWith(p, model, from, body, false)
}

// routesWith is routes, with or without the endpoints remembered as refusing.
func (s *Server) routesWith(p provider.Provider, model string, from provider.Protocol, body []byte, remembered bool) []endpointRoute {
	usable := func(pr provider.Protocol) bool {
		return p.Base(pr) != "" && declares(p, model, pr) && !(remembered && s.refusedEndpoint(p.ID, model, pr))
	}
	var out []endpointRoute
	relay := usable(from) && relayable(p, from, body)
	if relay {
		out = append(out, endpointRoute{to: from, relay: true})
	}
	prefs := []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
	if openAIFirst(p, model) {
		prefs = []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	}
	for _, pr := range prefs {
		if relay && pr == from {
			continue
		}
		if usable(pr) {
			out = append(out, endpointRoute{to: pr})
		}
	}
	return out
}

// relayable is whether the provider takes the agent's own API as it is: it
// has that endpoint, and a backend that only streams gets streamed requests.
func relayable(p provider.Provider, from provider.Protocol, body []byte) bool {
	return p.Base(from) != "" && (p.Account == nil || !p.Account.Stream || streamOf(body))
}

// declares is whether the provider may be asked for model at proto: its model
// list names the APIs of that model and proto is one of them. A list that says
// nothing about the model allows every API the provider has.
func declares(p provider.Provider, model string, proto provider.Protocol) bool {
	apis := p.APIs(model)
	return apis == nil || slices.Contains(apis, proto)
}

// openAIFirst is whether a translated request for model tries Responses before
// Chat: OpenAI's own models on api.openai.com or on Copilot. The vendor prefix
// of "vendor/model" does not count.
func openAIFirst(p provider.Provider, model string) bool {
	if p.Host() != "api.openai.com" && (p.Account == nil || p.Account.Agent != "copilot") {
		return false
	}
	id := strings.ToLower(model)
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	// gpt-oss is an open-weight model of another vendor, served as such
	if strings.HasPrefix(id, "gpt-oss") {
		return false
	}
	if strings.HasPrefix(id, "gpt-") || strings.HasPrefix(id, "codex") {
		return true
	}
	return len(id) > 1 && id[0] == 'o' && id[1] >= '0' && id[1] <= '9'
}

func refusedKey(providerID, model string, proto provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(proto)
}

// refusedEndpoint is whether the endpoint was found to refuse the model.
func (s *Server) refusedEndpoint(providerID, model string, proto provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused[refusedKey(providerID, model, proto)]
}

// markRefused remembers that the endpoint refuses the model. Only that
// provider, model and endpoint are affected.
func (s *Server) markRefused(providerID, model string, proto provider.Protocol) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused[refusedKey(providerID, model, proto)] = true
}
