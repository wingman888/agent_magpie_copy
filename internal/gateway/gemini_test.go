package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

const chatReply1 = `data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`

func chatFake() *fake {
	return &fake{reply: sse(chatReply1,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
}

const gemBody = `{"systemInstruction":{"parts":[{"text":"be brief"}]},
 "contents":[{"role":"user","parts":[{"text":"read a.go"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}}]},
  {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
  {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"out":"pkg"}}}]}],
 "tools":[{"functionDeclarations":[{"name":"read","description":"d","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}}}}]}],
 "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
 "generationConfig":{"temperature":0.5,"topP":0.9,"stopSequences":["X"],"thinkingConfig":{"thinkingBudget":-1}}}`

func TestGeminiStreamChatUpstream(t *testing.T) {
	f := chatFake()
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", gemBody)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages: %v", msgs)
	}
	if m := msgs[0].(map[string]any); m["role"] != "system" || m["content"] != "be brief" {
		t.Errorf("system: %v", m)
	}
	a, tl := msgs[2].(map[string]any), msgs[3].(map[string]any)
	id := a["tool_calls"].([]any)[0].(map[string]any)["id"]
	if id == "" || tl["role"] != "tool" || tl["tool_call_id"] != id {
		t.Errorf("pairing: %v %v", a, tl)
	}
	if up["reasoning_effort"] != "medium" || up["temperature"] != 0.5 || up["top_p"] != 0.9 {
		t.Errorf("params: %v", up)
	}
	tc := up["tool_choice"].(map[string]any)
	if tc["function"].(map[string]any)["name"] != "read" {
		t.Errorf("tool_choice: %v", tc)
	}
	if !strings.Contains(string(f.got), `"type":"object"`) || strings.Contains(string(f.got), "OBJECT") {
		t.Errorf("schema: %s", f.got)
	}
	if !strings.Contains(string(f.got), "image_url") || !strings.Contains(string(f.got), "data:image/png;base64,QUJD") {
		t.Errorf("image: %s", f.got)
	}
	evs := events(body)
	if len(evs) != 4 {
		t.Fatalf("events %d: %s", len(evs), body)
	}
	part := func(i int) map[string]any {
		return evs[i]["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	}
	if part(0)["thought"] != true || part(1)["text"] != "Hi" {
		t.Errorf("parts: %v %v", part(0), part(1))
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
	if um["promptTokenCount"] != float64(10) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage: %v", um)
	}
}

func TestGeminiGenerateJSONMode(t *testing.T) {
	f := &fake{reply: sse(
		`data: {"id":"c1","choices":[{"delta":{"content":"{}"},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
	  "generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"a":{"type":"STRING"}}}}}`)
	if code != 200 || !strings.Contains(body, `"finishReason":"STOP"`) || !strings.Contains(body, `"usageMetadata"`) {
		t.Fatalf("%d %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	sys := up["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "JSON") || !strings.Contains(sys, `"type":"string"`) {
		t.Errorf("system: %s", sys)
	}
}

func TestGeminiAnthropicUpstream(t *testing.T) {
	f := &fake{reply: sse(
		`event: message_start
data: {"type":"message_start","message":{"id":"m","model":"m1","usage":{"input_tokens":4}}}`,
		`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"yo"}}`,
		`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"systemInstruction":{"parts":[{"text":"sys"}]},
	  "contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":77}}`)
	if code != 200 || !strings.Contains(body, `"text":"yo"`) {
		t.Fatalf("%d %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["system"] != "sys" || up["max_tokens"] != float64(77) {
		t.Errorf("upstream: %v", up)
	}
}

func TestGeminiModelsCountAnd404(t *testing.T) {
	setup(t, provider.Chat, chatFake())
	_, rec := get(t, "/v1beta/models")
	if !strings.Contains(rec, `"name":"models/fake/m1"`) || !strings.Contains(rec, "supportedGenerationMethods") {
		t.Errorf("models: %s", rec)
	}
	code, body := post(t, "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello world, this is text"}]}]}}`)
	if code != 200 || !strings.HasPrefix(body, `{"totalTokens":`) {
		t.Errorf("count: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/nope/x:generateContent", `{}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("404: %d %s", code, body)
	}
	_, info := get(t, "/")
	if !strings.Contains(info, "v1beta") {
		t.Errorf("info: %s", info)
	}
}

func TestGeminiNoIDPairing(t *testing.T) {
	r, err := parseGemini([]byte(`{"contents":[
	 {"role":"model","parts":[{"functionCall":{"name":"f","args":{}}},{"functionCall":{"name":"f","args":{"a":1}}}]},
	 {"role":"user","parts":[{"functionResponse":{"name":"f","response":{"r":1}}}]}]}`), "m", false)
	if err != nil {
		t.Fatal(err)
	}
	c := r.Messages[0].Parts
	if c[0].ID == c[1].ID || c[1].ID == "" || r.Messages[1].Parts[0].CallID != c[1].ID {
		t.Errorf("ids: %+v %+v", c, r.Messages[1].Parts)
	}
}

func get(t *testing.T, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code, rec.Body.String()
}
