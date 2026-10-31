package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

const geminiChatReply = `data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}

data: {"id":"c1","choices":[{"delta":{"content":"Hi"}}]}

data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"read","arguments":""}}]}}]}

data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}

data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}

data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}

data: [DONE]

`

const geminiReq = `{"systemInstruction":{"parts":[{"text":"be brief"}]},
 "contents":[{"role":"user","parts":[{"text":"read a.go"}]},
  {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
  {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}],
 "tools":[{"functionDeclarations":[{"name":"read","description":"d","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"},"n":{"type":"ARRAY","items":{"type":"INTEGER"}}}}}]}],
 "generationConfig":{"maxOutputTokens":77,"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`

func TestGeminiStream(t *testing.T) {
	f := &fake{t: t, reply: geminiChatReply}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["model"] != "m1" || up["reasoning_effort"] != "medium" || up["max_tokens"].(float64) != 77 {
		t.Errorf("upstream: %s", f.got)
	}
	if !strings.Contains(string(f.got), `"type":"string"`) || strings.Contains(string(f.got), "STRING") || !strings.Contains(string(f.got), `"type":"integer"`) {
		t.Errorf("schema not lowered: %s", f.got)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 4 || msgs[2].(map[string]any)["tool_calls"] == nil || msgs[3].(map[string]any)["role"] != "tool" {
		t.Fatalf("messages: %v", msgs)
	}
	id := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["id"]
	if msgs[3].(map[string]any)["tool_call_id"] != id {
		t.Errorf("result not matched to call: %v", msgs)
	}
	evs := events(body)
	if len(evs) != 4 {
		t.Fatalf("events: %s", body)
	}
	part := func(i int) map[string]any {
		return evs[i]["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	}
	if part(0)["thought"] != true || part(1)["text"] != "Hi" {
		t.Errorf("parts: %s", body)
	}
	fc := part(2)["functionCall"].(map[string]any)
	if fc["id"] != "call_9" || fc["name"] != "read" || fc["args"].(map[string]any)["path"] != "a.go" {
		t.Errorf("call: %v", fc)
	}
	last := evs[3]
	if last["candidates"].([]any)[0].(map[string]any)["finishReason"] != "STOP" {
		t.Errorf("last: %v", last)
	}
	um := last["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != 10.0 || um["candidatesTokenCount"] != 3.0 || um["thoughtsTokenCount"] != 2.0 || um["totalTokenCount"] != 15.0 {
		t.Errorf("usage: %v", um)
	}
}

func TestGeminiGenerate(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, reply: geminiChatReply})
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out map[string]any
	json.Unmarshal([]byte(body), &out)
	c := out["candidates"].([]any)[0].(map[string]any)
	parts := c["content"].(map[string]any)["parts"].([]any)
	if c["finishReason"] != "STOP" || len(parts) != 3 || parts[2].(map[string]any)["functionCall"] == nil {
		t.Errorf("body: %s", body)
	}
	if out["usageMetadata"].(map[string]any)["candidatesTokenCount"] != 3.0 {
		t.Errorf("usage: %s", body)
	}
}

func TestGeminiErrorsAndLists(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, reply: geminiChatReply})
	code, body := post(t, "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) || !strings.Contains(body, `"code":404`) {
		t.Errorf("404: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello world, how are you today"}]}]}}`)
	if code != 200 || !strings.Contains(body, `"totalTokens"`) {
		t.Errorf("count: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:countTokens", `{"contents":[{"parts":[{"text":"hello world"}]}]}`)
	if code != 200 || !strings.Contains(body, `"totalTokens"`) {
		t.Errorf("count: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:generateContent", `not json`)
	if code != 400 || !strings.Contains(body, "INVALID_ARGUMENT") {
		t.Errorf("400: %d %s", code, body)
	}
	rec := get(t, "/v1beta/models")
	var l struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	json.Unmarshal([]byte(rec), &l)
	if len(l.Models) != len(provider.Catalog()) || len(l.Models) == 0 || l.Models[0].Name != "models/fake/m1" || len(l.Models[0].SupportedGenerationMethods) != 3 {
		t.Errorf("models: %s", rec)
	}
	if !strings.Contains(get(t, "/nothing"), "/v1beta/models") || !strings.Contains(get(t, "/"), "/v1beta/models") {
		t.Errorf("info/404 lack gemini")
	}
}

func TestGeminiJSONAndToolConfig(t *testing.T) {
	r, err := parseGemini([]byte(`{"model":"x","contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"application/pdf","data":"AA"}},{"inlineData":{"mimeType":"image/png","data":"AA"}}]}],
	  "tools":[{"functionDeclarations":[{"name":"a"},{"name":"b"}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["b"]}},
	  "generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.ToolChoice != "name:b" || !strings.Contains(r.System, `"type":"object"`) || !strings.Contains(strings.ToLower(r.System), "json") {
		t.Errorf("%+v", r)
	}
	ps := r.Messages[0].Parts
	if ps[0].Kind != Text || ps[1].Kind != Image {
		t.Errorf("parts: %+v", ps)
	}
}

func get(t *testing.T, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Body.String()
}
