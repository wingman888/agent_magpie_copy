package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

const geminiReq = `{"systemInstruction":{"parts":[{"text":"be brief"}]},
  "contents":[{"role":"user","parts":[{"text":"read a.go"}]},
    {"role":"model","parts":[{"functionCall":{"id":"fc0","name":"read_file","args":{"path":"x"}}}]},
    {"role":"user","parts":[{"functionResponse":{"id":"fc0","name":"read_file","response":{"output":"package x"}}}]}],
  "tools":[{"functionDeclarations":[{"name":"read_file","description":"read","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING","nullable":true}},"propertyOrdering":["path"]}}]}],
  "generationConfig":{"maxOutputTokens":100,"thinkingConfig":{"thinkingBudget":-1}}}`

func geminiUpstream(stop string) *fake {
	return &fake{reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Sure"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"`+stop+`"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
}

func TestGeminiStreamChatUpstream(t *testing.T) {
	f := geminiUpstream("tool_calls")
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["model"] != "m1" || up["stream"] != true || up["reasoning_effort"] != "medium" || up["max_tokens"] != float64(100) {
		t.Errorf("params: %v", up)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["content"] != "be brief" || msgs[3].(map[string]any)["tool_call_id"] != "fc0" {
		t.Errorf("messages: %v", msgs)
	}
	if !strings.Contains(string(f.got), `"type":"object"`) || strings.Contains(string(f.got), "propertyOrdering") || !strings.Contains(string(f.got), "x-nullable") {
		t.Errorf("schema: %s", f.got)
	}
	var parts []map[string]any
	evs := events(body)
	for _, e := range evs {
		for _, c := range e["candidates"].([]any) {
			for _, p := range c.(map[string]any)["content"].(map[string]any)["parts"].([]any) {
				parts = append(parts, p.(map[string]any))
			}
		}
	}
	if len(parts) != 3 || parts[0]["thought"] != true || parts[1]["text"] != "Sure" {
		t.Fatalf("parts: %v", parts)
	}
	fc := parts[2]["functionCall"].(map[string]any)
	if fc["name"] != "read_file" || fc["id"] != "call_1" || fc["args"].(map[string]any)["path"] != "a.go" {
		t.Errorf("functionCall: %v", fc)
	}
	last := evs[len(evs)-1]
	if last["candidates"].([]any)[0].(map[string]any)["finishReason"] != "STOP" {
		t.Errorf("last: %v", last)
	}
	um := last["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage: %v", um)
	}
}

func TestGeminiGenerateAndLength(t *testing.T) {
	setup(t, provider.Chat, geminiUpstream("tool_calls"))
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiReq)
	if code != 200 || !strings.Contains(body, `"finishReason":"STOP"`) || !strings.Contains(body, `"name":"read_file"`) {
		t.Errorf("%d %s", code, body)
	}
	setup(t, provider.Chat, geminiUpstream("length"))
	_, body = post(t, "/v1beta/models/fake/m1:generateContent", geminiReq)
	if !strings.Contains(body, `"finishReason":"MAX_TOKENS"`) {
		t.Errorf("%s", body)
	}
}

func TestGeminiTranslation(t *testing.T) {
	r, err := parseGemini([]byte(`{"systemInstruction":"sys",
	  "contents":[{"role":"model","parts":[{"functionCall":{"name":"f","args":{}}}]},
	    {"parts":[{"functionResponse":{"name":"f","response":{"error":"x"}}},{"inlineData":{"mimeType":"image/png","data":"AA"}},{"inlineData":{"mimeType":"application/pdf","data":"AA"}}]},
	    {"parts":[]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"any","allowedFunctionNames":["f"]}},
	  "generationConfig":{"responseMimeType":"application/json","responseJsonSchema":{"type":"OBJECT"}}}`), "m", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 2 || !r.Stream || r.ToolChoice != "name:f" {
		t.Fatalf("%+v", r)
	}
	call, res := r.Messages[0].Parts[0], r.Messages[1].Parts
	if call.ID == "" || res[0].CallID != call.ID || !res[0].IsError || res[0].Text != "x" {
		t.Errorf("result: %+v / %+v", call, res[0])
	}
	if res[1].Kind != Image || res[2].Text != "[attachment application/pdf]" {
		t.Errorf("attachments: %+v", res)
	}
	if !strings.HasPrefix(r.System, "sys\n\n") || !strings.Contains(r.System, `"type":"object"`) {
		t.Errorf("system: %q", r.System)
	}
}

func TestGeminiRoutes(t *testing.T) {
	setup(t, provider.Chat, geminiUpstream("stop"))
	for path, want := range map[string]int{
		"/v1beta/models/nocolon":              404,
		"/v1beta/models/fake/m1:bogus":        404,
		"/v1beta/models/nope:generateContent": 404,
	} {
		code, body := post(t, path, geminiReq)
		if code != want {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
	_, body := post(t, "/v1beta/models/nope:generateContent", geminiReq)
	if !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Error(body)
	}
	_, body = post(t, "/v1beta/models/x:bogus", "{}")
	if !strings.Contains(body, "unknown method bogus") {
		t.Error(body)
	}
	code, body := post(t, "/v1beta/models/fake/m1:countTokens", geminiReq)
	if code != 200 || !strings.Contains(body, `"totalTokens":`) {
		t.Errorf("%d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":`+geminiReq+`}`)
	if code != 200 || !strings.Contains(body, `"totalTokens":`) {
		t.Errorf("%d %s", code, body)
	}
	if code, _ = post(t, "/v1beta/models/fake/m1:countTokens", `{bad`); code != 400 {
		t.Errorf("bad json: %d", code)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	if !strings.Contains(rec.Body.String(), `"name":"models/fake/m1"`) {
		t.Error(rec.Body.String())
	}
}
