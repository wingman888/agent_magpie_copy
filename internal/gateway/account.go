package gateway

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

// account serves a request for an account provider (Codex, Copilot) under the
// agent's own sign-in. A sign-in that is gone or refused answers 401 with a
// message that says to sign in again.
func (s *Server) account(w http.ResponseWriter, r *http.Request, p provider.Provider, from provider.Protocol, model string, body []byte, call *Call) {
	to := p.Speaks()
	if len(to) == 0 {
		call.Status, call.Error = 502, p.Name+" has no endpoint configured"
		writeError(w, from, 502, call.Error)
		return
	}
	if err := p.SignIn(r.Context()); err != nil {
		call.Status, call.Error = 401, err.Error()
		writeError(w, from, 401, err.Error())
		return
	}
	call.To = to[0]
	switch {
	case to[0] == provider.Responses && from == provider.Responses:
		call.Status, call.Error = s.codexResponses(w, r, p, model, body, &call.Usage)
	case from == to[0]:
		call.Status, call.Error = s.passthrough(w, r, p, from, model, body, &call.Usage)
	default:
		call.Status, call.Error = s.translate(w, r, p, from, to[0], model, body, &call.Usage)
	}
}

// accountIDOf is the provider part of a "provider/model" id, or "" for a bare id.
func accountIDOf(model string) string {
	id, _, _ := strings.Cut(model, "/")
	return id
}

// forwardAccount sends a request under an account provider's sign-in. The
// sign-in names the URL and headers. A reply with no Content-Type whose body
// opens like an event stream is marked as one.
func (s *Server) forwardAccount(ctx context.Context, p provider.Provider, to provider.Protocol, body []byte) (*http.Response, error) {
	req, err := p.SignedRequest(ctx, to, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "dial/"+Version)
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized {
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": p.SignedOut(), "type": "authentication_error"}})
		return &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized",
			Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(b))}, nil
	}
	if res.Header.Get("Content-Type") == "" {
		br := bufio.NewReader(res.Body)
		head, _ := br.Peek(6)
		if bytes.HasPrefix(head, []byte("data:")) || bytes.HasPrefix(head, []byte("event:")) {
			res.Header.Set("Content-Type", "text/event-stream")
		}
		res.Body = struct {
			io.Reader
			io.Closer
		}{br, res.Body}
	}
	return res, nil
}

// codexResponses serves a Responses request through the Codex sign-in. The
// backend only streams: a streamed request is relayed as it arrives, and a
// plain one is read to its end and answered with the completed response.
func (s *Server) codexResponses(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, body []byte, u *Usage) (int, string) {
	var q struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &q)
	res, err := s.forwardAccount(r.Context(), p, provider.Responses, rewriteModel(body, model))
	if err != nil {
		msg := p.Name + ": " + err.Error()
		return writeError(w, provider.Responses, 502, msg), msg
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, provider.Responses, res.StatusCode, msg), msg
	}
	if q.Stream {
		return relayResponses(w, res, u)
	}
	return collectResponses(w, res, p, model, u)
}

// relayResponses copies an event stream to the client as it arrives, reading
// the token counts on the way past.
func relayResponses(w http.ResponseWriter, res *http.Response, u *Usage) (int, string) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	sniff := newSniffer(provider.Responses, "text/event-stream")
	defer func() { u.add(sniff.usage()) }()
	buf := make([]byte, 32<<10)
	for {
		n, err := res.Body.Read(buf)
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

// collectResponses reads a whole event stream and answers with one Responses
// object: the backend's own completed response when it carries output, else
// one built from the streamed text.
func collectResponses(w http.ResponseWriter, res *http.Response, p provider.Provider, model string, u *Usage) (int, string) {
	dec := decoder(provider.Responses)
	var col collector
	var final []byte
	readSSE(res.Body, func(_, data string) error {
		if final == nil {
			final = completedResponse(data)
		}
		return dec(data, col.add)
	})
	res2 := col.finish()
	u.add(res2.Usage)
	if col.err != "" && len(res2.Parts) == 0 {
		msg := p.Name + ": " + col.err
		return writeError(w, provider.Responses, 502, msg), col.err
	}
	out := final
	if out == nil {
		out = render(provider.Responses, res2, model)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(out)
	return http.StatusOK, col.err
}

// completedResponse is the response object of a response.completed (or
// response.incomplete) event, marked as a response, when it carries output.
// Nil otherwise, so the reply is then built from the streamed deltas.
func completedResponse(data string) []byte {
	var ev struct {
		Type     string          `json:"type"`
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal([]byte(data), &ev) != nil {
		return nil
	}
	if ev.Type != "response.completed" && ev.Type != "response.incomplete" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(ev.Response))
	dec.UseNumber()
	var obj map[string]any
	if dec.Decode(&obj) != nil || obj == nil {
		return nil
	}
	if out, _ := obj["output"].([]any); len(out) == 0 {
		return nil
	}
	obj["object"] = "response"
	b, err := json.Marshal(obj)
	if err != nil {
		return nil
	}
	return b
}
