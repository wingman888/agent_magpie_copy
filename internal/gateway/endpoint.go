package gateway

// Endpoint choice: one provider can serve a model on some of its endpoints
// only. A model list that declares where a model lives is believed; when it
// says nothing, every endpoint the provider has is tried in turn, and an
// endpoint that answers "not here" (a 4xx saying the model is not served on
// it) is remembered per provider, model and endpoint, and skipped from then on.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

func rejectKey(providerID, model string, proto provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(proto)
}

func (s *Server) endpointRejected(providerID, model string, proto provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rejected[rejectKey(providerID, model, proto)]
}

func (s *Server) markRejected(providerID, model string, proto provider.Protocol) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rejected == nil {
		s.rejected = make(map[string]bool)
	}
	s.rejected[rejectKey(providerID, model, proto)] = true
}

// declared are the provider's endpoints that its model list says carry the
// model, nil when it says nothing usable.
func declared(p provider.Provider, model string) []provider.Protocol {
	var out []provider.Protocol
	for _, pr := range p.APIs(model) {
		if p.Base(pr) != "" {
			out = append(out, pr)
		}
	}
	return out
}

// endpoints are the provider's endpoints worth sending the model to: those
// its list declares, else all it has, less those that turned the model away.
// When every one has, they are all tried again — there is nothing else to do.
func (s *Server) endpoints(p provider.Provider, model string) []provider.Protocol {
	all := p.Speaks()
	if d := declared(p, model); len(d) > 0 {
		var f []provider.Protocol
		for _, pr := range all {
			if slices.Contains(d, pr) {
				f = append(f, pr)
			}
		}
		all = f
	}
	var live []provider.Protocol
	for _, pr := range all {
		if !s.endpointRejected(p.ID, model, pr) {
			live = append(live, pr)
		}
	}
	if len(live) == 0 {
		return all
	}
	return live
}

// canSpeak reports whether the model can really be sent over the protocol:
// the provider has the endpoint, its list does not place the model
// elsewhere, and the endpoint has not turned the model away.
func (s *Server) canSpeak(p provider.Provider, model string, proto provider.Protocol) bool {
	if p.Base(proto) == "" || s.endpointRejected(p.ID, model, proto) {
		return false
	}
	if d := declared(p, model); len(d) > 0 {
		return slices.Contains(d, proto)
	}
	return true
}

// openAIModel says whether a model name is OpenAI's: gpt-*, codex*, or o and a digit,
// with any vendor/ prefix ignored.
func openAIModel(model string) bool {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "codex") ||
		len(m) > 1 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9'
}

// prefersResponses: OpenAI's own API, and OpenAI's models on Copilot, are
// at home on Responses; everyone else's models are best met on Chat.
func prefersResponses(p provider.Provider, model string) bool {
	for _, pr := range provider.Protocols {
		if provider.HostOf(p.Base(pr)) == "api.openai.com" {
			return true
		}
	}
	copilot := p.Account != nil && p.Account.Agent == "copilot"
	if !copilot {
		h := p.Host()
		copilot = h == "githubcopilot.com" || strings.HasSuffix(h, ".githubcopilot.com")
	}
	return copilot && openAIModel(model)
}

// route is the endpoints to try for a request, first choice first: the
// agent's own protocol when the model can be sent over it (nothing to
// translate), then the others as the model suits them.
func (s *Server) route(p provider.Provider, model string, from provider.Protocol) []provider.Protocol {
	avail := s.endpoints(p, model)
	var out []provider.Protocol
	if slices.Contains(avail, from) {
		out = append(out, from)
	}
	pref := []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
	if prefersResponses(p, model) {
		pref[0], pref[1] = provider.Responses, provider.Chat
	}
	for _, pr := range pref {
		if pr != from && slices.Contains(avail, pr) {
			out = append(out, pr)
		}
	}
	return out
}

// apiRejected recognizes a 4xx that says the model is not served on the
// endpoint it was sent to, as OpenAI-compatible servers and Copilot word it.
func apiRejected(status int, body []byte) bool {
	if status < 400 || status >= 500 || status == 401 || status == 408 || status == 429 {
		return false
	}
	msg := strings.ToLower(string(body))
	if strings.Contains(msg, "not accessible via the") && strings.Contains(msg, "endpoint") {
		return true
	}
	if notChatModel(status, body) {
		return true
	}
	for _, phrase := range []string{
		"unsupported_api_for_model",
		"use v1/chat/completions",
		"use /v1/chat/completions",
		"use chat/completions",
		"use /chat/completions",
		"not supported in the v1/responses",
		"not supported in /v1/responses",
		"not supported in v1/responses",
	} {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// open sends the request over the first endpoint that does not turn the
// model away, nothing having been written to the agent. A body sent over the
// agent's own protocol is relayed with only the model swapped; any other is
// translated from the parsed request. It returns the reply, the endpoint
// that gave it and the parsed request when one was made (nil if the agent's
// protocol was relayed throughout). On a transport error the endpoint is the
// one that failed.
func (s *Server) open(ctx context.Context, p provider.Provider, from provider.Protocol, order []provider.Protocol, relay bool,
	model string, body []byte, in http.Header) (res *http.Response, used provider.Protocol, req *Request, err error) {
	var parsed *Request
	for i, proto := range order {
		used = proto
		var sent []byte
		if proto == from && relay {
			sent = rewriteModel(body, model)
			if proto == provider.Chat {
				sent = developerAsSystem(sent)
			}
		} else {
			if parsed == nil {
				r, perr := parse(from, body)
				if perr != nil {
					return nil, used, nil, badRequest{perr}
				}
				r.Stream = true // the provider is always streamed on this path
				parsed = r
			}
			sent = build(proto, parsed, model, p.Host(), p.RejectsTemperature(model))
		}
		res, err = s.forward(ctx, p, proto, pathOf(proto), p.Prepare(sent), in)
		if err != nil {
			return nil, used, parsed, err
		}
		if res.StatusCode < 400 || res.StatusCode >= 500 {
			return res, used, parsed, nil
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(b))
		if !apiRejected(res.StatusCode, b) {
			return res, used, parsed, nil
		}
		s.markRejected(p.ID, model, proto)
		if i == len(order)-1 || ctx.Err() != nil {
			return res, used, parsed, nil // nowhere else to go: the agent gets this error
		}
	}
	return res, used, parsed, err
}

// badRequest is a request body magpie could not parse for translation.
type badRequest struct{ error }
