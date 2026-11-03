package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// Account providers ride on a login another agent holds. Every upstream call
// reads that login afresh, so nothing here is stored by dial.

// target works out where one upstream request goes, what proves the caller,
// and the body to send. base is the URL prefix the path is joined to.
func target(ctx context.Context, p provider.Provider, to provider.Protocol, body []byte) (base string, auth map[string]string, out []byte, err error) {
	switch p.ID {
	case provider.CodexID:
		tok, acct, err := provider.CodexCredentials(ctx)
		if err != nil {
			return "", nil, nil, err
		}
		return p.Base(to), map[string]string{"Authorization": "Bearer " + tok, "chatgpt-account-id": acct}, codexBody(body), nil
	case provider.CopilotID:
		tok, api, err := provider.CopilotSession(ctx)
		if err != nil {
			return "", nil, nil, err
		}
		h := provider.CopilotHeaders(tok)
		for k, v := range copilotHeaders(body, to) {
			h[k] = v
		}
		return api, h, body, nil
	}
	return p.Base(to), provider.AuthHeaders(p, to), body, nil
}

// codexBody shapes a Responses request for the ChatGPT Codex backend: it only
// streams, does not store, takes no sampling or output-length fields, and
// wants its input as a list of items.
func codexBody(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return body
	}
	if model, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(model, "codex/")
	}
	m["stream"] = true
	m["store"] = false
	delete(m, "max_output_tokens")
	delete(m, "temperature")
	delete(m, "top_p")
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": s}},
		}}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// copilotHeaders marks a Copilot request as the user's own turn or as the
// reply to a tool, which Copilot bills differently, and flags requests that
// carry images as vision requests.
func copilotHeaders(body []byte, to provider.Protocol) map[string]string {
	h := map[string]string{"X-Initiator": "user"}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return h
	}
	if lastIsToolResult(m, to) {
		h["X-Initiator"] = "agent"
	}
	if hasImage(m["messages"]) || hasImage(m["input"]) {
		h["Copilot-Vision-Request"] = "true"
	}
	return h
}

// lastIsToolResult reports whether the newest item of a request is a tool's
// result rather than something the user typed.
func lastIsToolResult(m map[string]any, to provider.Protocol) bool {
	if to == provider.Responses {
		items, _ := m["input"].([]any)
		if len(items) == 0 {
			return false
		}
		last, _ := items[len(items)-1].(map[string]any)
		t, _ := last["type"].(string)
		return t == "function_call_output"
	}
	msgs, _ := m["messages"].([]any)
	if len(msgs) == 0 {
		return false
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if role, _ := last["role"].(string); role == "tool" {
		return true
	}
	parts, _ := last["content"].([]any)
	for _, c := range parts {
		if part, ok := c.(map[string]any); ok && part["type"] == "tool_result" {
			return true
		}
	}
	return false
}

// hasImage looks for an image part anywhere in a list of messages or items.
func hasImage(x any) bool {
	switch t := x.(type) {
	case map[string]any:
		switch t["type"] {
		case "image_url", "input_image", "image":
			return true
		}
		for _, c := range t {
			if hasImage(c) {
				return true
			}
		}
	case []any:
		for _, c := range t {
			if hasImage(c) {
				return true
			}
		}
	}
	return false
}

// upstreamError is the message for an upstream reply that failed. For an
// account provider only a rejected login gets the sign-in advice; anything
// else keeps the upstream's own words.
func upstreamError(p provider.Provider, status int, b []byte, fallback string) string {
	if provider.IsAccount(p.ID) && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		return provider.SignedOut(p.ID).Error()
	}
	return p.Name + ": " + provider.APIError(b, fallback)
}

// upstreamFailure answers a request the upstream never served. A login that
// was rejected is a 401 the client can act on; anything else is a 502.
func upstreamFailure(w http.ResponseWriter, from provider.Protocol, p provider.Provider, err error) (int, string) {
	status := http.StatusBadGateway
	if provider.IsSignedOut(err) {
		status = http.StatusUnauthorized
	}
	msg := p.Name + ": " + err.Error()
	return writeError(w, from, status, msg), msg
}

// codexWhole answers a non-streaming Responses request. The ChatGPT backend
// only streams, so the stream is read to its end and the final response
// object is the reply; its output is filled from the streamed items when the
// final object left it empty.
func (s *Server) codexWhole(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, body []byte, u *Usage) (int, string) {
	proto := provider.Responses
	res, err := s.forward(r.Context(), p, proto, pathOf(proto), rewriteModel(body, model), r.Header)
	if err != nil {
		return upstreamFailure(w, proto, p, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := upstreamError(p, res.StatusCode, b, res.Status)
		return writeError(w, proto, res.StatusCode, msg), msg
	}
	rd, _ := upstreamStream(res)
	var items []json.RawMessage
	var final json.RawMessage
	var failed string
	readSSE(rd, func(_, data string) error {
		var ev struct {
			Type     string          `json:"type"`
			Item     json.RawMessage `json:"item"`
			Message  string          `json:"message"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		switch ev.Type {
		case "response.output_item.done":
			if len(ev.Item) > 0 {
				items = append(items, ev.Item)
			}
		case "response.completed", "response.incomplete":
			final = ev.Response
		case "response.failed", "error":
			failed = ev.Message
			var fr struct {
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(ev.Response, &fr) == nil && fr.Error != nil && fr.Error.Message != "" {
				failed = fr.Error.Message
			}
		}
		return nil
	})
	if failed != "" {
		msg := p.Name + ": " + failed
		return writeError(w, proto, http.StatusBadGateway, msg), msg
	}
	if final == nil {
		msg := p.Name + ": the stream ended without a finished response"
		return writeError(w, proto, http.StatusBadGateway, msg), msg
	}
	dec := json.NewDecoder(bytes.NewReader(final))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		msg := p.Name + ": the finished response is not JSON"
		return writeError(w, proto, http.StatusBadGateway, msg), msg
	}
	if out, _ := obj["output"].([]any); len(out) == 0 && len(items) > 0 {
		obj["output"] = items
	}
	var fu struct {
		Usage rUsage `json:"usage"`
	}
	json.Unmarshal(final, &fu)
	u.add(fu.Usage.usage())
	out, err := json.Marshal(obj)
	if err != nil {
		msg := p.Name + ": " + err.Error()
		return writeError(w, proto, http.StatusBadGateway, msg), msg
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(out)
	return http.StatusOK, ""
}

// wantsStream reports whether a client request asked for a stream.
func wantsStream(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	return v.Stream
}
