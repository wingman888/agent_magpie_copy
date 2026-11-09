package gateway

// Endpoints: which wire API a provider's model is sent to. A model is sent
// only to the APIs the provider's model list says serve it, when the list
// says; otherwise to any the provider speaks. The agent's own API is relayed
// as-is when the model serves it, and translated otherwise. An endpoint that
// answers that it doesn't serve the model is remembered, and the request goes
// to the next one; nothing reaches the agent until one answers.

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// endpointsOf lists the APIs a model may be sent to: the ones the provider's
// model list gives it, else every one the provider speaks. Only those with a
// base URL count.
func endpointsOf(p provider.Provider, model string) []provider.Protocol {
	if apis := p.APIs(model); len(apis) > 0 {
		var out []provider.Protocol
		for _, pr := range apis {
			if p.Base(pr) != "" {
				out = append(out, pr)
			}
		}
		return out
	}
	return p.Speaks()
}

// servesOn reports whether a provider's model may be sent to an API, not
// counting one that refused it before.
func (s *Server) servesOn(p provider.Provider, model string, proto provider.Protocol) bool {
	return slices.Contains(s.endpointOrder(p, model, proto, true), proto)
}

// endpointOrder lists the APIs a request for model goes to, in the order they
// are tried. The agent's own API comes first when it may be relayed as-is;
// the others are translations: OpenAI's models over Responses first, the rest
// over Chat, and Anthropic last. An API that refused the model is left out,
// unless that leaves none.
func (s *Server) endpointOrder(p provider.Provider, model string, from provider.Protocol, relay bool) []provider.Protocol {
	all := endpointsOf(p, model)
	var out []provider.Protocol
	for _, pr := range all {
		if !s.notServedBefore(p.ID, model, pr) {
			out = append(out, pr)
		}
	}
	if len(out) == 0 {
		out = all
	}
	var ordered []provider.Protocol
	if relay && slices.Contains(out, from) {
		ordered = append(ordered, from)
	}
	pref := []provider.Protocol{provider.Chat, provider.Responses}
	if responsesFirst(p, model) {
		pref = []provider.Protocol{provider.Responses, provider.Chat}
	}
	pref = append(pref, provider.Anthropic)
	for _, pr := range pref {
		if slices.Contains(out, pr) && !slices.Contains(ordered, pr) {
			ordered = append(ordered, pr)
		}
	}
	return ordered
}

// responsesFirst says a model is translated to Responses before Chat: any
// model on OpenAI's own API, and an OpenAI model on Copilot.
func responsesFirst(p provider.Provider, model string) bool {
	if p.Account != nil && p.Account.Agent == "copilot" {
		return openAIModel(model)
	}
	for _, pr := range provider.Protocols {
		if hostIs(p.Base(pr), "api.openai.com") {
			return true
		}
	}
	return false
}

// openAIModel recognizes OpenAI's model names (gpt-*, codex*, o<digit>…),
// ignoring a vendor/ prefix. gpt-oss is an open model of another vendor.
func openAIModel(model string) bool {
	name := strings.ToLower(model[strings.LastIndex(model, "/")+1:])
	if strings.HasPrefix(name, "gpt-oss") {
		return false
	}
	return strings.HasPrefix(name, "gpt-") || strings.HasPrefix(name, "codex") || openO.MatchString(name)
}

var openO = regexp.MustCompile(`^o\d`)

// hostIs reports whether a base URL's host is name, whatever its port.
func hostIs(u, name string) bool {
	h := provider.HostOf(u)
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	return h == name
}

// notServedWords match the errors upstreams answer with when a model is
// reached on the wrong endpoint.
var notServedWords = regexp.MustCompile(`not accessible via the .{0,40}endpoint|unsupported_api_for_model`)

// notChatModel says a reply is a client error that the endpoint doesn't serve
// the model; a request is then sent to the provider's next API. (The name is
// from when only Chat's refusal was retried over Responses.)
func notChatModel(status int, body []byte) bool {
	if status < 400 || status >= 500 {
		return false
	}
	msg := strings.ToLower(string(body))
	for _, phrase := range []string{
		"not a chat model",
		"not supported in the v1/chat/completions",
		"not supported in /v1/chat/completions",
		"not supported in the v1/responses",
		"not supported in /v1/responses",
		"use v1/chat/completions",
		"use /v1/chat/completions",
		"use v1/completions",
		"use /v1/completions",
		"use v1/responses",
		"use /v1/responses",
	} {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return notServedWords.MatchString(msg)
}

// notServedKey names one endpoint's refusal of one model on one provider.
func notServedKey(providerID, model string, proto provider.Protocol) string {
	return providerID + "\x00" + model + "\x00" + string(proto)
}

// notServedBefore reports whether an endpoint refused a model on a provider.
func (s *Server) notServedBefore(providerID, model string, proto provider.Protocol) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused[notServedKey(providerID, model, proto)]
}

// markNotServed remembers that an endpoint refused a model on a provider.
func (s *Server) markNotServed(providerID, model string, proto provider.Protocol) {
	s.mu.Lock()
	if s.refused == nil { // a Server made without New
		s.refused = map[string]bool{}
	}
	s.refused[notServedKey(providerID, model, proto)] = true
	s.mu.Unlock()
}

