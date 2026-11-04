package provider

import (
	"encoding/json"
	"testing"
)

// A plain string input becomes one user message in the same item shape the
// Responses translator writes, and instructions is present.
func TestAcctPrepareCodexCanonicalInput(t *testing.T) {
	out := prepareCodex([]byte(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":9}`))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	items, ok := m["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input %s", out)
	}
	item := items[0].(map[string]any)
	if item["type"] != "message" || item["role"] != "user" {
		t.Errorf("item %v", item)
	}
	parts := item["content"].([]any)
	part := parts[0].(map[string]any)
	if part["type"] != "input_text" || part["text"] != "hi" {
		t.Errorf("content %v", parts)
	}
	if v, ok := m["instructions"]; !ok || v != "" {
		t.Errorf("instructions %v", v)
	}
	if m["stream"] != true || m["store"] != false {
		t.Errorf("stream/store: %s", out)
	}
	if _, ok := m["max_output_tokens"]; ok {
		t.Errorf("max_output_tokens kept: %s", out)
	}
	if m["model"] != "gpt-5.5" {
		t.Errorf("model %v", m["model"])
	}
}
