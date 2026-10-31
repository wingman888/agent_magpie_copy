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
    {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}],
  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING","nullable":true},"n":{"type":"INTEGER"},"xs":{"type":"ARRAY","items":{"type":"NUMBER"},"minItems":"1"}}}}]}],
  "generationConfig":{"maxOutputTokens":77,"temperature":0.3,"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`

func chatFake(t *testing.T) *fake {
	return &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
}

func TestGeminiStreamChatUpstream(t *testing.T) {
	f := chatFake(t)
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" || msgs[2].(map[string]any)["tool_calls"] == nil ||
		msgs[3].(map[string]any)["role"] != "tool" {
		t.Errorf("messages: %v", msgs)
	}
	id := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["id"]
	if msgs[3].(map[string]any)["tool_call_id"] != id || msgs[3].(map[string]any)["content"] != "package x" {
		t.Errorf("tool result not matched to call: %v", msgs)
	}
	if up["reasoning_effort"] != "medium" || up["max_tokens"] != float64(77) || up["stream"] != true {
		t.Errorf("params: %s", f.got)
	}
	params := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	props := params["properties"].(map[string]any)
	if params["type"] != "object" || props["path"].(map[string]any)["type"] != "string" ||
		props["xs"].(map[string]any)["items"].(map[string]any)["type"] != "number" || props["xs"].(map[string]any)["minItems"] != float64(1) {
		t.Errorf("schema: %v", params)
	}
	if _, ok := props["path"].(map[string]any)["nullable"]; ok {
		t.Errorf("nullable kept: %v", props)
	}

	evs := events(body)
	if len(evs) != 5 {
		t.Fatalf("events: %d\n%s", len(evs), body)
	}
	part := func(i int) map[string]any {
		return evs[i]["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	}
	if p := part(0); p["thought"] != true || p["text"] != "hmm" {
		t.Errorf("thought: %v", p)
	}
	if part(1)["text"] != "Hi " || part(2)["text"] != "there" {
		t.Errorf("text: %v %v", part(1), part(2))
	}
	fc := part(3)["functionCall"].(map[string]any)
	if fc["name"] != "read" || fc["id"] != "call_1" || fc["args"].(map[string]any)["path"] != "a.go" {
		t.Errorf("call: %v", fc)
	}
	last := evs[4]
	cand := last["candidates"].([]any)[0].(map[string]any)
	um := last["usageMetadata"].(map[string]any)
	if cand["finishReason"] != "STOP" || um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) ||
		um["thoughtsTokenCount"] != float64(2) || um["totalTokenCount"] != float64(13) {
		t.Errorf("last: %v", last)
	}
	if _, ok := um["cachedContentTokenCount"]; ok {
		t.Errorf("cached present: %v", um)
	}
	if last["responseId"] == "" || last["modelVersion"] == "" {
		t.Errorf("ids: %v", last)
	}
	for _, e := range evs[:4] {
		if e["candidates"].([]any)[0].(map[string]any)["finishReason"] != nil {
			t.Errorf("early finish: %v", e)
		}
	}
}

func TestGeminiGenerateContentChatUpstream(t *testing.T) {
	f := chatFake(t)
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
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
	c := out.Candidates[0]
	if c.FinishReason != "STOP" || len(c.Content.Parts) != 3 || c.Content.Parts[0]["thought"] != true ||
		c.Content.Parts[1]["text"] != "Hi there" || c.Content.Parts[2]["functionCall"] == nil {
		t.Errorf("reply: %s", body)
	}
	if out.UsageMetadata["candidatesTokenCount"] != float64(3) {
		t.Errorf("usage: %s", body)
	}
}

func TestGeminiToolChoiceAndJSON(t *testing.T) {
	f := chatFake(t)
	setup(t, provider.Chat, f)
	post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"x"}]}],
	  "tools":[{"functionDeclarations":[{"name":"a"},{"name":"b","parametersJsonSchema":{"type":"object"}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["b"]}},
	  "generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"k":{"type":"STRING"}}},
	    "thinkingConfig":{"thinkingLevel":"HIGH"},"stopSequences":["END"]}}`)
	var up map[string]any
	json.Unmarshal(f.got, &up)
	tc := up["tool_choice"].(map[string]any)["function"].(map[string]any)["name"]
	if tc != "b" || len(up["tools"].([]any)) != 1 || up["reasoning_effort"] != "high" {
		t.Errorf("tools: %s", f.got)
	}
	rf := up["response_format"].(map[string]any)
	if rf["type"] != "json_schema" || !strings.Contains(string(f.got), `"type":"string"`) {
		t.Errorf("response_format: %s", f.got)
	}
	if up["stop"].([]any)[0] != "END" {
		t.Errorf("stop: %v", up["stop"])
	}

	post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"x"}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"NONE"}},"tools":[{"functionDeclarations":[{"name":"a"}]}]}`)
	json.Unmarshal(f.got, &up)
	if up["tool_choice"] != "none" {
		t.Errorf("none: %s", f.got)
	}
}

