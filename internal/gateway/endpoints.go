package gateway

// Endpoints: a model is served on some of a provider's wire APIs and turned
// away by the others. The provider's model list says which, when it does.
// When it does not, a request goes to one API, and if that API refuses the
// model, to the provider's next API. Each refusal is remembered per provider,
// model and API, so the next request goes straight to an API that serves it.

import (
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// endpointRefusal matches the messages upstreams use when a model is not
// served on the API asked. See rejectsEndpoint.
var endpointRefusal = regexp.MustCompile(`(?i)not accessible via the .{1,80}endpoint|unsupported_api_for_model|use v1/responses|use /v1/chat/completions|not supported in the v1/responses|not a chat model`)

// rejectsEndpoint says whether a reply means the model is not served on the
// API it was sent to, so another API of the provider may serve it. Only 4xx
// replies qualify; a 5xx that happens to name an API is an outage, not a
// refusal.
func rejectsEndpoint(status int, body []byte) bool {
	if status < 400 || status >= 500 {
		return false
	}
	return notChatModel(status, body) || endpointRefusal.Match(body)
}

// isOpenAIModel says whether a model is OpenAI's by its name: gpt-*, codex*,
// or o followed by a digit (o3, o4-mini). A vendor/ prefix is ignored. The
// open-weight gpt-oss models are not OpenAI's API models, whatever their name.
func isOpenAIModel(model string) bool {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if strings.HasPrefix(m, "gpt-oss") {
		return false
	}
	return strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "codex") ||
		(len(m) > 1 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9')
}

// streamRefused says whether an error event that came before any content
// means the model is not served on the API it was sent to: the same test as
// rejectsEndpoint, which has no status to go by on a stream.
func streamRefused(f *streamFault) bool {
	return f != nil && (endpointRefusal.MatchString(f.msg) || notChatModel(400, []byte(f.msg)))
}

// isCopilot says whether a provider is GitHub Copilot, signed in or keyed.
func isCopilot(p provider.Provider) bool {
	return p.Account != nil && p.Account.Agent == "copilot" || strings.HasSuffix(p.Host(), "githubcopilot.com")
}

// translationOrder is the order a request is translated to the provider's
// APIs in. OpenAI's own models, on api.openai.com or Copilot, prefer
// Responses; everything else prefers Chat Completions. Anthropic comes last.
func translationOrder(p provider.Provider, model string) []provider.Protocol {
	if isOpenAIModel(model) && (p.Host() == "api.openai.com" || isCopilot(p)) {
		return []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	}
	return []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
}

// endpoints is the APIs a request for model goes to on p, in the order to try
// them. The agent's own API comes first, forwarded as it is, when relay allows
// it and the provider serves the model on it. The others are translated, in
// translationOrder. An API the model is known not to be served on, or one that
// refused it before, is left out. When every API is left out by refusals, the
// refusals are ignored and all the declared APIs are tried again.
func (s *Server) endpoints(p provider.Provider, model string, from provider.Protocol, relay bool) []provider.Protocol {
	if order := s.endpointsWhere(p, model, from, relay, true); len(order) > 0 {
		return order
	}
	return s.endpointsWhere(p, model, from, relay, false)
}

func (s *Server) endpointsWhere(p provider.Provider, model string, from provider.Protocol, relay, remembered bool) []provider.Protocol {
	declared := p.APIs(model) // nil: the list says nothing, so all the provider's APIs may be tried
	usable := func(pr provider.Protocol) bool {
		if p.Base(pr) == "" {
			return false
		}
		if declared != nil && !slices.Contains(declared, pr) {
			return false
		}
		return !remembered || !s.refused(p.ID, model, pr)
	}
	var out []provider.Protocol
	passthrough := relay && slices.Contains(provider.Protocols, from) && usable(from)
	if passthrough {
		out = append(out, from)
	}
	for _, pr := range translationOrder(p, model) {
		if (passthrough && pr == from) || !usable(pr) {
			continue
		}
		out = append(out, pr)
	}
	return out
}

// refusalKey names one provider's model on one API.
func refusalKey(providerID, model string, proto provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(proto)
}

// refused says whether an API has turned a provider's model away before.
func (s *Server) refused(providerID, model string, proto provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refusals[refusalKey(providerID, model, proto)]
}

// refuse remembers that an API turned a provider's model away.
func (s *Server) refuse(providerID, model string, proto provider.Protocol) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refusals == nil {
		s.refusals = make(map[string]bool)
	}
	s.refusals[refusalKey(providerID, model, proto)] = true
}
