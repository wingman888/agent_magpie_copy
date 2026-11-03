package gateway

// Requests served through an account provider (a Codex or Copilot login).
// The account's signer supplies the URL and credentials; this file decides
// which protocol the backend is spoken and how its replies reach the client.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// account serves a request through an account provider. The backend speaks one
// protocol (see provider.Provider.AccountProtocol); a client asking for another
// is translated to it, and one asking for it is relayed as-is.
func (s *Server) account(w http.ResponseWriter, r *http.Request, p provider.Provider, from provider.Protocol, model string, body []byte, u *Usage) (int, string) {
	to := p.AccountProtocol()
	if to == "" {
		msg := p.Name + " cannot be used through this login"
		return writeError(w, from, 502, msg), msg
	}
	if from != to {
		return s.translate(w, r, p, from, to, model, body, u)
	}
	// The Codex backend only streams. A client that wants one JSON reply gets
	// the stream folded into it.
	if to == provider.Responses && !wantsStream(body) {
		return s.codexCollect(w, r, p, model, body, u)
	}
	return s.passthrough(w, r, p, from, model, body, u)
}

// codexCollect answers a non-streamed Responses request through the Codex
// backend. The stream is read to its end; the final response object, or the
// text it carried when there is none, becomes the one reply.
func (s *Server) codexCollect(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, body []byte, u *Usage) (int, string) {
	const from = provider.Responses
	res, err := s.forward(r.Context(), p, from, pathOf(from), rewriteModel(body, model), r.Header)
	if err != nil {
		return transportError(w, from, p, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return replyError(w, from, p, res)
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeError(w, from, 502, msg), msg
	}

	var deltas strings.Builder // the text the stream carried, in order
	var final map[string]any   // the response object of the closing event
	var failed string
	readSSE(res.Body, func(_, data string) error {
		var ev struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta"`
			Message  string          `json:"message"`
			Error    *errorBody      `json:"error"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		switch ev.Type {
		case "response.output_text.delta":
			deltas.WriteString(ev.Delta)
		case "response.completed", "response.incomplete", "response.failed":
			var rsp struct {
				Error *errorBody `json:"error"`
			}
			json.Unmarshal(ev.Response, &rsp)
			switch {
			case rsp.Error != nil && rsp.Error.Message != "":
				failed = rsp.Error.Message
			case ev.Type == "response.failed":
				failed = "the response failed"
			}
			var obj map[string]any
			if json.Unmarshal(ev.Response, &obj) == nil && obj != nil {
				final = obj
			}
		case "error":
			failed = ev.Message
			if ev.Error != nil && ev.Error.Message != "" {
				failed = ev.Error.Message
			}
			if failed == "" {
				failed = "the stream reported an error"
			}
		}
		return nil
	})
	if failed != "" {
		msg := p.Name + ": " + failed
		return writeError(w, from, 502, msg), failed
	}

	var out []byte
	switch {
	case final != nil:
		if usage, ok := final["usage"]; ok && usage != nil {
			var ru rUsage
			if b, err := json.Marshal(usage); err == nil && json.Unmarshal(b, &ru) == nil {
				u.add(ru.usage())
			}
		} else {
			// The backend sent no usage: the reply still carries an all-zero one.
			final["usage"] = map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
		}
		if items, _ := final["output"].([]any); len(items) == 0 && deltas.Len() > 0 {
			final["output"] = []any{map[string]any{
				"id": "msg_" + newID(), "type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": deltas.String(), "annotations": []any{}}},
			}}
		}
		final["object"] = "response"
		out, _ = json.Marshal(final)
	case deltas.Len() > 0:
		out = renderResponses(Result{Parts: []Part{{Kind: Text, Text: deltas.String()}}}, model)
	default:
		msg := p.Name + " ended without a reply"
		return writeError(w, from, 502, msg), msg
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(out)
	return http.StatusOK, ""
}

// errorBody is the error object a stream or response may carry.
type errorBody struct {
	Message string `json:"message"`
}

// wantsStream reads the stream flag of a request body.
func wantsStream(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	return v.Stream
}

// transportError answers a request that never got a reply. A login that is
// gone or refused says so, with the sign-in hint; anything else is a 502.
func transportError(w http.ResponseWriter, from provider.Protocol, p provider.Provider, err error) (int, string) {
	if provider.IsRelogin(err) {
		msg := err.Error()
		return writeError(w, from, http.StatusUnauthorized, msg), msg
	}
	msg := p.Name + ": " + err.Error()
	return writeError(w, from, http.StatusBadGateway, msg), err.Error()
}

// replyError answers a provider reply with an error status. For an account a
// refused token means the login has lapsed, so the answer is 401 asking the
// user to sign in again.
func replyError(w http.ResponseWriter, from provider.Protocol, p provider.Provider, res *http.Response) (int, string) {
	if p.Account != nil && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden) {
		err := provider.ReloginFor(p.Account.Agent)
		return writeError(w, from, http.StatusUnauthorized, err.Error()), err.Error()
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	msg := p.Name + ": " + provider.APIError(b, res.Status)
	return writeError(w, from, res.StatusCode, msg), msg
}

// missingLogin is the error for a model of an account whose login is gone:
// the account provider has vanished with the login, so the answer asks the
// user to sign in again rather than calling the model unknown.
func missingLogin(model string) error {
	agent, _, ok := strings.Cut(model, "/")
	if !ok {
		return nil
	}
	switch agent {
	case "codex", "copilot":
		return provider.ReloginFor(agent)
	}
	return nil
}

// peekBody is a response body whose first bytes were read ahead.
type peekBody struct {
	io.Reader
	io.Closer
}

// sniffStream gives a reply without a Content-Type the type its body shows.
// The Codex backend sends none; an event stream begins with "data:" or
// "event:", so such a reply is marked as one.
func sniffStream(res *http.Response) {
	if res.Header.Get("Content-Type") != "" {
		return
	}
	br := bufio.NewReader(res.Body)
	head, _ := br.Peek(6)
	if bytes.HasPrefix(head, []byte("data:")) || bytes.HasPrefix(head, []byte("event:")) {
		res.Header.Set("Content-Type", "text/event-stream")
	}
	res.Body = peekBody{Reader: br, Closer: res.Body}
}