func TestGeminiImagesAndIDs(t *testing.T) {
	f := chatFake(t)
	setup(t, provider.Chat, f)
	post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[
	  {"role":"user","parts":[{"text":"look"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}},{"inlineData":{"mimeType":"application/pdf","data":"QUJD"}}]},
	  {"role":"model","parts":[{"functionCall":{"name":"f","args":{}}},{"functionCall":{"name":"f","args":{"a":1}}}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"f","response":{"output":"one"}}},{"functionResponse":{"name":"f","response":{"error":"bad"}}}]}]}`)
	s := string(f.got)
	for _, want := range []string{`data:image/png;base64,QUJD`, `[attachment: application/pdf]`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	calls := msgs[1].(map[string]any)["tool_calls"].([]any)
	id1, id2 := calls[0].(map[string]any)["id"], calls[1].(map[string]any)["id"]
	if id1 == id2 || msgs[2].(map[string]any)["tool_call_id"] != id2 || msgs[3].(map[string]any)["tool_call_id"] != id1 {
		t.Errorf("ids: %v", msgs)
	}
}

func TestGeminiAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7,"cache_read_input_tokens":3}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"{}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":4}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"systemInstruction":{"parts":[{"text":"sys"}]},
	  "contents":[{"role":"user","parts":[{"text":"hi"}]}],
	  "generationConfig":{"maxOutputTokens":50,"responseMimeType":"application/json","responseJsonSchema":{"type":"object"}}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if !strings.Contains(up["system"].(string), "sys") || !strings.Contains(up["system"].(string), "JSON") || up["max_tokens"] != float64(50) {
		t.Errorf("upstream: %s", f.got)
	}
	var out map[string]any
	json.Unmarshal([]byte(body), &out)
	cand := out["candidates"].([]any)[0].(map[string]any)
	um := out["usageMetadata"].(map[string]any)
	if cand["finishReason"] != "MAX_TOKENS" || um["promptTokenCount"] != float64(10) || um["cachedContentTokenCount"] != float64(3) {
		t.Errorf("reply: %s", body)
	}
}

func TestGeminiResponsesUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`data: {"type":"response.output_text.delta","delta":"yo"}`,
		`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":4,"output_tokens":2}}}`)}
	setup(t, provider.Responses, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
	  "generationConfig":{"maxOutputTokens":9,"responseMimeType":"application/json"}}`)
	if code != 200 || !strings.Contains(body, `"yo"`) {
		t.Fatalf("%d %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["max_output_tokens"] != float64(9) || up["text"] == nil {
		t.Errorf("upstream: %s", f.got)
	}
}

func TestGeminiUnknownModelAndCount(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope/x:generateContent", `{"contents":[]}`)
	var e struct{ Error struct {
		Code    int
		Status  string
		Message string
	} }
	json.Unmarshal([]byte(body), &e)
	if code != 404 || e.Error.Code != 404 || e.Error.Status != "NOT_FOUND" || e.Error.Message == "" {
		t.Errorf("%d %s", code, body)
	}
	for _, b := range []string{
		`{"contents":[{"role":"user","parts":[{"text":"hello world, count me"}]}]}`,
		`{"generateContentRequest":{"model":"models/fake/m1","contents":[{"role":"user","parts":[{"text":"hello world, count me"}]}]}}`,
	} {
		code, body = post(t, "/v1beta/models/fake/m1:countTokens", b)
		var c struct{ TotalTokens int }
		json.Unmarshal([]byte(body), &c)
		if code != 200 || c.TotalTokens <= 0 {
			t.Errorf("count %d %s", code, body)
		}
	}
	code, body = post(t, "/v1beta/models/fake/m1:bogus", `{}`)
	if code != 404 {
		t.Errorf("bogus method: %d %s", code, body)
	}
}

func TestGeminiModelsList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	var out struct {
		Models []struct {
			Name, DisplayName          string
			SupportedGenerationMethods []string
		}
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Models) != 1 || out.Models[0].Name != "models/fake/m1" || out.Models[0].DisplayName == "" || len(out.Models[0].SupportedGenerationMethods) != 3 {
		t.Errorf("%s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "generateContent") {
		t.Errorf("info: %s", rec.Body.String())
	}
}
