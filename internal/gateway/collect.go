package gateway

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// wantsStream reports whether the client asked for a streamed reply.
func wantsStream(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	return v.Stream
}

// collectResponses reads a Responses event stream to its end and answers
// the client with the single response object it describes. The final
// response.completed event carries it; when its output is empty (the
// ChatGPT backend does that) the output is rebuilt from the items and text
// deltas seen along the way.
func (s *Server) collectResponses(w http.ResponseWriter, p provider.Provider, res *http.Response, u *Usage) (int, string) {
	sniff := newSniffer(provider.Responses, "text/event-stream")
	var final map[string]any
	var failed string
	items := map[int]json.RawMessage{}
	var text strings.Builder
	readSSE(&teeReader{r: res.Body, fn: sniff.write}, func(event, data string) error {
		var ev map[string]json.RawMessage
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		var typ string
		json.Unmarshal(ev["type"], &typ)
		if typ == "" {
			typ = event
		}
		switch typ {
		case "response.completed", "response.incomplete", "response.failed":
			var r map[string]any
			if json.Unmarshal(ev["response"], &r) == nil && r != nil {
				final = r
				if typ == "response.failed" {
					if e, ok := r["error"].(map[string]any); ok {
						failed, _ = e["message"].(string)
					}
					if failed == "" {
						failed = "the backend reported a failed response"
					}
				}
			}
		case "response.output_item.done":
			var idx int
			json.Unmarshal(ev["output_index"], &idx)
			items[idx] = ev["item"]
		case "response.output_text.delta":
			var d string
			json.Unmarshal(ev["delta"], &d)
			text.WriteString(d)
		case "error":
			var e struct {
				Message string `json:"message"`
			}
			json.Unmarshal(ev["error"], &e)
			if e.Message == "" {
				json.Unmarshal(ev["message"], &e.Message)
			}
			if e.Message != "" {
				failed = e.Message
			}
		}
		return nil
	})
	u.add(sniff.usage())
	if final == nil {
		msg := failed
		if msg == "" {
			msg = "the stream ended without a completed response"
		}
		msg = p.Name + ": " + msg
		return writeError(w, provider.Responses, 502, msg), msg
	}
	if out, _ := final["output"].([]any); len(out) == 0 {
		var rebuilt []any
		idxs := make([]int, 0, len(items))
		for i := range items {
			idxs = append(idxs, i)
		}
		sort.Ints(idxs)
		for _, i := range idxs {
			var it any
			if json.Unmarshal(items[i], &it) == nil {
				rebuilt = append(rebuilt, it)
			}
		}
		if len(rebuilt) == 0 && text.Len() > 0 {
			rebuilt = []any{map[string]any{"type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}}}}
		}
		if rebuilt != nil {
			final["output"] = rebuilt
		}
	}
	final["object"] = "response"
	writeJSON(w, 200, final)
	return 200, failed
}

type teeReader struct {
	r  interface{ Read([]byte) (int, error) }
	fn func([]byte)
}

func (t *teeReader) Read(b []byte) (int, error) {
	n, err := t.r.Read(b)
	if n > 0 {
		t.fn(b[:n])
	}
	return n, err
}
