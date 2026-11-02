package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

func chatToolReply() string {
	return sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)
}

const geminiBody = `{
  "systemInstruction":{"parts":[{"text":"be brief"}]},
  "contents":[
    {"role":"user","parts":[{"text":"read a.go"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}},{"inlineData":{"mimeType":"application/pdf","data":"BBBB"}}]},
    {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}
  ],
  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"},"lines":{"type":"ARRAY","items":{"type":"INTEGER"}}},"required":["path"]}}]}],
  "toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},
  "generationConfig":{"maxOutputTokens":100,"temperature":0.3,"topP":0.9,"stopSequences":["END"],
    "thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}
}`

func checkGeminiUpstream(t *testing.T, got []byte) {
	t.Helper()
	var up map[string]any
	if err := json.Unmarshal(got, &up); err != nil {
		t.Fatalf("upstream body: %v %s", err, got)
	}
	if up["model"] != "m1" || up["stream"] != true || up["max_tokens"] != float64(100) || up["reasoning_effort"] != "medium" ||
		up["temperature"] != 0.3 || up["top_p"] != 0.9 || up["tool_choice"] != "auto" {
		t.Errorf("params: %s", got)
	}
	if stop, _ := up["stop"].([]any); len(stop) != 1 || stop[0] != "END" {
		t.Errorf("stop: %v", up["stop"])
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages: %s", got)
	}
	m0 := msgs[0].(map[string]any)
	if m0["role"] != "system" || m0["content"] != "be brief" {
		t.Errorf("system: %v", m0)
	}
	user := msgs[1].(map[string]any)
	parts, _ := user["content"].([]any)
	if len(parts) != 3 || parts[1].(map[string]any)["type"] != "image_url" ||
		parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,AAAA" ||
		!strings.Contains(parts[2].(map[string]any)["text"].(string), "application/pdf") {
		t.Errorf("user: %v", user)
	}
	calls := msgs[2].(map[string]any)["tool_calls"].([]any)
	callID := calls[0].(map[string]any)["id"].(string)
	if calls[0].(map[string]any)["function"].(map[string]any)["arguments"] != `{"path":"x"}` {
		t.Errorf("call: %v", calls)
	}
	res := msgs[3].(map[string]any)
	if res["role"] != "tool" || res["tool_call_id"] != callID || callID == "" || res["content"] != "package x" {
		t.Errorf("tool result %v does not answer call %q", res, callID)
	}
	fn := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	params, _ := json.Marshal(fn["parameters"])
	if want := `{"properties":{"lines":{"items":{"type":"integer"},"type":"array"},"path":{"type":"string"}},"required":["path"],"type":"object"}`; string(params) != want {
		t.Errorf("schema:\n got %s\nwant %s", params, want)
	}
}

func TestGeminiStreamChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: chatToolReply()}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.path != "/v1/chat/completions" {
		t.Errorf("path %s", f.path)
	}
	checkGeminiUpstream(t, f.got)
	evs := events(body)
	var parts []map[string]any
	for i, e := range evs {
		c := e["candidates"].([]any)[0].(map[string]any)
		for _, p := range c["content"].(map[string]any)["parts"].([]any) {
			parts = append(parts, p.(map[string]any))
		}
		if _, ok := c["finishReason"]; ok != (i == len(evs)-1) {
			t.Errorf("finishReason on event %d of %d", i, len(evs))
		}
		if e["modelVersion"] != "fake/m1" || e["responseId"] != "c1" {
			t.Errorf("event %d: %v", i, e)
		}
	}
	if len(parts) != 4 || parts[0]["thought"] != true || parts[0]["text"] != "hmm" ||
		parts[1]["text"] != "Hi " || parts[2]["text"] != "there" {
		t.Fatalf("parts: %v", parts)
	}
	fc := parts[3]["functionCall"].(map[string]any)
	if fc["id"] != "call_1" || fc["name"] != "read" || fc["args"].(map[string]any)["path"] != "a.go" {
		t.Errorf("call: %v", fc)
	}
	last := evs[len(evs)-1]
	if c := last["candidates"].([]any)[0].(map[string]any); c["finishReason"] != "STOP" {
		t.Errorf("finish: %v", c)
	}
	um := last["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) ||
		um["thoughtsTokenCount"] != float64(2) || um["totalTokenCount"] != float64(15) {
		t.Errorf("usage: %v", um)
	}
	// the call is recorded like any other
	srv := New()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", strings.NewReader(geminiBody)))
	calls := srv.Recent()
	if len(calls) != 1 || calls[0].From != provider.Gemini || calls[0].To != provider.Chat || calls[0].Status != 200 ||
		calls[0].Usage.Input != 10 || calls[0].Usage.Reasoning != 2 || calls[0].Provider != "fake" {
		t.Errorf("recorded: %+v", calls)
	}
}

