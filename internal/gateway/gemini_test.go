package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// gemAPIDecode reads a JSON object, failing the test if it is not one.
func gemAPIDecode(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, body)
	}
	return m
}

// gemAPIGet is a GET against a fresh gateway.
func gemAPIGet(t *testing.T, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code, rec.Body.String()
}

// gemAPIMap walks nested JSON objects and arrays by key or index.
func gemAPIMap(v any, path ...any) any {
	for _, k := range path {
		switch x := k.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[x]
		case int:
			a, ok := v.([]any)
			if !ok || x >= len(a) {
				return nil
			}
			v = a[x]
		}
	}
	return v
}

func TestGemAPI_GenerateNonStreaming(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"Hello"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":" there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":50,"temperature":0.2}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("compact STOP missing: %s", body)
	}
	got := gemAPIDecode(t, body)
	if txt := gemAPIMap(got, "candidates", 0, "content", "parts", 0, "text"); txt != "Hello there" {
		t.Errorf("text = %v", txt)
	}
	if n := gemAPIMap(got, "usageMetadata", "promptTokenCount"); n != float64(7) {
		t.Errorf("promptTokenCount = %v", n)
	}
	up := gemAPIDecode(t, string(f.got))
	msgs := gemAPIMap(up, "messages").([]any)
	if s := gemAPIMap(msgs[0], "content"); s != "be brief" {
		t.Errorf("system message = %#v", msgs[0])
	}
	if up["max_tokens"] != float64(50) || up["temperature"] != 0.2 {
		t.Errorf("generation config not carried: %v", up)
	}
}

func TestGemAPI_StreamOrderAndUsage(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse",
		`{"contents":[{"role":"user","parts":[{"text":"read a.go"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := events(body)
	if len(evs) != 4 {
		t.Fatalf("want 4 events (thought, text, call, final), got %d: %s", len(evs), body)
	}
	if gemAPIMap(evs[0], "candidates", 0, "content", "parts", 0, "thought") != true {
		t.Errorf("first event is not a thought: %v", evs[0])
	}
	if gemAPIMap(evs[1], "candidates", 0, "content", "parts", 0, "text") != "Hi" {
		t.Errorf("second event is not the text: %v", evs[1])
	}
	call := gemAPIMap(evs[2], "candidates", 0, "content", "parts", 0, "functionCall")
	if gemAPIMap(call, "id") != "call_9" || gemAPIMap(call, "name") != "read" || gemAPIMap(call, "args", "path") != "a.go" {
		t.Errorf("function call = %v", call)
	}
	last := evs[3]
	if gemAPIMap(last, "candidates", 0, "finishReason") != "STOP" {
		t.Errorf("final finishReason: %v", last)
	}
	if gemAPIMap(last, "usageMetadata", "thoughtsTokenCount") != float64(2) {
		t.Errorf("thoughtsTokenCount: %v", last)
	}
}

func TestGemAPI_TranslatesRequestForChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[
	    {"role":"user","parts":[{"text":"read a.go"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]},
	    {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"a.go"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}],
	  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
	     "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}}}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
	  "generationConfig":{"thinkingConfig":{"thinkingBudget":-1},"responseMimeType":"application/json",
	     "responseSchema":{"type":"OBJECT"}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	up := gemAPIDecode(t, string(f.got))
	msgs := gemAPIMap(up, "messages").([]any)
	if len(msgs) != 4 {
		t.Fatalf("want system, user, assistant, tool; got %d messages", len(msgs))
	}
	if s, _ := gemAPIMap(msgs[0], "content").(string); !strings.Contains(s, "be brief") || !strings.Contains(s, "JSON") || !strings.Contains(s, `{"type":"object"}`) {
		t.Errorf("system prompt = %q", s)
	}
	if url := gemAPIMap(msgs[1], "content", 1, "image_url", "url"); url != "data:image/png;base64,AAAA" {
		t.Errorf("image url = %v", url)
	}
	callID := gemAPIMap(msgs[2], "tool_calls", 0, "id")
	if callID == nil || callID == "" {
		t.Fatalf("generated call id missing: %v", msgs[2])
	}
	if gemAPIMap(msgs[3], "role") != "tool" || gemAPIMap(msgs[3], "tool_call_id") != callID {
		t.Errorf("tool message does not pair with the call: %v", msgs[3])
	}
	if gemAPIMap(up, "reasoning_effort") != "medium" {
		t.Errorf("reasoning_effort = %v", up["reasoning_effort"])
	}
	if gemAPIMap(up, "tools", 0, "function", "parameters", "type") != "object" ||
		gemAPIMap(up, "tools", 0, "function", "parameters", "properties", "path", "type") != "string" {
		t.Errorf("schema types not lower-cased: %v", up["tools"])
	}
	if gemAPIMap(up, "tool_choice", "function", "name") != "read" {
		t.Errorf("tool_choice = %v", up["tool_choice"])
	}
}

func TestGemAPI_UnknownModelNotFound(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Fatalf("status %d: %s", code, body)
	}
}

func TestGemAPI_UnknownMethodNotFound(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/fake/m1:embedContent", `{}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Fatalf("status %d: %s", code, body)
	}
}

func TestGemAPI_CountTokensBothShapes(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	direct := `{"contents":[{"role":"user","parts":[{"text":"hello there, count these words please"}]}]}`
	wrapped := `{"generateContentRequest":` + direct + `}`
	for _, b := range []string{direct, wrapped} {
		code, body := post(t, "/v1beta/models/fake/m1:countTokens", b)
		if code != 200 || !strings.HasPrefix(body, `{"totalTokens":`) {
			t.Fatalf("status %d: %s", code, body)
		}
		if n := gemAPIMap(gemAPIDecode(t, body), "totalTokens"); n == nil || n.(float64) <= 0 {
			t.Errorf("totalTokens = %v", n)
		}
	}
}

func TestGemAPI_ModelsListUsesGeminiNames(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := gemAPIGet(t, "/v1beta/models")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	models := gemAPIMap(gemAPIDecode(t, body), "models").([]any)
	found := false
	for _, m := range models {
		if gemAPIMap(m, "name") == "models/fake/m1" {
			found = true
			if gemAPIMap(m, "supportedGenerationMethods", 0) == nil {
				t.Errorf("no supportedGenerationMethods: %v", m)
			}
		}
	}
	if !found {
		t.Errorf("models/fake/m1 not listed: %s", body)
	}
}

func TestGemAPI_InfoAndNotFoundMentionGemini(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	if _, body := gemAPIGet(t, "/"); !strings.Contains(body, "/v1beta/models/{model}:generateContent") {
		t.Errorf("info does not list Gemini: %s", body)
	}
	code, body := gemAPIGet(t, "/nope")
	if code != http.StatusNotFound || !strings.Contains(body, "v1beta") {
		t.Errorf("404 text = %d %s", code, body)
	}
}

func TestGemAPI_StreamUpstreamErrorIsGeminiShaped(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, code: 500, reply: `{"error":{"message":"boom"}}`, ctype: "application/json"})
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"x"}]}]}`)
	if code != 500 || !strings.Contains(body, `"status":"INTERNAL"`) {
		t.Fatalf("status %d: %s", code, body)
	}
}
