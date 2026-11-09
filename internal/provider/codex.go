package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// PrepareBody adapts a request body to the upstream an account provider
// stands for. Other providers get the body back unchanged.
func PrepareBody(p Provider, to Protocol, body []byte) ([]byte, error) {
	if p.IsAccount() && p.ID == CodexID && to == Responses {
		return CodexBody(body)
	}
	return body, nil
}

// CodexBody rewrites a Responses request into the form the ChatGPT Codex
// backend accepts. The backend only streams, does not store conversations,
// and refuses sampling and output-length fields, so those are dropped. A
// plain-string input becomes a one-item list.
func CodexBody(body []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, errors.New("request body is not a JSON object")
	}
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, "codex/")
	}
	m["stream"] = true
	m["store"] = false
	delete(m, "max_output_tokens")
	delete(m, "temperature")
	delete(m, "top_p")
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": s,
			}},
		}}
	}
	return json.Marshal(m)
}

// IsEventStream tells whether an upstream reply is a server-sent event
// stream. When the reply has no Content-Type, the first bytes of the body
// decide: an event stream starts with "event:" or "data:".
func IsEventStream(contentType string, head []byte) bool {
	if strings.HasPrefix(contentType, "text/event-stream") {
		return true
	}
	if contentType != "" {
		return false
	}
	t := bytes.TrimLeft(head, " \t\r\n")
	return bytes.HasPrefix(t, []byte("event:")) || bytes.HasPrefix(t, []byte("data:"))
}

// CollectResponses turns a Responses event stream into the single JSON
// object a non-streaming client expects. It takes the response carried by
// response.completed; when that is missing it assembles the output from the
// finished output items.
func CollectResponses(stream []byte) ([]byte, error) {
	var items []json.RawMessage
	var done json.RawMessage
	var failed string
	for _, line := range bytes.Split(stream, []byte("\n")) {
		rest, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
		if !ok {
			continue
		}
		var ev struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
			Item     json.RawMessage `json:"item"`
			Message  string          `json:"message"`
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(bytes.TrimSpace(rest), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.completed", "response.incomplete":
			done = ev.Response
		case "response.failed", "error":
			failed = ev.Message
			if ev.Error != nil && ev.Error.Message != "" {
				failed = ev.Error.Message
			}
			if failed == "" {
				failed = "Codex failed the response"
			}
		case "response.output_item.done":
			if len(ev.Item) > 0 {
				items = append(items, ev.Item)
			}
		}
	}
	if len(done) > 0 && string(done) != "null" {
		if len(items) == 0 {
			return done, nil
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(done, &obj) != nil {
			return done, nil
		}
		if out, ok := obj["output"]; !ok || string(out) == "null" || string(out) == "[]" {
			obj["output"], _ = json.Marshal(items)
			return json.Marshal(obj)
		}
		return done, nil
	}
	if failed != "" {
		return nil, errors.New(failed)
	}
	if len(items) == 0 {
		return nil, errors.New("Codex ended the stream without a response")
	}
	return json.Marshal(map[string]any{"object": "response", "status": "completed", "output": items})
}
