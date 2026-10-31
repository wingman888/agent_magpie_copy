package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// sniffEventStream names the content type of a reply that came without one:
// the ChatGPT backend streams events but never says so. The first bytes
// decide; none are lost.
func sniffEventStream(res *http.Response) {
	if res.Header.Get("Content-Type") != "" || res.Body == nil || res.StatusCode >= 400 {
		return
	}
	br := bufio.NewReaderSize(res.Body, 4096)
	sse := false
	for n := 1; n <= 4096; n++ {
		b, err := br.Peek(n)
		if err != nil {
			break
		}
		t := bytes.TrimLeft(b, " \t\r\n\xef\xbb\xbf")
		if bytes.HasPrefix(t, []byte("data:")) || bytes.HasPrefix(t, []byte("event:")) {
			sse = true
			break
		}
		if len(t) >= 6 {
			break
		}
	}
	res.Body = struct {
		io.Reader
		io.Closer
	}{br, res.Body}
	if sse {
		res.Header.Set("Content-Type", "text/event-stream")
	}
}

func wantsStream(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	return v.Stream
}

// collectResponses reads a Responses event stream to its end and answers
// with the single JSON object a non-streaming client expects.
func collectResponses(w http.ResponseWriter, body io.Reader, model string, u *Usage) (int, string) {
	var (
		final   map[string]any
		created map[string]any
		text    strings.Builder
		items   = map[int]any{}
		failed  string
	)
	readSSE(body, func(_, data string) error {
		var ev map[string]any
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		typ, _ := ev["type"].(string)
		resp, _ := ev["response"].(map[string]any)
		switch typ {
		case "response.created":
			created = resp
		case "response.completed", "response.incomplete":
			final = resp
		case "response.output_text.delta":
			if d, ok := ev["delta"].(string); ok {
				text.WriteString(d)
			}
		case "response.output_item.done":
			idx := len(items)
			if f, ok := ev["output_index"].(float64); ok {
				idx = int(f)
			}
			items[idx] = ev["item"]
		case "response.failed", "error":
			failed = "the backend reported an error"
			if resp != nil {
				if e, ok := resp["error"].(map[string]any); ok {
					if m, _ := e["message"].(string); m != "" {
						failed = m
					}
				}
			}
			if m, _ := ev["message"].(string); m != "" {
				failed = m
			}
		}
		return nil
	})
	if final == nil {
		if failed != "" {
			return writeError(w, "responses", 502, failed), failed
		}
		final = map[string]any{}
		for k, v := range created {
			final[k] = v
		}
		final["status"] = "completed"
	}
	final["object"] = "response"
	if _, ok := final["id"]; !ok {
		final["id"] = "resp_dial"
	}
	if _, ok := final["created_at"]; !ok {
		final["created_at"] = time.Now().Unix()
	}
	if _, ok := final["model"]; !ok {
		final["model"] = model
	}
	if out, _ := final["output"].([]any); len(out) == 0 {
		if len(items) > 0 {
			idx := make([]int, 0, len(items))
			for i := range items {
				idx = append(idx, i)
			}
			sort.Ints(idx)
			var o []any
			for _, i := range idx {
				o = append(o, items[i])
			}
			final["output"] = o
		} else {
			final["output"] = []any{map[string]any{"type": "message", "id": "msg_dial", "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}}}}
		}
	}
	if us, ok := final["usage"]; ok {
		b, _ := json.Marshal(map[string]any{"usage": us})
		sn := newSniffer("responses", "text/event-stream")
		sn.write(append([]byte("data: "), append(b, '\n')...))
		u.add(sn.usage())
	}
	writeJSON(w, 200, final)
	return 200, failed
}