func TestGeminiGenerateContent(t *testing.T) {
	f := &fake{t: t, reply: chatToolReply()}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	checkGeminiUpstream(t, f.got)
	var out struct {
		Candidates []struct {
			Content struct {
				Role  string
				Parts []struct {
					Text         string
					Thought      bool
					FunctionCall *struct {
						ID, Name string
						Args     map[string]any
					}
				}
			}
			FinishReason string
		}
		UsageMetadata map[string]int
		ModelVersion  string
		ResponseID    string
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err, body)
	}
	c := out.Candidates[0]
	if c.FinishReason != "STOP" || c.Content.Role != "model" || len(c.Content.Parts) != 3 {
		t.Fatalf("reply: %s", body)
	}
	if !c.Content.Parts[0].Thought || c.Content.Parts[1].Text != "Hi there" ||
		c.Content.Parts[2].FunctionCall == nil || c.Content.Parts[2].FunctionCall.ID != "call_1" ||
		c.Content.Parts[2].FunctionCall.Args["path"] != "a.go" {
		t.Errorf("parts: %s", body)
	}
	if u := out.UsageMetadata; u["promptTokenCount"] != 10 || u["candidatesTokenCount"] != 3 || u["thoughtsTokenCount"] != 2 {
		t.Errorf("usage: %v", u)
	}
	if out.ModelVersion != "fake/m1" || out.ResponseID == "" {
		t.Errorf("meta: %s", body)
	}
	// streamGenerateContent without alt=sse is a JSON array
	code, body = post(t, "/v1beta/models/fake/m1:streamGenerateContent", geminiBody)
	var arr []map[string]any
	if code != 200 || json.Unmarshal([]byte(body), &arr) != nil || len(arr) != 1 {
		t.Errorf("array stream: %d %s", code, body)
	}
}

