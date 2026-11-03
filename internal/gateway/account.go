package gateway

// Requests to agent-account providers (codex, copilot). The sign-in is put on
// by provider.AccountUpstream; this file reshapes what dial sends and reads
// what comes back, for the parts that differ from an ordinary provider.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

const (
	codexID   = "codex"
	copilotID = "copilot"
)

// codexDrop are the fields the ChatGPT Codex backend refuses: sampling
// settings and output-length caps.
var codexDrop = []string{
	"max_output_tokens", "max_tokens", "max_completion_tokens",
	"temperature", "top_p", "presence_penalty", "frequency_penalty",
	"seed", "stop", "n",
}

// codexBody makes a Responses body the Codex backend accepts: the model
// without its codex/ prefix, streaming on, no storage, no refused fields, and
// a bare string input turned into a list of items.
func codexBody(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return body
	}
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, codexID+"/")
	}
	m["stream"] = true
	m["store"] = false
	for _, k := range codexDrop {
		delete(m, k)
	}
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{"role": "user", "content": s}}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// copilotTurn reads a Chat Completions body and reports whether its last turn
// is a tool result (billed as the agent's turn, not the user's) and whether
// any message carries an image.
func copilotTurn(body []byte) (toolReturn, hasImage bool) {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil {
		return false, false
	}
	if n := len(v.Messages); n > 0 {
		toolReturn = v.Messages[n-1].Role == "tool"
	}
	for _, m := range v.Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) == nil {
			for _, p := range parts {
				if p.Type == "image_url" {
					hasImage = true
				}
			}
		}
	}
	return toolReturn, hasImage
}

// wantsStream reports whether a client body asked for a streamed reply.
func wantsStream(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	return v.Stream
}

// bodyReader pairs a buffered reader with the body it wraps, so bytes read
// ahead for sniffing are not lost.
type bodyReader struct {
	io.Reader
	io.Closer
}

// sniffEventStream gives a reply without a Content-Type the event-stream type
// when its body opens like one. The ChatGPT backend sometimes sends none.
func sniffEventStream(res *http.Response) {
	if res.Header.Get("Content-Type") != "" {
		return
	}
	br := bufio.NewReader(res.Body)
	head, _ := br.Peek(6)
	res.Body = bodyReader{Reader: br, Closer: res.Body}
	head = bytes.TrimLeft(head, " \t\r\n")
	if bytes.HasPrefix(head, []byte("event:")) || bytes.HasPrefix(head, []byte("data:")) {
		res.Header.Set("Content-Type", "text/event-stream")
	}
}

// aggregate serves a non-streaming Responses request from Codex. The backend
// only streams, so the stream is read to its terminal event and the final
// response object is returned as one JSON document.
func (s *Server) aggregate(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, body []byte, u *Usage) (int, string) {
	res, err := s.forward(r.Context(), p, provider.Responses, pathOf(provider.Responses), rewriteModel(body, model), r.Header)
	if err != nil {
		return writeError(w, provider.Responses, 502, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, provider.Responses, res.StatusCode, msg), msg
	}
	var final json.RawMessage
	var failed string
	readSSE(res.Body, func(_, data string) error {
		var ev struct {
			Type     string          `json:"type"`
			Message  string          `json:"message"`
			Response json.RawMessage `json:"response"`
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		switch ev.Type {
		case "response.completed", "response.incomplete", "response.failed":
			var rr struct {
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
				Usage rUsage `json:"usage"`
			}
			json.Unmarshal(ev.Response, &rr)
			if rr.Error != nil && rr.Error.Message != "" {
				failed = rr.Error.Message
			}
			u.add(rr.Usage.usage())
			final = ev.Response
		case "error":
			failed = ev.Message
			if ev.Error != nil && ev.Error.Message != "" {
				failed = ev.Error.Message
			}
		}
		return nil
	})
	if failed != "" {
		msg := p.Name + ": " + failed
		return writeError(w, provider.Responses, 502, msg), msg
	}
	if len(final) == 0 {
		msg := p.Name + ": the stream ended without a response"
		return writeError(w, provider.Responses, 502, msg), msg
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(final)
	return 200, ""
}
