package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// geminiPost sends a Gemini call to the gateway and returns the reply.
func geminiPost(t *testing.T, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// partsOf is the parts of the first candidate of a Gemini response or event.
func partsOf(ev map[string]any) []any {
	cands, _ := ev["candidates"].([]any)
	if len(cands) == 0 {
		return nil
	}
	c, _ := cands[0].(map[string]any)
	content, _ := c["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	return parts
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("not an object: %#v", v)
	}
	return m
}

func TestGeminiGenerateContentText(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
	)}
	setup(t, provider.Chat, f)
	code, body := geminiPost(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("no compact STOP: %s", body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%v %s", err, body)
	}
	parts := partsOf(out)
	if len(parts) != 1 || asMap(t, parts[0])["text"] != "Hi there" {
		t.Errorf("parts: %s", body)
	}
	u := asMap(t, out["usageMetadata"])
	if u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(5) || u["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage: %v", u)
	}
	if out["modelVersion"] != "fake/m1" {
		t.Errorf("modelVersion: %v", out["modelVersion"])
	}
}

func TestGeminiStreamOrderAndToolCall(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c2","model":"m1","choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"content":"Let me look."}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"a.go\"}"}}]}}]}`,
		`data: {"id":"c2","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c2","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":9,"completion_tokens_details":{"reasoning_tokens":7}}}`,
	)}
	setup(t, provider.Chat, f)
	code, body := geminiPost(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"read"}]}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	evs := events(body)
	if len(evs) != 4 {
		t.Fatalf("want 4 events, got %d: %s", len(evs), body)
	}
	think := asMap(t, partsOf(evs[0])[0])
	if think["text"] != "think" || think["thought"] != true {
		t.Errorf("thought first: %v", think)
	}
	if asMap(t, partsOf(evs[1])[0])["text"] != "Let me look." {
		t.Errorf("text second: %v", evs[1])
	}
	call := asMap(t, asMap(t, partsOf(evs[2])[0])["functionCall"])
	args := asMap(t, call["args"])
	if call["id"] != "call_9" || call["name"] != "read" || args["path"] != "a.go" {
		t.Errorf("call: %v", call)
	}
	last := evs[3]
	c0 := asMap(t, last["candidates"].([]any)[0])
	if c0["finishReason"] != "STOP" {
		t.Errorf("last finishReason: %v", c0)
	}
	u := asMap(t, last["usageMetadata"])
	if u["promptTokenCount"] != float64(20) || u["thoughtsTokenCount"] != float64(7) {
		t.Errorf("usage: %v", u)
	}
}

func TestGeminiRequestTranslation(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c3","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c3","choices":[{"delta":{},"finish_reason":"stop"}]}`,
	)}
	setup(t, provider.Chat, f)
	body := `{
		"systemInstruction":{"parts":[{"text":"be brief"}]},
		"contents":[
			{"role":"user","parts":[{"text":"read a.go"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}},{"inlineData":{"mimeType":"application/pdf","data":"BBBB"}}]},
			{"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"a.go"}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package a"}}}]}
		],
		"tools":[{"functionDeclarations":[{"name":"read","description":"read a file","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
		"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
		"generationConfig":{"maxOutputTokens":100,"temperature":0.5,"topP":0.9,"stopSequences":["END"],
			"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"n":{"type":"INTEGER"}}},
			"thinkingConfig":{"thinkingBudget":-1}}
	}`
	if code, out := geminiPost(t, "/v1beta/models/fake/m1:generateContent", body); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("%v %s", err, f.got)
	}
	msgs := up["messages"].([]any)
	sys := asMap(t, msgs[0])
	if sys["role"] != "system" || !strings.HasPrefix(sys["content"].(string), "be brief") ||
		!strings.Contains(sys["content"].(string), "JSON") || !strings.Contains(sys["content"].(string), `"type":"integer"`) {
		t.Errorf("system: %v", sys)
	}
	user := asMap(t, msgs[1])
	if !strings.Contains(string(mustJSON(t, user)), `data:image/png;base64,AAAA`) || !strings.Contains(string(mustJSON(t, user)), "application/pdf") {
		t.Errorf("user: %v", user)
	}
	asst := asMap(t, msgs[2])
	calls := asst["tool_calls"].([]any)
	callID := asMap(t, calls[0])["id"]
	if callID == "" || callID == nil {
		t.Fatalf("assistant call has no id: %v", asst)
	}
	tool := asMap(t, msgs[3])
	if tool["role"] != "tool" || tool["tool_call_id"] != callID || tool["content"] != "package a" {
		t.Errorf("tool result: %v (call %v)", tool, callID)
	}
	fn := asMap(t, asMap(t, up["tools"].([]any)[0])["function"])
	params := asMap(t, fn["parameters"])
	if params["type"] != "object" || asMap(t, asMap(t, params["properties"])["path"])["type"] != "string" {
		t.Errorf("schema not lowered: %v", params)
	}
	if up["reasoning_effort"] != "medium" {
		t.Errorf("effort: %v", up["reasoning_effort"])
	}
	if up["max_tokens"] != float64(100) {
		t.Errorf("max_tokens: %v", up["max_tokens"])
	}
	choice := asMap(t, up["tool_choice"])
	if asMap(t, choice["function"])["name"] != "read" {
		t.Errorf("tool_choice: %v", choice)
	}
}

func TestGeminiAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"m1\",\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}",
	)}
	setup(t, provider.Anthropic, f)
	code, body := geminiPost(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":50}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%v %s", err, body)
	}
	if asMap(t, partsOf(out)[0])["text"] != "Hello" || !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("reply: %s", body)
	}
	if u := asMap(t, out["usageMetadata"]); u["promptTokenCount"] != float64(7) || u["candidatesTokenCount"] != float64(3) {
		t.Errorf("usage: %v", u)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["max_tokens"] != float64(50) {
		t.Errorf("max_tokens to anthropic: %s", f.got)
	}
}

func TestGeminiErrorsCountAndModels(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})

	code, body := geminiPost(t, "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("unknown model: %d %s", code, body)
	}

	code, body = geminiPost(t, "/v1beta/models/fake/m1:countTokens",
		`{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello there, this is a fairly long test sentence"}]}]}}`)
	if code != 200 || !strings.HasPrefix(body, `{"totalTokens":`) {
		t.Errorf("countTokens: %d %s", code, body)
	}
	code, body = geminiPost(t, "/v1beta/models/fake/m1:countTokens", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 || !strings.HasPrefix(body, `{"totalTokens":`) {
		t.Errorf("countTokens bare: %d %s", code, body)
	}

	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	list := rec.Body.String()
	if !strings.Contains(list, `"name":"models/fake/m1"`) || !strings.Contains(list, `"supportedGenerationMethods":["generateContent","streamGenerateContent","countTokens"]`) {
		t.Errorf("models: %s", list)
	}

	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("root info lacks v1beta: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/nothing", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("404 hint lacks v1beta: %d %s", rec.Code, rec.Body.String())
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
