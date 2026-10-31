package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"

	"github.com/yetone/dial/internal/provider"
)

// foldResponses turns a Responses event stream back into the single JSON
// object a non-streaming client expects. The finished response of the
// response.completed event is used as it is; when the stream ended without
// one, the output is put together from the text and item events.
func (s *Server) foldResponses(w http.ResponseWriter, p provider.Provider, res *http.Response, u *Usage) (int, string) {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	sn := newSniffer(provider.Responses, "text/event-stream")
	sn.write(raw)
	u.add(sn.usage())

	var final map[string]any
	var created map[string]any
	var failure string
	items := map[int]any{}
	text := map[int]*bytes.Buffer{}
	readSSE(bytes.NewReader(raw), func(_, data string) error {
		var ev map[string]any
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		typ, _ := ev["type"].(string)
		resp, _ := ev["response"].(map[string]any)
		idx := 0
		if f, ok := ev["output_index"].(float64); ok {
			idx = int(f)
		}
		switch typ {
		case "response.created":
			created = resp
		case "response.completed", "response.incomplete":
			if resp != nil {
				final = resp
			}
		case "response.failed":
			if resp != nil {
				final = resp
			}
			failure = errText(resp["error"], "the model failed")
		case "error":
			failure = errText(ev, "the model failed")
		case "response.output_text.delta":
			if d, ok := ev["delta"].(string); ok {
				if text[idx] == nil {
					text[idx] = &bytes.Buffer{}
				}
				text[idx].WriteString(d)
			}
		case "response.output_item.done":
			if it, ok := ev["item"]; ok {
				items[idx] = it
			}
		}
		return nil
	})
	if final == nil || (failure != "" && len(items) == 0 && len(text) == 0) {
		if failure != "" {
			msg := p.Name + ": " + failure
			return writeError(w, provider.Responses, 502, msg), msg
		}
	}
	if final == nil {
		final = map[string]any{}
		for k, v := range created {
			final[k] = v
		}
		final["status"] = "completed"
	}
	final["object"] = "response"
	if out, _ := final["output"].([]any); len(out) == 0 {
		idxs := map[int]bool{}
		for i := range items {
			idxs[i] = true
		}
		for i := range text {
			idxs[i] = true
		}
		var order []int
		for i := range idxs {
			order = append(order, i)
		}
		sort.Ints(order)
		var built []any
		for _, i := range order {
			if it, ok := items[i]; ok {
				built = append(built, it)
				continue
			}
			built = append(built, map[string]any{"type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": text[i].String(), "annotations": []any{}}}})
		}
		if built == nil {
			built = []any{}
		}
		final["output"] = built
	}
	b, err := json.Marshal(final)
	if err != nil {
		return writeError(w, provider.Responses, 502, err.Error()), err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(b)
	return 200, failure
}

func errText(v any, fallback string) string {
	if m, ok := v.(map[string]any); ok {
		if s, _ := m["message"].(string); s != "" {
			return s
		}
		if e, ok := m["error"].(map[string]any); ok {
			if s, _ := e["message"].(string); s != "" {
				return s
			}
		}
	}
	return fallback
}
