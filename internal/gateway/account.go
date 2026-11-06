package gateway

// Requests for account providers (codex/, copilot/). Their upstreams differ
// from the vendor APIs in three ways: the Codex backend only streams, both
// take a token read from the agent's login on every request, and Copilot
// wants to know whether a turn is the user's or a tool's.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// accountProto is the wire API an account provider speaks upstream.
func accountProto(id string) provider.Protocol {
	if id == provider.Copilot {
		return provider.Chat
	}
	return provider.Responses
}

// accountReply serves a request for an account provider.
func (s *Server) accountReply(w http.ResponseWriter, r *http.Request, p provider.Provider, from provider.Protocol, model string, body []byte, u *Usage) (int, string) {
	to := accountProto(p.ID)
	req, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	stream := req.Stream

	// A client that already speaks the upstream's protocol has its body sent
	// as it was, so no field is lost; otherwise the body is rebuilt for it.
	up := body
	if from != to {
		req.Stream = true
		up = build(to, req, model, p.Host())
	}
	if p.ID == provider.Codex {
		up, err = codexBody(up, model)
	} else {
		up, err = copilotBody(up, model)
	}
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}

	res, err := s.accountSend(r.Context(), p, up)
	if err != nil {
		msg := p.Name + ": " + err.Error()
		return writeError(w, from, 502, msg), msg
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := accountFailure(p, res.StatusCode, res.Status, b)
		return writeError(w, from, res.StatusCode, msg), msg
	}
	stream2, isSSE := sniffStream(res.Header.Get("Content-Type"), res.Body)
	if !isSSE {
		// the event stream is the only form these upstreams answer in; a
		// JSON body here is an error or a shape dial does not know
		b, _ := io.ReadAll(io.LimitReader(stream2, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeError(w, from, 502, msg), msg
	}

	if from == to && stream {
		// Codex's own stream, relayed as it comes
		h := w.Header()
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		sniff := newSniffer(to, "text/event-stream")
		defer func() { u.add(sniff.usage()) }()
		buf := make([]byte, 32<<10)
		for {
			n, err := stream2.Read(buf)
			if n > 0 {
				sniff.write(buf[:n])
				if _, werr := w.Write(buf[:n]); werr != nil {
					return http.StatusOK, ""
				}
				if f != nil {
					f.Flush()
				}
			}
			if err != nil {
				break
			}
		}
		return http.StatusOK, ""
	}

	dec := decoder(to)
	if stream {
		enc := encoder(from, newSSEWriter(w), req.Model)
		var failed string
		readSSE(stream2, func(_, data string) error {
			return dec(data, func(ev Event) {
				switch ev.Kind {
				case KError:
					failed = ev.Text
				case KStart, KUsage:
					u.add(ev.Usage)
				}
				enc.event(ev)
			})
		})
		enc.finish()
		return http.StatusOK, failed
	}
	// the client wants one JSON reply: collect the upstream stream into it
	var col collector
	readSSE(stream2, func(_, data string) error {
		return dec(data, col.add)
	})
	if col.err != "" && len(col.res.Parts) == 0 {
		msg := p.Name + ": " + col.err
		return writeError(w, from, 502, msg), msg
	}
	res2 := col.finish()
	u.add(res2.Usage)
	out := render(from, res2, req.Model)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(out)
	return http.StatusOK, col.err
}

// accountSend sends the request to the account's upstream with a token read
// from the agent's login at that moment.
func (s *Server) accountSend(ctx context.Context, p provider.Provider, body []byte) (*http.Response, error) {
	var url string
	h := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "text/event-stream",
		"User-Agent":   "dial/" + Version,
	}
	switch p.ID {
	case provider.Codex:
		tok, acct, err := provider.CodexAccess(ctx)
		if err != nil {
			return nil, err
		}
		url = provider.CodexBase + "/responses"
		h["Authorization"] = "Bearer " + tok
		h["chatgpt-account-id"] = acct
	default:
		session, api, err := provider.CopilotSession(ctx)
		if err != nil {
			return nil, err
		}
		url = api + "/chat/completions"
		h["Authorization"] = "Bearer " + session
		h["Editor-Version"] = "vscode/1.99.0"
		h["Copilot-Integration-Id"] = "vscode-chat"
		for k, v := range copilotHeaders(body) {
			h[k] = v
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	return s.client.Do(req)
}

// accountFailure is the message for an upstream error reply. A rejected
// login gets the sign-in instruction instead of the raw upstream text.
func accountFailure(p provider.Provider, status int, statusText string, body []byte) string {
	switch {
	case p.ID == provider.Codex && status == http.StatusUnauthorized:
		return provider.ErrCodexSignOut.Error()
	case p.ID == provider.Copilot && (status == http.StatusUnauthorized || status == http.StatusForbidden):
		return provider.ErrCopilotSignIn.Error()
	}
	return p.Name + ": " + provider.APIError(body, statusText)
}

// codexBody makes a Responses body the ChatGPT Codex backend accepts: the
// bare model name, streamed, not stored, no sampling or output-length
// fields, and a plain string input turned into a list of items.
func codexBody(body []byte, model string) ([]byte, error) {
	m, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	m["model"] = strings.TrimPrefix(model, "codex/")
	m["stream"] = true
	m["store"] = false
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		delete(m, k)
	}
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{"role": "user", "content": s}}
	}
	return json.Marshal(m)
}

// copilotBody sets the model and streams the Chat Completions body.
func copilotBody(body []byte, model string) ([]byte, error) {
	m, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	m["model"] = model
	m["stream"] = true
	return json.Marshal(m)
}

// decodeObject reads a JSON object keeping numbers exactly as written.
func decodeObject(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// copilotHeaders marks a Copilot request as the user's own turn or a tool's
// return trip (they are billed differently), and as a vision request when it
// carries an image.
func copilotHeaders(body []byte) map[string]string {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(body, &v)
	h := map[string]string{"X-Initiator": "user"}
	if n := len(v.Messages); n > 0 {
		if role := v.Messages[n-1].Role; role == "tool" || role == "assistant" {
			h["X-Initiator"] = "agent"
		}
	}
	for _, m := range v.Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			if p.Type == "image_url" {
				h["Copilot-Vision-Request"] = "true"
			}
		}
	}
	return h
}

// sniffStream tells an event stream from a plain body by its content type or
// by its first line, since the Codex backend may send no Content-Type. It
// returns the body, unchanged, with the bytes it peeked at put back.
func sniffStream(contentType string, body io.Reader) (io.Reader, bool) {
	br := bufio.NewReader(body)
	var head bytes.Buffer
	for {
		line, err := br.ReadBytes('\n')
		head.Write(line)
		if len(bytes.TrimSpace(line)) > 0 || err != nil {
			break
		}
	}
	first := bytes.TrimSpace(head.Bytes())
	sse := strings.HasPrefix(contentType, "text/event-stream") ||
		bytes.HasPrefix(first, []byte("event:")) ||
		bytes.HasPrefix(first, []byte("data:"))
	return io.MultiReader(bytes.NewReader(head.Bytes()), br), sse
}
