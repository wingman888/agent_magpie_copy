package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// chatScript is an OpenAI Chat stream with reasoning, text and one tool
// call whose arguments arrive in pieces.
var chatScript = sse(
	`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"let me think"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"content":"Reading "}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"content":"it"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_up","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"b.go\"}"}}]}}]}`,
	`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
	`data: [DONE]`)

const geminiReq = `{
  "systemInstruction":{"parts":[{"text":"be brief"}]},
  "contents":[
    {"role":"user","parts":[{"text":"read a.go"}]},
    {"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"a.go"}}}]},
    {"role":"user","parts":[{"functionResponse":{"name":"read_file","response":{"output":"package a"}}}]},
    {"role":"user","parts":[{"text":"now b.go"},{"inlineData":{"mimeType":"image/png","data":"iVBOR"}},{"inlineData":{"mimeType":"application/pdf","data":"JVBER"}}]}
  ],
  "tools":[{"functionDeclarations":[{"name":"read_file","description":"read a file",
    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"},"type":{"type":"STRING","enum":["A","B"]},"lines":{"type":"ARRAY","items":{"type":"INTEGER"}}},"required":["path"]}}]}],
  "toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},
  "generationConfig":{"maxOutputTokens":321,"temperature":0.3,"stopSequences":["END"],
    "thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}
}`

func TestGeminiStreamChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: chatScript}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}

	// what the upstream saw
	if f.path != "/v1/chat/completions" {
		t.Errorf("path %s", f.path)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("messages: %s", f.got)
	}
	m0, m2, m3, m4 := msgs[0].(map[string]any), msgs[2].(map[string]any), msgs[3].(map[string]any), msgs[4].(map[string]any)
	if m0["role"] != "system" || m0["content"] != "be brief" {
		t.Errorf("system: %v", m0)
	}
	callID := m2["tool_calls"].([]any)[0].(map[string]any)["id"]
	if callID == "" || callID == nil || m3["role"] != "tool" || m3["tool_call_id"] != callID || m3["content"] != "package a" {
		t.Errorf("tool turn: %v / %v", m2, m3)
	}
	content := m4["content"].([]any)
	if len(content) != 3 || content[1].(map[string]any)["type"] != "image_url" ||
		!strings.Contains(content[2].(map[string]any)["text"].(string), "application/pdf") {
		t.Errorf("user content: %v", content)
	}
	if up["reasoning_effort"] != "medium" || up["max_tokens"] != float64(321) || up["stream"] != true || up["temperature"] != 0.3 {
		t.Errorf("params: %s", f.got)
	}
	if so, _ := up["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Errorf("stream_options: %v", up["stream_options"])
	}
	if up["tool_choice"] != "auto" {
		t.Errorf("tool_choice: %v", up["tool_choice"])
	}
	params := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	props := params["properties"].(map[string]any)
	if params["type"] != "object" || props["path"].(map[string]any)["type"] != "string" ||
		props["type"].(map[string]any)["type"] != "string" || props["lines"].(map[string]any)["items"].(map[string]any)["type"] != "integer" ||
		props["lines"].(map[string]any)["type"] != "array" {
		t.Errorf("schema not lower-cased: %v", params)
	}
	if e := props["type"].(map[string]any)["enum"].([]any); e[0] != "A" {
		t.Errorf("enum changed: %v", e)
	}

	// what the client got
	evs := events(body)
	var kinds []string
	for _, e := range evs {
		c := e["candidates"].([]any)[0].(map[string]any)
		parts := c["content"].(map[string]any)["parts"].([]any)
		for _, p := range parts {
			p := p.(map[string]any)
			switch {
			case p["functionCall"] != nil:
				kinds = append(kinds, "call")
				fc := p["functionCall"].(map[string]any)
				if fc["id"] != "call_up" || fc["name"] != "read_file" || fc["args"].(map[string]any)["path"] != "b.go" {
					t.Errorf("functionCall: %v", fc)
				}
			case p["thought"] == true:
				kinds = append(kinds, "thought")
			case p["text"] != "":
				kinds = append(kinds, "text")
			}
		}
		if e["modelVersion"] != "fake/m1" || e["responseId"] == "" {
			t.Errorf("chunk meta: %v", e)
		}
	}
	if got := strings.Join(kinds, " "); got != "thought text text call" {
		t.Errorf("parts: %s\n%s", got, body)
	}
	last := evs[len(evs)-1]
	c := last["candidates"].([]any)[0].(map[string]any)
	um, _ := last["usageMetadata"].(map[string]any)
	if c["finishReason"] != "STOP" || um == nil {
		t.Fatalf("last chunk: %v", last)
	}
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(3) || um["thoughtsTokenCount"] != float64(2) || um["totalTokenCount"] != float64(15) {
		t.Errorf("usage: %v", um)
	}
	for _, e := range evs[:len(evs)-1] {
		if e["usageMetadata"] != nil || e["candidates"].([]any)[0].(map[string]any)["finishReason"] != nil {
			t.Errorf("early finish: %v", e)
		}
	}
}

func TestGeminiGenerateChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: chatScript}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Role  string
				Parts []struct {
					Text         string
					Thought      bool
					FunctionCall *struct {
						ID   string
						Name string
						Args map[string]any
					}
				}
			}
			FinishReason string
		}
		UsageMetadata map[string]int
		ModelVersion  string
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	c := out.Candidates[0]
	if c.FinishReason != "STOP" || c.Content.Role != "model" || len(c.Content.Parts) != 3 {
		t.Fatalf("reply: %s", body)
	}
	p := c.Content.Parts
	if !p[0].Thought || p[0].Text != "let me think" || p[1].Text != "Reading it" || p[2].FunctionCall == nil ||
		p[2].FunctionCall.ID != "call_up" || p[2].FunctionCall.Args["path"] != "b.go" {
		t.Errorf("parts: %s", body)
	}
	if u := out.UsageMetadata; u["promptTokenCount"] != 10 || u["candidatesTokenCount"] != 3 || u["thoughtsTokenCount"] != 2 {
		t.Errorf("usage: %v", u)
	}
	if out.ModelVersion != "fake/m1" {
		t.Errorf("modelVersion %q", out.ModelVersion)
	}
	// the call was recorded like any other
}

func TestGeminiRecordsCall(t *testing.T) {
	f := &fake{t: t, reply: chatScript}
	setup(t, provider.Chat, f)
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1beta/models/fake/m1:generateContent", strings.NewReader(geminiReq)))
	calls := s.Recent()
	if len(calls) != 1 || calls[0].From != Gemini || calls[0].To != provider.Chat || calls[0].Model != "fake/m1" ||
		calls[0].Provider != "fake" || calls[0].Status != 200 || calls[0].Usage.Input != 10 || calls[0].Usage.Reasoning != 2 {
		t.Errorf("calls: %+v", calls)
	}
}

func TestGeminiAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7,"cache_read_input_tokens":3}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hm"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"{\"a\":1}"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":4}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/models/fake/m1:streamGenerateContent?alt=sse", `{
	  "systemInstruction":{"parts":[{"text":"sys"}]},
	  "contents":[{"role":"user","parts":[{"text":"give json"}]},
	    {"role":"model","parts":[{"functionCall":{"id":"x1","name":"ls","args":{}}}]},
	    {"role":"user","parts":[{"functionResponse":{"id":"x1","name":"ls","response":{"error":"boom"}}}]}],
	  "tools":[{"functionDeclarations":[{"name":"ls","parametersJsonSchema":{"type":"object"}},{"name":"rm"}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["ls"]}},
	  "generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"a":{"type":"NUMBER"}}},
	    "thinkingConfig":{"thinkingBudget":2048}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	sys, _ := up["system"].(string)
	if !strings.HasPrefix(sys, "sys") || !strings.Contains(sys, "JSON") || !strings.Contains(sys, `"type":"number"`) {
		t.Errorf("system: %q", sys)
	}
	if th := up["thinking"].(map[string]any); th["budget_tokens"] != float64(2048) {
		t.Errorf("thinking: %v", th)
	}
	if tc := up["tool_choice"].(map[string]any); tc["type"] != "tool" || tc["name"] != "ls" {
		t.Errorf("tool_choice: %v", tc)
	}
	if tools := up["tools"].([]any); len(tools) != 1 {
		t.Errorf("tools: %v", tools)
	}
	msgs := up["messages"].([]any)
	res := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if res["tool_use_id"] != "x1" || res["is_error"] != true || res["content"] != "boom" {
		t.Errorf("tool result: %v", res)
	}
	evs := events(body)
	last := evs[len(evs)-1]
	if c := last["candidates"].([]any)[0].(map[string]any); c["finishReason"] != "MAX_TOKENS" {
		t.Errorf("finish: %v", last)
	}
	um := last["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["cachedContentTokenCount"] != float64(3) || um["candidatesTokenCount"] != float64(4) {
		t.Errorf("usage: %v", um)
	}
	if !strings.Contains(body, `"thought":true`) || !strings.Contains(body, `{\"a\":1}`) {
		t.Errorf("body: %s", body)
	}
}

func TestGeminiResponsesUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"resp_1","model":"m1"}}`,
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"fc_1","name":"shell"}}`,
		`data: {"type":"response.function_call_arguments.delta","delta":"{\"cmd\":"}`,
		`data: {"type":"response.function_call_arguments.delta","delta":"\"ls\"}"}`,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"fc_1","name":"shell","arguments":"{\"cmd\":\"ls\"}"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":8,"output_tokens":6,"output_tokens_details":{"reasoning_tokens":1}}}}`)}
	setup(t, provider.Responses, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "contents":[{"role":"user","parts":[{"text":"list"}]}],
	  "tools":[{"functionDeclarations":[{"name":"shell","parameters":{"type":"OBJECT","properties":{"cmd":{"type":"STRING"}}}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"NONE"}},
	  "generationConfig":{"maxOutputTokens":99,"responseMimeType":"application/json","responseJsonSchema":{"type":"object"},
	    "thinkingConfig":{"thinkingLevel":"HIGH"}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if f.path != "/v1/responses" || up["max_output_tokens"] != float64(99) || up["tool_choice"] != "none" {
		t.Errorf("upstream: %s", f.got)
	}
	if r := up["reasoning"].(map[string]any); r["effort"] != "high" {
		t.Errorf("reasoning: %v", r)
	}
	if tf := up["text"].(map[string]any)["format"].(map[string]any); tf["type"] != "json_schema" {
		t.Errorf("text format: %v", tf)
	}
	var out map[string]any
	json.Unmarshal([]byte(body), &out)
	parts := out["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)
	if len(parts) != 2 || parts[0].(map[string]any)["text"] != "ok" {
		t.Fatalf("parts: %s", body)
	}
	fc := parts[1].(map[string]any)["functionCall"].(map[string]any)
	if fc["id"] != "fc_1" || fc["args"].(map[string]any)["cmd"] != "ls" {
		t.Errorf("call: %v", fc)
	}
	um := out["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(8) || um["candidatesTokenCount"] != float64(5) || um["thoughtsTokenCount"] != float64(1) {
		t.Errorf("usage: %v", um)
	}
}

func TestGeminiUnknownModelAndErrors(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"slow down"}}`})
	code, body := post(t, "/v1beta/models/nope/x:generateContent", `{"contents":[]}`)
	var e struct {
		Error struct {
			Code    int
			Message string
			Status  string
		}
	}
	json.Unmarshal([]byte(body), &e)
	if code != 404 || e.Error.Code != 404 || e.Error.Status != "NOT_FOUND" || !strings.Contains(e.Error.Message, "fake/m1") {
		t.Errorf("%d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":`)
	json.Unmarshal([]byte(body), &e)
	if code != 400 || e.Error.Status != "INVALID_ARGUMENT" {
		t.Errorf("%d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	json.Unmarshal([]byte(body), &e)
	if code != 429 || e.Error.Status != "RESOURCE_EXHAUSTED" || !strings.Contains(e.Error.Message, "slow down") {
		t.Errorf("%d %s", code, body)
	}
	code, body = post(t, "/v1beta/nothing", `{}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) || !strings.Contains(body, "generateContent") {
		t.Errorf("%d %s", code, body)
	}
}

func TestGeminiCountTokensAndModels(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	for _, b := range []string{
		`{"contents":[{"role":"user","parts":[{"text":"hello there, how are you today?"}]}]}`,
		`{"generateContentRequest":{"model":"models/fake/m1","contents":[{"role":"user","parts":[{"text":"hello there, how are you today?"}]}]}}`,
	} {
		code, body := post(t, "/v1beta/models/fake/m1:countTokens", b)
		var out struct{ TotalTokens int }
		json.Unmarshal([]byte(body), &out)
		if code != 200 || out.TotalTokens < 1 {
			t.Errorf("%d %s", code, body)
		}
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	var list struct {
		Models []struct {
			Name                       string
			DisplayName                string
			SupportedGenerationMethods []string
		}
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list.Models) != len(provider.Catalog()) || len(list.Models) == 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	m := list.Models[0]
	if m.Name != "models/fake/m1" || m.DisplayName == "" || strings.Join(m.SupportedGenerationMethods, ",") != "generateContent,streamGenerateContent,countTokens" {
		t.Errorf("model: %+v", m)
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "generateContent") {
		t.Errorf("info: %s", rec.Body.String())
	}
}

func TestGeminiTargetAndSchema(t *testing.T) {
	for in, want := range map[string][2]string{
		"fake/m1:generateContent":            {"fake/m1", "generateContent"},
		"models/fake/m1:streamGenerateContent": {"fake/m1", "streamGenerateContent"},
		"ollama/qwen3:8b:generateContent":     {"ollama/qwen3:8b", "generateContent"},
	} {
		m, meth := geminiTarget(in)
		if m != want[0] || meth != want[1] {
			t.Errorf("%s → %s %s", in, m, meth)
		}
	}
	got := string(normalizeSchema(json.RawMessage(`{"type":"OBJECT","properties":{"a":{"anyOf":[{"type":"STRING"},{"type":"NULL"}]},"b":{"type":"BOOLEAN","nullable":true}}}`)))
	if strings.Contains(got, "OBJECT") || strings.Contains(got, "STRING") || strings.Contains(got, "NULL") || strings.Contains(got, "nullable") || !strings.Contains(got, `["boolean","null"]`) {
		t.Errorf("schema: %s", got)
	}
}

func TestGeminiCallIDMatching(t *testing.T) {
	r, err := parseGemini([]byte(`{"contents":[
	  {"role":"model","parts":[{"functionCall":{"name":"a","args":{}}},{"functionCall":{"name":"b"}},{"functionCall":{"name":"a","args":{"x":1}}}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"a","response":{"output":"2"}}},{"functionResponse":{"name":"b","response":{"v":1}}},{"functionResponse":{"name":"a","response":{"output":"1"}}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	calls, results := r.Messages[0].Parts, r.Messages[1].Parts
	if calls[0].ID == "" || calls[0].ID == calls[2].ID {
		t.Fatalf("ids: %+v", calls)
	}
	if results[0].CallID != calls[2].ID || results[1].CallID != calls[1].ID || results[2].CallID != calls[0].ID {
		t.Errorf("matching: %+v / %+v", calls, results)
	}
	if results[1].Text != `{"v":1}` || results[0].Text != "2" {
		t.Errorf("results: %+v", results)
	}
	// thinking budget 0 turns reasoning off
	r, _ = parseGemini([]byte(`{"contents":[],"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`))
	if r.Thinking || r.Effort != "" {
		t.Errorf("budget 0: %+v", r)
	}
}
