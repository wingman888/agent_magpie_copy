package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

const geminiBody = `{
 "systemInstruction":{"parts":[{"text":"be brief"}]},
 "contents":[
  {"role":"user","parts":[{"text":"read a.go"}]},
  {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
  {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}],
 "tools":[{"functionDeclarations":[{"name":"read","description":"read","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}}}}]}],
 "toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},
 "generationConfig":{"maxOutputTokens":77,"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`

const chatReply = `data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}

data: {"id":"c1","choices":[{"delta":{"content":"Hi"}}]}

data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}

data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}

data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}

data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}

data: [DONE]

`

func TestGeminiStreamChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: chatReply}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" || msgs[3].(map[string]any)["role"] != "tool" {
		t.Errorf("messages: %v", msgs)
	}
	// call without id and its result get the same generated id
	id1 := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["id"]
	if id1 == "" || id1 != msgs[3].(map[string]any)["tool_call_id"] {
		t.Errorf("ids: %v", msgs)
	}
	if up["reasoning_effort"] != "medium" || up["max_tokens"] != float64(77) {
		t.Errorf("upstream params: %s", f.got)
	}
	if !strings.Contains(string(f.got), `"type":"object"`) || strings.Contains(string(f.got), "OBJECT") {
		t.Errorf("schema: %s", f.got)
	}
	evs := events(body)
	if len(evs) < 4 {
		t.Fatalf("events: %s", body)
	}
	var kinds []string
	for _, ev := range evs {
		c := ev["candidates"].([]any)[0].(map[string]any)
		for _, p := range c["content"].(map[string]any)["parts"].([]any) {
			pm := p.(map[string]any)
			switch {
			case pm["functionCall"] != nil:
				fc := pm["functionCall"].(map[string]any)
				if fc["id"] != "call_1" || fc["args"].(map[string]any)["path"] != "a.go" {
					t.Errorf("call: %v", fc)
				}
				kinds = append(kinds, "call")
			case pm["thought"] == true:
				kinds = append(kinds, "thought")
			case pm["text"] != "":
				kinds = append(kinds, "text")
			}
		}
	}
	if strings.Join(kinds, ",") != "thought,text,call" {
		t.Errorf("order: %v", kinds)
	}
	last := evs[len(evs)-1]
	if last["candidates"].([]any)[0].(map[string]any)["finishReason"] != "STOP" {
		t.Errorf("last: %v", last)
	}
	u := last["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(3) || u["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage: %v", u)
	}
}

func TestGeminiGenerateContent(t *testing.T) {
	f := &fake{t: t, reply: chatReply}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var m map[string]any
	json.Unmarshal([]byte(body), &m)
	c := m["candidates"].([]any)[0].(map[string]any)
	if c["finishReason"] != "STOP" || !strings.Contains(body, `"functionCall"`) || !strings.Contains(body, `"a.go"`) {
		t.Errorf("body: %s", body)
	}
	if m["usageMetadata"].(map[string]any)["candidatesTokenCount"] != float64(3) {
		t.Errorf("usage: %s", body)
	}
}

func TestGeminiUnknownModel(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope/x:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"NOT_FOUND"`) || !strings.Contains(body, `"code":404`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestGeminiModelsAndCount(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"models/fake/m1"`) || !strings.Contains(rec.Body.String(), "countTokens") {
		t.Errorf("models: %s", rec.Body)
	}
	code, body := post(t, "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello world, this is text"}]}]}}`)
	if code != 200 || !strings.Contains(body, `"totalTokens"`) {
		t.Errorf("count: %d %s", code, body)
	}
}

func TestGeminiJSONAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`event: message_start
data: {"type":"message_start","message":{"id":"m","model":"m1","usage":{"input_tokens":1}}}`,
		`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":2}}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"},{"inlineData":{"mimeType":"application/pdf","data":"AA"}}]}],
	 "generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"a":{"type":"STRING"}}}}}`)
	if code != 200 || !strings.Contains(body, `"MAX_TOKENS"`) {
		t.Fatalf("%d %s", code, body)
	}
	got := string(f.got)
	if !strings.Contains(got, "JSON") || !strings.Contains(got, `\"string\"`) && !strings.Contains(got, `"string"`) || !strings.Contains(got, "attachment omitted") {
		t.Errorf("upstream: %s", got)
	}
}