// serveEndpoints sends a request for model to the provider, over the APIs
// endpointOrder gives. An API that refuses the model is remembered and the
// next one tried, as long as no reply has gone to the agent; any other answer
// is the reply. Relayed replies are not translated; the others are.
func (s *Server) serveEndpoints(w http.ResponseWriter, r *http.Request, from provider.Protocol, p provider.Provider, model string, body []byte, relay bool, call *Call) (int, string) {
	protos := s.endpointOrder(p, model, from, relay)
	if len(protos) == 0 {
		msg := p.Name + " has no endpoint configured"
		if len(p.Speaks()) > 0 {
			msg = p.Name + " has no endpoint for " + model
		}
		return writeError(w, from, 502, msg), msg
	}
	var req *Request // the agent's request, parsed once when translating
	var stream bool  // whether the agent asked for a stream
	for i, to := range protos {
		last := i == len(protos)-1
		call.To = to
		relayed := to == from && relay
		var res *http.Response
		var err error
		if relayed {
			rewritten := rewriteModel(body, model)
			if to == provider.Chat {
				rewritten = developerAsSystem(rewritten)
			}
			res, err = s.forward(r.Context(), p, to, pathOf(to), p.Prepare(rewritten), r.Header)
		} else {
			if req == nil {
				parsed, perr := parse(from, body)
				if perr != nil {
					return writeError(w, from, 400, perr.Error()), perr.Error()
				}
				req, stream = parsed, parsed.Stream
				req.Stream = true // the provider is always streamed
			}
			out := build(to, req, model, p.Host(), p.RejectsTemperature(model))
			res, err = s.forward(r.Context(), p, to, pathOf(to), p.Prepare(out), r.Header)
		}
		if err != nil {
			return writeError(w, from, 502, p.Name+": "+err.Error()), err.Error()
		}
		if res.StatusCode >= 400 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			if notChatModel(res.StatusCode, b) {
				s.markNotServed(p.ID, model, to)
				if !last {
					continue // nothing has gone to the agent yet
				}
			}
			msg := p.Name + ": " + provider.APIError(b, res.Status)
			keepRetry(w.Header(), res.Header)
			return writeError(w, from, res.StatusCode, msg), msg
		}
		if relayed {
			return relayReply(w, res, to, &call.Usage)
		}
		defer res.Body.Close()
		return deliverTranslated(w, p, from, to, req, stream, res, &call.Usage)
	}
	return writeError(w, from, 502, p.Name+" has no endpoint for "+model), p.Name + " has no endpoint for " + model
}

// relayReply passes a successful reply the provider speaks as-is to the
// agent, the token counts it carries read on the way past into u.
func relayReply(w http.ResponseWriter, res *http.Response, proto provider.Protocol, u *Usage) (int, string) {
	defer res.Body.Close()
	rd, sse := eventStream(res)
	h := w.Header()
	for _, k := range []string{"Content-Type", "Request-Id", "X-Request-Id"} {
		if v := res.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	if sse {
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(res.StatusCode)
	f, _ := w.(http.Flusher)
	sniff := newSniffer(proto, res.Header.Get("Content-Type"))
	defer func() { u.add(sniff.usage()) }()
	g, _ := w.(*holdWriter)
	// a stream that fails before its first content is another provider's
	// to answer: none of it goes to the agent
	var pre bytes.Buffer
	if sse && res.StatusCode < 400 {
		if fail := awaitContent(rd, proto, &pre); fail != nil && g != nil {
			g.failStream(fail.status, errorPayload(proto, fail.status, fail.msg))
			return fail.status, fail.msg
		}
	}
	g.commit()
	if pre.Len() > 0 {
		sniff.write(pre.Bytes())
		if _, werr := w.Write(pre.Bytes()); werr != nil {
			return res.StatusCode, ""
		}
		if f != nil {
			f.Flush()
		}
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			sniff.write(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return res.StatusCode, ""
			}
			if f != nil {
				f.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	return res.StatusCode, ""
}

// deliverTranslated writes a reply the provider gave in to's API as from's,
// the token counts it carries read on the way past into u.
func deliverTranslated(w http.ResponseWriter, p provider.Provider, from, to provider.Protocol, req *Request, stream bool, res *http.Response, u *Usage) (int, string) {
	dec := decoder(to)
	rd, sse := eventStream(res)
	if !sse {
		// the provider ignored stream:true; read the whole reply as one
		// event stream would be wrong, so give up cleanly
		b, _ := io.ReadAll(io.LimitReader(rd, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeError(w, from, 502, msg), msg
	}
	if stream {
		// until its first content, the reply waits in w: a stream that fails
		// before then is another provider's to answer, and nothing of it is
		// sent on
		g, _ := w.(*holdWriter)
		enc := encoder(from, newSSEWriter(w), req.Model)
		var failed string
		var fail *streamFail
		readSSE(rd, func(_, data string) error {
			err := dec(data, func(ev Event) {
				if fail != nil {
					return
				}
				switch ev.Kind {
				case KError:
					failed = ev.Text
					if st := eventStatus(ev); g != nil && !g.passing && retryable(st, []byte(ev.Text)) {
						fail = &streamFail{st, ev.Text}
						return
					}
				case KStart, KUsage:
					u.add(ev.Usage)
				}
				enc.event(ev)
				if isContent(ev.Kind) || ev.Kind == KError {
					g.commit()
				}
			})
			if fail != nil {
				return errPeek
			}
			return err
		})
		if fail != nil && g != nil {
			g.failStream(fail.status, errorPayload(from, fail.status, fail.msg))
			return fail.status, fail.msg
		}
		enc.finish()
		return 200, failed
	}
	var col collector
	readSSE(rd, func(_, data string) error {
		return dec(data, col.add)
	})
	if col.err != "" && len(col.res.Parts) == 0 {
		return writeError(w, from, 502, p.Name+": "+col.err), col.err
	}
	res2 := col.finish()
	u.add(res2.Usage)
	out := render(from, res2, req.Model)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(out)
	return 200, col.err
}
