package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// Gemini bodies are compact JSON with no trailing newline.
func TestFixR7GeminiJSONHasNoTrailingNewline(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGeminiJSON(rec, 200, map[string]any{"totalTokens": 5})
	if got := rec.Body.String(); got != `{"totalTokens":5}` {
		t.Fatalf("body = %q", got)
	}
	rec = httptest.NewRecorder()
	writeError(rec, provider.Gemini, 404, "nope")
	got := rec.Body.String()
	if strings.HasSuffix(got, "\n") || !strings.Contains(got, `"status":"NOT_FOUND"`) {
		t.Fatalf("gemini error body = %q", got)
	}
}

// The OpenAI-style error keeps the baseline writeJSON encoder, newline included.
func TestFixR7OpenAIErrorKeepsBaselineNewline(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, provider.Chat, 404, "nope")
	if !strings.HasSuffix(rec.Body.String(), "\n") {
		t.Fatalf("chat error body = %q", rec.Body.String())
	}
}

// top_p reaches Anthropic even when temperature is also set.
func TestFixR7AnthropicKeepsTopP(t *testing.T) {
	temp, top := 0.5, 0.9
	var m map[string]any
	if err := json.Unmarshal(buildAnthropic(&Request{Temp: &temp, TopP: &top, MaxTokens: 100}, "claude-x"), &m); err != nil {
		t.Fatal(err)
	}
	if m["top_p"] != 0.9 {
		t.Fatalf("top_p missing: %v", m)
	}
	if _, ok := m["temperature"]; ok {
		t.Fatalf("temperature should yield to top_p: %v", m)
	}
	var m2 map[string]any
	if err := json.Unmarshal(buildAnthropic(&Request{Temp: &temp, MaxTokens: 100}, "claude-x"), &m2); err != nil {
		t.Fatal(err)
	}
	if m2["temperature"] != 0.5 {
		t.Fatalf("temperature-only request changed: %v", m2)
	}
}
