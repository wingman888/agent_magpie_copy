package gateway

// The ChatGPT Codex backend takes Responses requests with its own rules:
// it only streams, it does not store, it refuses sampling and output-length
// fields, and it wants input as a list of items. Requests for codex/<model>
// are reshaped here on their way out, and a non-streaming client gets its
// reply collected from the stream.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// codexFields are the fields the Codex backend rejects.
var codexFields = []string{"max_output_tokens", "max_completion_tokens", "max_tokens",
	"temperature", "top_p", "top_k", "presence_penalty", "frequency_penalty", "seed"}

// isCodex is true for the Codex account provider.
func isCodex(p provider.Provider) bool {
	return p.Account != nil && p.ID == provider.CodexID
}

// codexRequestBody reshapes a Responses request for the Codex backend.
func codexRequestBody(body []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, "codex/")
	}
	m["stream"] = true
	m["store"] = false
	for _, k := range codexFields {
		delete(m, k)
	}
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": s}},
		}}
	}
	return json.Marshal(m)
}

// codexResponses serves a Responses client from the Codex account. A
// streaming client gets the upstream stream as it comes; a non-streaming
// one gets the final response object, with its output collected from the
// stream when the final event leaves it out.
func (s *Server) codexResponses(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, body []byte, u *Usage) (int, string) {
	var q struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &q)
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
	rd, isStream := streamBody(res)
	if !isStream {
		// a plain JSON reply: relay it as it is
		b, _ := io.ReadAll(io.LimitReader(rd, 64<<20))
		sniff := newSniffer(provider.Responses, "")
		sniff.write(b)
		u.add(sniff.usage())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.StatusCode)
		w.Write(b)
		return res.StatusCode, ""
	}
	if q.Stream {
		h := w.Header()
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		sniff := newSniffer(provider.Responses, "text/event-stream")
		defer func() { u.add(sniff.usage()) }()
		buf := make([]byte, 32<<10)
		for {
			n, err := rd.Read(buf)
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
	return s.codexCollect(w, rd, p, u)
}

// codexCollect reads a whole Codex stream and answers with one JSON response.
func (s *Server) codexCollect(w http.ResponseWriter, rd io.Reader, p provider.Provider, u *Usage) (int, string) {
	var items []json.RawMessage
	var final json.RawMessage
	var failed string
	readSSE(rd, func(_, data string) error {
		var ev struct {
			Type     string          `json:"type"`
			Item     json.RawMessage `json:"item"`
			Response json.RawMessage `json:"response"`
			Message  string          `json:"message"`
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
			var rf struct {
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(ev.Response, &rf) == nil && rf.Error != nil && rf.Error.Message != "" {
				failed = rf.Error.Message
			}
			if failed == "" {
				failed = "the reply failed"
			}
		}
		return nil
	})
	if failed != "" {
		msg := p.Name + ": " + failed
		return writeError(w, provider.Responses, 502, msg), msg
	}
	if final == nil && len(items) == 0 {
		msg := p.Name + ": the reply ended before it was complete"
		return writeError(w, provider.Responses, 502, msg), msg
	}
	obj := map[string]any{}
	if len(final) > 0 {
		if err := json.Unmarshal(final, &obj); err != nil {
			msg := p.Name + ": " + err.Error()
			return writeError(w, provider.Responses, 502, msg), msg
		}
	} else {
		obj["object"], obj["status"] = "response", "completed"
	}
	if out, ok := obj["output"].([]any); !ok || len(out) == 0 {
		if len(items) > 0 {
			obj["output"] = items
		}
	}
	var rr struct {
		Usage rUsage `json:"usage"`
	}
	if len(final) > 0 {
		json.Unmarshal(final, &rr)
		u.add(rr.Usage.usage())
	}
	b, err := json.Marshal(obj)
	if err != nil {
		msg := p.Name + ": " + err.Error()
		return writeError(w, provider.Responses, 502, msg), msg
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(b)
	return http.StatusOK, ""
}
