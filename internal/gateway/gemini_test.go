package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

const geminiReq = `{
 "systemInstruction":{"parts":[{"text":"be brief"}]},
 "contents":[
  {"role":"user","parts":[{"text":"read a.go"}]},
  {"role":"model","parts":[{"text":"thinking","thought":true},{"functionCall":{"id":"c0","name":"read","args":{"path":"x"}}}]},
  {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}
 ],
 "tools":[{"functionDeclarations":[{"name":"read","description":"read a file","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"},"n":{"type":"ARRAY","items":{"type":"INTEGER"}}}}}]}],
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
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiReq)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["content"] != "be brief" || msgs[3].(map[string]any)["tool_call_id"] != "c0" {
		t.Errorf("messages: %v", msgs)
	}
	if up["reasoning_effort"] != "medium" || up["max_tokens"] != float64(77) {
		t.Errorf("params: %s", f.got)
	}
	if !strings.Contains(string(f.got), `"type":"object"`) || !strings.Contains(string(f.got), `"type":"string"`) ||
		!strings.Contains(string(f.got), `"type":"integer"`) || strings.Contains(string(f.got), "OBJECT") {
		t.Errorf("schema: %s", f.got)
	}
	evs := events(body)
	if len(evs) != 4 {
		t.Fatalf("events: %s", body)
	}
	part := func(i int) map[string]any {
		return evs[i]["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	}
	if part(0)["thought"] != true || part(0)["text"] != "hmm" || part(1)["text"] != "Hi" {
		t.Errorf("text parts: %s", body)
	}
	fc := part(2)["functionCall"].(map[string]any)
	if fc["id"] != "call_1" || fc["name"] != "read" || fc["args"].(map[string]any)["path"] != "a.go" {
		t.Errorf("call: %v", fc)
	}
	last := evs[3]
	if last["candidates"].([]any)[0].(map[string]any)["finishReason"] != "STOP" {
		t.Errorf("last: %v", last)
	}
	um := last["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) || um["thoughtsTokenCount"] != float64(2) || um["totalTokenCount"] != float64(15) {
		t.Errorf("usage: %v", um)
	}
}

func TestGeminiGenerateContentAllUpstreams(t *testing.T) {
	anth := sse(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7,"cache_read_input_tokens":3}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"ls","input":{}}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"dir\":\".\"}"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":4}}`,
		`data: {"type":"message_stop"}`)
	resp := sse(
		`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"rc1","name":"ls","id":"fc1"}}`,
		`data: {"type":"response.function_call_arguments.delta","delta":"{\"dir\":\".\"}"}`,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"rc1","name":"ls","arguments":"{\"dir\":\".\"}"}}`,
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":6,"output_tokens":2}}}`)
	for _, c := range []struct {
		proto provider.Protocol
		reply string
		call  string
	}{{provider.Chat, chatReply, "call_1"}, {provider.Anthropic, anth, "toolu_1"}, {provider.Responses, resp, "rc1"}} {
		f := &fake{t: t, reply: c.reply}
		setup(t, c.proto, f)
		code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"},{"inlineData":{"mimeType":"application/pdf","data":"QUJD"}}]}],
		  "tools":[{"functionDeclarations":[{"name":"ls","parametersJsonSchema":{"type":"object"}}]}],
		  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["ls"]}},
		  "generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT"}}}`)
		if code != 200 {
			t.Fatalf("%s: %d %s", c.proto, code, body)
		}
		if !strings.Contains(string(f.got), "attachment omitted") || !strings.Contains(string(f.got), "JSON") {
			t.Errorf("%s upstream: %s", c.proto, f.got)
		}
		var out struct {
			Candidates []struct {
				Content struct {
					Parts []map[string]any
				}
				FinishReason string
			}
			UsageMetadata map[string]any
		}
		json.Unmarshal([]byte(body), &out)
		if len(out.Candidates) != 1 || out.Candidates[0].FinishReason == "" || out.UsageMetadata == nil {
			t.Fatalf("%s reply: %s", c.proto, body)
		}
		var found bool
		for _, p := range out.Candidates[0].Content.Parts {
			if fc, ok := p["functionCall"].(map[string]any); ok && fc["id"] == c.call {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no functionCall in %s", c.proto, body)
		}
		if c.proto == provider.Anthropic {
			um := out.UsageMetadata
			if out.Candidates[0].FinishReason != "MAX_TOKENS" || um["promptTokenCount"] != float64(10) || um["cachedContentTokenCount"] != float64(3) {
				t.Errorf("anthropic: %s", body)
			}
		}
	}
}

func TestGeminiMatchesCallByName(t *testing.T) {
	r, err := parseGemini([]byte(`{"contents":[
	 {"role":"model","parts":[{"functionCall":{"name":"f","args":{"a":1}}},{"functionCall":{"name":"f","args":{"a":2}}}]},
	 {"role":"user","parts":[{"functionResponse":{"name":"f","response":{"x":1}}},{"functionResponse":{"name":"f","response":{"x":2}}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	a, u := r.Messages[0].Parts, r.Messages[1].Parts
	if a[0].ID == "" || u[0].CallID != a[0].ID || u[1].CallID != a[1].ID || a[0].ID == a[1].ID {
		t.Errorf("ids: %+v %+v", a, u)
	}
}

func TestGeminiThinkingLevels(t *testing.T) {
	for in, want := range map[string]string{
		`{"thinkingBudget":0}`: "", `{"thinkingBudget":2000}`: "low", `{"thinkingBudget":20000}`: "high", `{"thinkingLevel":"HIGH"}`: "high"} {
		r, _ := parseGemini([]byte(`{"generationConfig":{"thinkingConfig":` + in + `}}`))
		if r.Effort != want {
			t.Errorf("%s: %q want %q", in, r.Effort, want)
		}
	}
}

func TestGeminiErrorsCountModels(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) || !strings.Contains(body, `"code":404`) {
		t.Errorf("%d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello world, how are you today"}]}]}}`)
	if code != 200 || !strings.Contains(body, `"totalTokens":`) || strings.Contains(body, `"totalTokens":0`) {
		t.Errorf("%d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake%2Fm1:countTokens", `{"contents":[{"parts":[{"text":"hello"}]}]}`)
	if code != 200 {
		t.Errorf("%d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:bogus", `{}`)
	if code != 404 {
		t.Errorf("%d %s", code, body)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	var out struct {
		Models []struct {
			Name        string
			DisplayName string
			Methods     []string `json:"supportedGenerationMethods"`
		}
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || len(out.Models) != len(provider.Catalog()) || len(out.Models) == 0 || out.Models[0].Name != "models/fake/m1" || len(out.Models[0].Methods) != 3 {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}
