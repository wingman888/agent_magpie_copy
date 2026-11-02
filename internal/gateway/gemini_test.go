package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

const chatReply = `data: {"id":"c1","model":"m1","choices":[{"delta":{"reasoning_content":"hmm"}}]}

data: {"id":"c1","choices":[{"delta":{"content":"Hi"}}]}

data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_7","function":{"name":"read","arguments":"{\"path\":"}}]}}]}

data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a\"}"}}]}}]}

data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}

data: [DONE]

`

func TestGeminiGenerateChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: chatReply}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	 "systemInstruction":{"parts":[{"text":"be brief"}]},
	 "contents":[
	  {"role":"user","parts":[{"text":"look"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}},{"inlineData":{"mimeType":"application/pdf","data":"QUJD"}}]},
	  {"role":"model","parts":[{"thought":true,"text":"thinking"},{"functionCall":{"name":"read","args":{"path":"x"}}}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"data"}}}]}],
	 "tools":[{"functionDeclarations":[{"name":"read","description":"d","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"},"n":{"type":"ARRAY","items":{"type":"INTEGER"}}}}}]}],
	 "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
	 "generationConfig":{"maxOutputTokens":77,"temperature":0.3,"topP":0.8,"stopSequences":["END"],"thinkingConfig":{"thinkingBudget":-1}}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages: %s", f.got)
	}
	if m := msgs[0].(map[string]any); m["role"] != "system" || m["content"] != "be brief" {
		t.Errorf("system: %v", m)
	}
	u := msgs[1].(map[string]any)["content"].([]any)
	if u[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,QUJD" || !strings.Contains(u[2].(map[string]any)["text"].(string), "application/pdf") {
		t.Errorf("user: %v", u)
	}
	a := msgs[2].(map[string]any)
	tc := a["tool_calls"].([]any)[0].(map[string]any)
	id := tc["id"].(string)
	if id == "" || msgs[3].(map[string]any)["role"] != "tool" || msgs[3].(map[string]any)["tool_call_id"] != id {
		t.Errorf("pairing: %v / %v", a, msgs[3])
	}
	if up["reasoning_effort"] != "medium" || up["max_tokens"] != float64(77) || up["temperature"] != 0.3 || up["top_p"] != 0.8 || up["stop"].([]any)[0] != "END" {
		t.Errorf("params: %s", f.got)
	}
	tool := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if !strings.Contains(string(f.got), `"type":"object"`) || strings.Contains(string(f.got), "OBJECT") || tool["name"] != "read" {
		t.Errorf("tools: %s", f.got)
	}
	if tcn := up["tool_choice"].(map[string]any)["function"].(map[string]any)["name"]; tcn != "read" {
		t.Errorf("tool_choice: %v", up["tool_choice"])
	}
	if !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("compact finish: %s", body)
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Role  string
				Parts []map[string]any
			}
			FinishReason string
		}
		UsageMetadata map[string]float64
		ModelVersion  string
		ResponseID    string `json:"responseId"`
	}
	json.Unmarshal([]byte(body), &out)
	ps := out.Candidates[0].Content.Parts
	if len(ps) != 3 || ps[0]["thought"] != true || ps[1]["text"] != "Hi" {
		t.Fatalf("parts: %s", body)
	}
	fc := ps[2]["functionCall"].(map[string]any)
	if fc["id"] != "call_7" || fc["name"] != "read" || fc["args"].(map[string]any)["path"] != "a" {
		t.Errorf("call: %v", fc)
	}
	um := out.UsageMetadata
	if um["promptTokenCount"] != 10 || um["thoughtsTokenCount"] != 2 || um["candidatesTokenCount"] != 5 || out.ModelVersion == "" || out.ResponseID == "" {
		t.Errorf("usage: %s", body)
	}
}

func TestGeminiStream(t *testing.T) {
	f := &fake{t: t, reply: chatReply}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
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
	if fc["id"] != "call_7" || fc["args"].(map[string]any)["path"] != "a" {
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
	if !strings.Contains(body, "data: ") || strings.Contains(body, "event:") {
		t.Errorf("sse: %s", body)
	}
}

func TestGeminiResponsesAndAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		`data: {"type":"response.completed","response":{"id":"r1","model":"m1","usage":{"input_tokens":3,"output_tokens":2}}}`)}
	setup(t, provider.Responses, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"systemInstruction":{"parts":[{"text":"sys"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":9,"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"a":{"type":"STRING"}}}}}`)
	if code != 200 || !strings.Contains(body, `"text":"Hello"`) || f.path != "/v1/responses" {
		t.Fatalf("%d %s %s", code, body, f.path)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	ins, _ := up["instructions"].(string)
	if !strings.Contains(ins, "sys") || !strings.Contains(ins, "JSON") || !strings.Contains(ins, `"type":"string"`) {
		t.Errorf("instructions: %s", f.got)
	}

	f2 := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"yo"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":4}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f2)
	code, body = post(t, "/v1beta/models/fake/m1:generateContent", `{"systemInstruction":{"parts":[{"text":"sys"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":9}}`)
	if code != 200 || !strings.Contains(body, `"text":"yo"`) || !strings.Contains(body, `"finishReason":"MAX_TOKENS"`) {
		t.Fatalf("%d %s", code, body)
	}
	json.Unmarshal(f2.got, &up)
	if up["system"] != "sys" || up["max_tokens"] != float64(9) {
		t.Errorf("anthropic upstream: %s", f2.got)
	}
}

func TestGeminiIDPairingAndNone(t *testing.T) {
	r, err := parseGemini([]byte(`{"model":"x","contents":[
	 {"role":"model","parts":[{"functionCall":{"name":"a","args":{}}},{"functionCall":{"name":"a","args":{}}}]},
	 {"role":"user","parts":[{"functionResponse":{"name":"a","response":{"r":1}}},{"functionResponse":{"name":"a","response":{"r":2}}}]}],
	 "toolConfig":{"functionCallingConfig":{"mode":"NONE"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	c, u := r.Messages[0].Parts, r.Messages[1].Parts
	if c[0].ID == "" || c[0].ID == c[1].ID || u[0].CallID != c[0].ID || u[1].CallID != c[1].ID || r.ToolChoice != "none" {
		t.Errorf("%+v %+v %s", c, u, r.ToolChoice)
	}
}

func TestGeminiCountModelsAndErrors(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello world, this is text"}]}]}}`)
	if code != 200 || !strings.HasPrefix(body, `{"totalTokens":`) {
		t.Errorf("%d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("%d %s", code, body)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	if !strings.Contains(rec.Body.String(), `"name":"models/fake/m1"`) || !strings.Contains(rec.Body.String(), "supportedGenerationMethods") {
		t.Errorf("%s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("%s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/nothing", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestGeminiClaudeWithheld(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeFile(t, dir+"/.credentials.json", `{"claudeAiOauth":{"accessToken":"SECRET-TOKEN"}}`)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "withheld") || strings.Contains(rec.Body.String(), "SECRET-TOKEN") {
		t.Errorf("%s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if strings.Contains(rec.Body.String(), "SECRET-TOKEN") || strings.Contains(rec.Body.String(), `"claude`) {
		t.Errorf("%s", rec.Body.String())
	}
}

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}