func TestGeminiIDsAndLengthAndJSON(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c2","choices":[{"delta":{"content":"{\"a\":"}}]}`,
		`data: {"id":"c2","choices":[{"delta":{},"finish_reason":"length"}]}`,
		`data: {"id":"c2","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":6}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "contents":[{"role":"user","parts":[{"text":"go"}]},
	    {"role":"model","parts":[{"functionCall":{"id":"a1","name":"ls","args":{}}},{"functionCall":{"name":"ls","args":{"d":"x"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"ls","response":{"output":"second"}}},{"functionResponse":{"id":"a1","name":"ls","response":{"error":"boom"}}}]}],
	  "tools":[{"functionDeclarations":[{"name":"ls"},{"name":"cat"}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["cat"]}},
	  "generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"a":{"type":"NUMBER","nullable":true}}},
	    "thinkingConfig":{"thinkingBudget":0}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" || !strings.Contains(sys["content"].(string), "JSON") ||
		!strings.Contains(sys["content"].(string), `"type":"object"`) || !strings.Contains(sys["content"].(string), `["number","null"]`) {
		t.Errorf("json instruction: %v", sys)
	}
	if rf, _ := up["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Errorf("response_format: %v", up["response_format"])
	}
	calls := msgs[2].(map[string]any)["tool_calls"].([]any)
	second := calls[1].(map[string]any)["id"].(string)
	if calls[0].(map[string]any)["id"] != "a1" || second == "" || second == "a1" {
		t.Errorf("calls: %v", calls)
	}
	if msgs[3].(map[string]any)["tool_call_id"] != second || msgs[4].(map[string]any)["tool_call_id"] != "a1" {
		t.Errorf("results: %v", msgs[3:])
	}
	tc, _ := json.Marshal(up["tool_choice"])
	if string(tc) != `{"function":{"name":"cat"},"type":"function"}` || len(up["tools"].([]any)) != 1 {
		t.Errorf("tool choice %s, tools %v", tc, up["tools"])
	}
	if _, ok := up["reasoning_effort"]; ok {
		t.Errorf("thinking budget 0 should not reason: %v", up)
	}
	if !strings.Contains(body, `"finishReason":"MAX_TOKENS"`) || !strings.Contains(body, `"cachedContentTokenCount":6`) ||
		!strings.Contains(body, `"promptTokenCount":8`) {
		t.Errorf("reply: %s", body)
	}
}

func TestGeminiErrors(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"slow down"}}`}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	var e struct {
		Error struct {
			Code    int
			Message string
			Status  string
		}
	}
	json.Unmarshal([]byte(body), &e)
	if code != 404 || e.Error.Code != 404 || e.Error.Status != "NOT_FOUND" || !strings.Contains(e.Error.Message, "fake/m1") {
		t.Errorf("unknown model: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":`)
	if code != 400 || !strings.Contains(body, `"status":"INVALID_ARGUMENT"`) {
		t.Errorf("bad request: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 429 || !strings.Contains(body, `"status":"RESOURCE_EXHAUSTED"`) || !strings.Contains(body, "slow down") {
		t.Errorf("upstream error: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:embedContent", `{}`)
	if code != 404 || !strings.Contains(body, `"NOT_FOUND"`) {
		t.Errorf("unknown method: %d %s", code, body)
	}
	code, body = post(t, "/nowhere", `{}`)
	if code != 404 || !strings.Contains(body, "/v1beta") {
		t.Errorf("404 hint: %d %s", code, body)
	}
}

func TestGeminiModelsCountAndInfo(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	p, _ := provider.Find("fake")
	p.Models = []string{"m1", "tag:v2"}
	provider.Save(*p)
	h := New().Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	var list struct {
		Models []struct {
			Name                       string
			DisplayName                string
			SupportedGenerationMethods []string
		}
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list.Models) != len(provider.Catalog()) || len(list.Models) != 2 {
		t.Fatalf("models: %d %s", rec.Code, rec.Body.String())
	}
	for _, m := range list.Models {
		if !strings.HasPrefix(m.Name, "models/fake/") || m.DisplayName == "" ||
			strings.Join(m.SupportedGenerationMethods, ",") != "generateContent,streamGenerateContent,countTokens" {
			t.Errorf("model: %+v", m)
		}
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models/fake/m1", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"models/fake/m1"`) {
		t.Errorf("model: %d %s", rec.Code, rec.Body.String())
	}

	for _, body := range []string{
		`{"contents":[{"role":"user","parts":[{"text":"hello there, how are you today?"}]}]}`,
		`{"generateContentRequest":{"model":"models/fake/m1","contents":[{"role":"user","parts":[{"text":"hello there, how are you today?"}]}]}}`,
	} {
		code, out := post(t, "/v1beta/models/fake/m1:countTokens", body)
		var v struct{ TotalTokens int }
		json.Unmarshal([]byte(out), &v)
		if code != 200 || v.TotalTokens <= 0 {
			t.Errorf("countTokens: %d %s", code, out)
		}
	}
	// a model id with a colon in it: the method is after the last one
	code, out := post(t, "/v1beta/models/fake/tag:v2:countTokens", `{"contents":[{"parts":[{"text":"hi"}]}]}`)
	if code != 200 || !strings.Contains(out, "totalTokens") {
		t.Errorf("colon model: %d %s", code, out)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "generateContent") {
		t.Errorf("info: %s", rec.Body.String())
	}
}

func TestGeminiAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7,"cache_read_input_tokens":3}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"ls","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"dir\":"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\".\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{
	  "contents":[{"role":"user","parts":[{"text":"ls"}]}],
	  "tools":[{"functionDeclarations":[{"name":"ls","parametersJsonSchema":{"type":"object","properties":{"dir":{"type":"string"}}}}]}],
	  "generationConfig":{"responseMimeType":"application/json","thinkingConfig":{"thinkingLevel":"HIGH","includeThoughts":true}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if !strings.Contains(up["system"].(string), "JSON") || up["thinking"].(map[string]any)["budget_tokens"] != float64(24000) {
		t.Errorf("upstream: %s", f.got)
	}
	evs := events(body)
	if len(evs) != 1 {
		t.Fatalf("events: %s", body)
	}
	if !strings.Contains(body, `"functionCall":{"args":{"dir":"."},"id":"toolu_1","name":"ls"}`) ||
		!strings.Contains(body, `"promptTokenCount":10`) || !strings.Contains(body, `"candidatesTokenCount":4`) ||
		!strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("reply: %s", body)
	}
}
