package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// Which API a model is served over differs by vendor and by model. The
// vendor's model list says so when it can (provider.APIs); when it can't, a
// request is sent to an API the model turns away and the next one is tried.
// What was turned away is remembered per (provider, model, API).

// refusalPhrases are how upstreams say "this model isn't served on this
// endpoint". Matched in lower case.
var refusalPhrases = []string{
	"unsupported_api_for_model",
	"not a chat model",
	"not supported in the v1/chat/completions",
	"not supported in /v1/chat/completions",
	"not supported in the v1/responses",
	"not supported in /v1/responses",
	"use v1/chat/completions",
	"use /v1/chat/completions",
	"use v1/responses",
	"use /v1/responses",
	"use v1/completions",
	"use /v1/completions",
}

// notAccessible matches "... is not accessible via the /chat/completions endpoint".
var notAccessible = regexp.MustCompile(`not accessible via the [^ "]+ endpoint`)

// endpointRefused reports whether a reply is a client-side 4xx saying the
// model is not served on the endpoint it was sent to. Other statuses never
// count, even when the text reads the same (a 500 saying "use v1/responses"
// is a server fault, not a refusal).
func endpointRefused(status int, body []byte) bool {
	if status < 400 || status >= 500 {
		return false
	}
	return namesOtherAPI(body)
}

// namesOtherAPI reports whether a reply's text says the model is served on
// another endpoint, whatever the status. Retrying the same endpoint cannot
// help such a reply, so a server fault that carries this wording is not
// retried; endpointRefused still decides what moves on.
func namesOtherAPI(body []byte) bool {
	msg := strings.ToLower(string(body))
	if notAccessible.MatchString(msg) {
		return true
	}
	for _, phrase := range refusalPhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// refusedKey names one (provider, model, API) triple in the refusal memory.
func refusedKey(providerID, model string, proto provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(proto)
}

// isRefused reports whether the provider turned model away on proto before.
func (s *Server) isRefused(providerID, model string, proto provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused[refusedKey(providerID, model, proto)]
}

// markRefused remembers that the provider does not serve model on proto.
func (s *Server) markRefused(providerID, model string, proto provider.Protocol) {
	s.mu.Lock()
	s.refused[refusedKey(providerID, model, proto)] = true
	s.mu.Unlock()
}

// openAIModel reports whether a model name is one of OpenAI's: gpt-*,
// codex*, or o followed by a digit, ignoring any "vendor/" prefix. gpt-oss
// models are open weights served by other vendors, so they are excluded.
func openAIModel(model string) bool {
	name := strings.ToLower(model)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if strings.HasPrefix(name, "gpt-oss") {
		return false
	}
	if strings.HasPrefix(name, "gpt-") || strings.HasPrefix(name, "codex") {
		return true
	}
	return len(name) >= 2 && name[0] == 'o' && name[1] >= '0' && name[1] <= '9'
}

// preferResponses reports whether a translated request to this model should
// try Responses before Chat: on OpenAI's own API, and for OpenAI models on
// Copilot.
func preferResponses(p provider.Provider, model string) bool {
	if p.Host() == "api.openai.com" {
		return true
	}
	return p.Account != nil && p.Account.Agent == "copilot" && openAIModel(model)
}

// translateOrder is the order to try APIs in when a request has to be
// translated into one the provider serves.
func translateOrder(p provider.Provider, model string) []provider.Protocol {
	if preferResponses(p, model) {
		return []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	}
	return []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
}

// endpoints lists the APIs to try for model on p, in order. The client's own
// API comes first when the provider serves it (relay); the rest follow the
// translation order. The list is empty when the provider can't serve the
// model at all.
func (s *Server) endpoints(p provider.Provider, model string, from provider.Protocol, relay bool) []provider.Protocol {
	if out := s.pick(p, model, from, relay, true); len(out) > 0 {
		return out
	}
	// every API the provider has turned the model away on: a stale memory
	// must not strand it, so the APIs are tried again
	return s.pick(p, model, from, relay, false)
}

func (s *Server) pick(p provider.Provider, model string, from provider.Protocol, relay, honor bool) []provider.Protocol {
	ok := func(proto provider.Protocol) bool {
		if p.Base(proto) == "" || !listed(p, model, proto) {
			return false
		}
		return !honor || !s.isRefused(p.ID, model, proto)
	}
	var out []provider.Protocol
	if relay && ok(from) {
		out = append(out, from)
	}
	for _, proto := range translateOrder(p, model) {
		if !slices.Contains(out, proto) && ok(proto) {
			out = append(out, proto)
		}
	}
	return out
}

// listed reports whether the provider's model list allows model over proto.
// A model whose list is unknown is allowed over every API the provider has.
func listed(p provider.Provider, model string, proto provider.Protocol) bool {
	apis := p.APIs(model)
	return apis == nil || slices.Contains(apis, proto)
}

// declared reports whether the provider's model list says model is served
// over proto. An unknown list declares nothing.
func declared(p provider.Provider, model string, proto provider.Protocol) bool {
	return slices.Contains(p.APIs(model), proto)
}

// usable reports whether model may be sent to the provider over proto now.
func (s *Server) usable(p provider.Provider, model string, proto provider.Protocol) bool {
	return p.Base(proto) != "" && listed(p, model, proto) && !s.isRefused(p.ID, model, proto)
}

// serveEndpoints sends the request to each API in turn. The next API is
// tried only when the provider turns the model away on this one; nothing
// reaches the client before that is settled, so a refusal leaks no bytes.
// The last API's reply, error or not, is delivered as it came.
func (s *Server) serveEndpoints(w http.ResponseWriter, r *http.Request, p provider.Provider, from provider.Protocol, relay bool, protos []provider.Protocol, model string, body []byte, call *Call) (int, string) {
	var request *Request
	stream := false
	for i, to := range protos {
		last := i == len(protos)-1
		call.To = to
		pass := relay && to == from
		var res *http.Response
		var err error
		if pass {
			res, err = s.sendPassthrough(r.Context(), p, to, model, body, r.Header)
		} else {
			if request == nil {
				req, perr := parse(from, body)
				if perr != nil {
					return writeError(w, from, 400, perr.Error()), perr.Error()
				}
				request, stream = req, req.Stream
				request.Stream = true
			}
			res, err = s.sendTranslated(r.Context(), p, to, request, model, r.Header)
		}
		if err != nil {
			return writeError(w, from, 502, p.Name+": "+err.Error()), err.Error()
		}
		if res.StatusCode >= 400 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			if !last && endpointRefused(res.StatusCode, b) {
				s.markRefused(p.ID, model, to)
				continue
			}
			res.Body = io.NopCloser(bytes.NewReader(b))
		}
		defer res.Body.Close()
		if pass {
			return s.replyPassthrough(w, res, p, to, &call.Usage)
		}
		return s.replyTranslated(w, res, p, from, to, request, stream, &call.Usage)
	}
	msg := p.Name + " has no endpoint serving " + model
	return writeError(w, from, 502, msg), msg
}

// sendTranslated sends one translated, streaming request to proto.
func (s *Server) sendTranslated(ctx context.Context, p provider.Provider, to provider.Protocol, req *Request, model string, in http.Header) (*http.Response, error) {
	body := build(to, req, model, p.Host(), p.RejectsTemperature(model))
	return s.forward(ctx, p, to, pathOf(to), p.Prepare(body), in)
}
