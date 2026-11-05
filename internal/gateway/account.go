package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// This file serves the account providers (codex, copilot): their requests
// go to the agent's own backend with the agent's own login.

// bodyReader is a response body with a reader that may have read ahead.
type bodyReader struct {
	io.Reader
	io.Closer
}

// forwardAccount sends a request to an account provider's backend. The login
// is read anew for each call, and a Codex request is reshaped for that
// backend.
func (s *Server) forwardAccount(ctx context.Context, p provider.Provider, to provider.Protocol, path string, body []byte) (*http.Response, error) {
	up, err := p.AccountUpstream(ctx)
	if err != nil {
		return nil, err
	}
	if p.ID == provider.CodexID {
		if body, err = provider.CodexBody(body, ""); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, up.Base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", "dial/"+Version)
	for k, v := range up.Headers {
		req.Header.Set(k, v)
	}
	if p.ID == provider.CopilotID && to == provider.Chat {
		for k, v := range provider.CopilotHeaders(body) {
			req.Header.Set(k, v)
		}
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized {
		res.Body.Close()
		return nil, p.SignedOut()
	}
	if res.Header.Get("Content-Type") == "" {
		// the Codex backend may leave Content-Type off a stream; a body that
		// opens with an SSE field name is a stream all the same
		br := bufio.NewReader(res.Body)
		head, _ := br.Peek(8)
		if h := strings.TrimLeft(string(head), " \r\n\t"); strings.HasPrefix(h, "event:") || strings.HasPrefix(h, "data:") {
			res.Header.Set("Content-Type", "text/event-stream")
		}
		res.Body = bodyReader{br, res.Body}
	}
	return res, nil
}

// codexResponses serves a Responses request through the Codex backend. A
// streamed ask is relayed as it comes; a plain ask gets the final response
// object back as one JSON document.
func (s *Server) codexResponses(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, body []byte, u *Usage) (int, string) {
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	if v.Stream {
		return s.passthrough(w, r, p, provider.Responses, model, body, u)
	}
	res, err := s.forward(r.Context(), p, provider.Responses, "/responses", rewriteModel(body, model), r.Header)
	if err != nil {
		return writeError(w, provider.Responses, 502, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, provider.Responses, res.StatusCode, msg), msg
	}
	final, err := collectResponse(res.Body)
	if err != nil {
		msg := p.Name + ": " + err.Error()
		return writeError(w, provider.Responses, 502, msg), msg
	}
	var done struct {
		Usage rUsage `json:"usage"`
	}
	if json.Unmarshal(final, &done) == nil {
		u.add(done.Usage.usage())
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(final)
	return http.StatusOK, ""
}

// collectResponse reads a Responses event stream and returns the response
// object of its final event.
func collectResponse(r io.Reader) ([]byte, error) {
	var final json.RawMessage
	var failure string
	err := readSSE(r, func(_, data string) error {
		var ev struct {
			Type     string          `json:"type"`
			Message  string          `json:"message"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		switch ev.Type {
		case "response.completed", "response.incomplete", "response.done":
			final = ev.Response
		case "response.failed":
			var rs struct {
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			failure = "response failed"
			if json.Unmarshal(ev.Response, &rs) == nil && rs.Error != nil && rs.Error.Message != "" {
				failure = rs.Error.Message
			}
		case "error":
			if ev.Message != "" {
				failure = ev.Message
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(final) == 0 {
		if failure == "" {
			failure = "did not finish the response"
		}
		return nil, errors.New(failure)
	}
	return final, nil
}
