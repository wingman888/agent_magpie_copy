package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// collectResponses folds a Responses event stream into the one JSON object
// a non-streaming client expects. The completed event carries the response;
// the ChatGPT backend often leaves its output empty there, so the finished
// output items (and, failing those, the text deltas) fill it in.
func collectResponses(w http.ResponseWriter, p provider.Provider, body io.Reader, u *Usage) (int, string) {
	sn := newSniffer(provider.Responses, "text/event-stream")
	var final map[string]any
	var text strings.Builder
	items := map[int]any{}
	var failed string
	readSSE(io.TeeReader(body, snWriter{sn}), func(_, data string) error {
		var ev struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta"`
			Index    int             `json:"output_index"`
			Item     json.RawMessage `json:"item"`
			Response json.RawMessage `json:"response"`
			Message  string          `json:"message"`
			Error    struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		switch ev.Type {
		case "response.output_text.delta":
			text.WriteString(ev.Delta)
		case "response.output_item.done":
			var it any
			if json.Unmarshal(ev.Item, &it) == nil {
				items[ev.Index] = it
			}
		case "response.completed", "response.incomplete":
			var m map[string]any
			if json.Unmarshal(ev.Response, &m) == nil {
				final = m
			}
		case "response.failed":
			var r struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			json.Unmarshal(ev.Response, &r)
			failed = r.Error.Message
			if failed == "" {
				failed = "the response failed"
			}
		case "error":
			failed = ev.Message
			if failed == "" {
				failed = ev.Error.Message
			}
			if failed == "" {
				failed = "the backend reported an error"
			}
		}
		return nil
	})
	u.add(sn.usage())
	if final == nil {
		if failed != "" {
			msg := p.Name + ": " + failed
			return writeError(w, provider.Responses, 502, msg), msg
		}
		if text.Len() == 0 && len(items) == 0 {
			msg := p.Name + " ended the stream without a response"
			return writeError(w, provider.Responses, 502, msg), msg
		}
		final = map[string]any{"id": "resp_dial", "status": "completed"}
	}
	if out, _ := final["output"].([]any); len(out) == 0 {
		var output []any
		if len(items) > 0 {
			idx := make([]int, 0, len(items))
			for i := range items {
				idx = append(idx, i)
			}
			sort.Ints(idx)
			for _, i := range idx {
				output = append(output, items[i])
			}
		} else if text.Len() > 0 {
			output = []any{map[string]any{"type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}}}}
		}
		if output == nil {
			output = []any{}
		}
		final["output"] = output
	}
	final["object"] = "response"
	if _, ok := final["status"]; !ok {
		final["status"] = "completed"
	}
	writeJSON(w, 200, final)
	return 200, failed
}

type snWriter struct{ s *usageSniffer }

func (w snWriter) Write(b []byte) (int, error) {
	w.s.write(b)
	return len(b), nil
}
