package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// Signed-in agent accounts (Codex, Copilot) are providers with no base URL:
// the agent's own credentials, read by provider.Sign on every request, say
// where and how to send the request.

// accountHint is how the client is told to recover a lapsed sign-in.
var accountHint = map[string]string{
	"codex":   "请重新登录（运行 codex login）",
	"copilot": "请重新登录（在编辑器中重新登录 GitHub Copilot）",
}

// authError marks a failure to sign in or a token the upstream refused. It
// answers 401 so the client knows to ask the user to sign in again.
type authError struct{ err error }

func (e authError) Error() string { return e.err.Error() }
func (e authError) Unwrap() error { return e.err }

// failStatus is the HTTP status for a failed upstream call.
func failStatus(err error) int {
	var ae authError
	if errors.As(err, &ae) {
		return http.StatusUnauthorized
	}
	return http.StatusBadGateway
}

// resolveAccount maps "codex/<model>" or "copilot/<model>" to the signed-in
// account. A provider the user built under the same id wins over the account.
func resolveAccount(id string) (provider.Provider, string, bool) {
	pid, model, ok := strings.Cut(strings.TrimSpace(id), "/")
	if !ok || model == "" {
		return provider.Provider{}, "", false
	}
	for _, a := range provider.Accounts() {
		if a.ID != pid {
			continue
		}
		if own, err := provider.Find(pid); err == nil && own.Account == nil {
			return provider.Provider{}, "", false
		}
		return a, model, true
	}
	return provider.Provider{}, "", false
}

// accountProto is the one protocol an account's upstream speaks.
func accountProto(agent string) provider.Protocol {
	if agent == "codex" {
		return provider.Responses
	}
	return provider.Chat
}

// serveAccount answers a request sent to a signed-in account. Requests in the
// account's own protocol go through untouched (codex non-streaming is
// collected into one reply); anything else is translated.
func (s *Server) serveAccount(w http.ResponseWriter, r *http.Request, p provider.Provider, from provider.Protocol, model string, body []byte, u *Usage) (provider.Protocol, int, string) {
	to := accountProto(p.Account.Agent)
	stream := wantsStream(body)
	switch {
	case from == to && (to == provider.Chat || stream):
		st, msg := s.passthrough(w, r, p, from, model, body, u)
		return to, st, msg
	case from == to:
		st, msg := s.codexOnce(w, r, p, model, body, u)
		return to, st, msg
	}
	st, msg := s.translate(w, r, p, from, to, model, body, u)
	return to, st, msg
}

// jsonObject parses a JSON object, keeping numbers exactly as written.
func jsonObject(b []byte) (map[string]any, bool) {
	m := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

func wantsStream(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	return v.Stream
}

// forwardAccount sends a request to a signed-in account. Prepare adjusts the
// body to what the upstream requires and Sign attaches the agent's token.
func (s *Server) forwardAccount(ctx context.Context, p provider.Provider, to provider.Protocol, body []byte) (*http.Response, error) {
	req := (&http.Request{Method: http.MethodPost, Header: http.Header{}}).WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream, application/json")
	req.Header.Set("User-Agent", "dial/"+Version)
	if err := p.Sign(ctx, req, to, p.Prepare(body)); err != nil {
		return nil, authError{err}
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		return nil, authError{fmt.Errorf("令牌被拒绝（HTTP %d：%s），%s", res.StatusCode,
			provider.APIError(b, res.Status), accountHint[p.Account.Agent])}
	}
	sniffStream(res)
	return res, nil
}

// sniffStream tells an event stream from a reply that left out its
// Content-Type (the ChatGPT backend does): a body opening with data: or
// event: is a stream, and the reply is labelled as one.
func sniffStream(res *http.Response) {
	if res.Header.Get("Content-Type") != "" {
		return
	}
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

// codexOnce serves a non-streaming Responses request to Codex. The backend
// only streams, so the stream is read to its end and the response.completed
// object is returned as one JSON reply.
func (s *Server) codexOnce(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, body []byte, u *Usage) (int, string) {
	res, err := s.forward(r.Context(), p, provider.Responses, "/responses", rewriteModel(body, model), r.Header)
	if err != nil {
		msg := p.Name + ": " + err.Error()
		return writeError(w, provider.Responses, failStatus(err), msg), msg
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, provider.Responses, res.StatusCode, msg), msg
	}
	var done json.RawMessage
	var col collector
	dec := (&responsesDecoder{}).decode
	readSSE(res.Body, func(_, data string) error {
		var head struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal([]byte(data), &head) == nil {
			switch head.Type {
			case "response.completed", "response.done", "response.incomplete":
				if len(head.Response) > 0 {
					done = head.Response
				}
			}
		}
		return dec(data, col.add)
	})
	if done != nil {
		var v struct {
			Usage *rUsage `json:"usage"`
		}
		if json.Unmarshal(done, &v) == nil && v.Usage != nil {
			u.add(v.Usage.usage())
		}
		if m, ok := jsonObject(done); ok {
			m["object"] = "response"
			if b, err := json.Marshal(m); err == nil {
				done = b
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(done)
		return http.StatusOK, ""
	}
	if col.err != "" && len(col.res.Parts) == 0 {
		msg := p.Name + ": " + col.err
		return writeError(w, provider.Responses, http.StatusBadGateway, msg), col.err
	}
	res2 := col.finish()
	u.add(res2.Usage)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(renderResponses(res2, model))
	return http.StatusOK, col.err
}
