package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
	"github.com/yetone/dial/internal/usage"
)

// decode reads a JSON object body.
func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, body)
	}
	return m
}

// candidate is the first candidate of a Gemini reply or stream event.
func candidate(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	cs, _ := m["candidates"].([]any)
	if len(cs) == 0 {
		t.Fatalf("no candidates in %v", m)
	}
	return cs[0].(map[string]any)
}

// partList is the parts of a candidate.
func partList(c map[string]any) []map[string]any {
	content, _ := c["content"].(map[string]any)
	ps, _ := content["parts"].([]any)
	var out []map[string]any
	for _, p := range ps {
		out = append(out, p.(map[string]any))
	}
	return out
}

func TestGeminiGenerateContentText(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"Hello"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	m := decode(t, body)
	c := candidate(t, m)
	ps := partList(c)
	if len(ps) != 1 || ps[0]["text"] != "Hello" {
		t.Errorf("parts: %s", body)
	}
	if c["finishReason"] != "STOP" {
		t.Errorf("finishReason: %s", body)
	}
	um, _ := m["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(4) {
		t.Errorf("usageMetadata: %s", body)
	}
	if !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("not compact: %s", body)
	}
	if f.path != "/v1/chat/completions" || !strings.Contains(string(f.got), `"model":"m1"`) {
		t.Errorf("upstream %s %s", f.path, f.got)
	}
}

func TestGeminiStreamOrder(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":3}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"read"}]}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if n := strings.Count(body, "data: "); n != 5 {
		t.Fatalf("want 5 data events, got %d: %s", n, body)
	}
	evs := events(body)
	if len(evs) != 5 {
		t.Fatalf("%d events: %s", len(evs), body)
	}
	var got []string
	for _, ev := range evs[:4] {
		for _, p := range partList(candidate(t, ev)) {
			switch {
			case p["thought"] == true:
				got = append(got, "thought:"+p["text"].(string))
			case p["functionCall"] != nil:
				fc := p["functionCall"].(map[string]any)
				args := fc["args"].(map[string]any)
				if args["path"] != "a.go" {
					t.Errorf("args not whole: %v", args)
				}
				got = append(got, "call:"+fc["id"].(string)+":"+fc["name"].(string))
			default:
				got = append(got, "text:"+p["text"].(string))
			}
		}
	}
	if want := "thought:hmm|text:Hi |text:there|call:call_1:read"; strings.Join(got, "|") != want {
		t.Errorf("order:\n got %v\nwant %s", got, want)
	}
	last := evs[4]
	c := candidate(t, last)
	if c["finishReason"] != "STOP" {
		t.Errorf("final finishReason: %v", last)
	}
	um, _ := last["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(5) || um["thoughtsTokenCount"] != float64(3) {
		t.Errorf("final usage: %v", um)
	}
}

func TestGeminiRequestTranslation(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[
	    {"role":"user","parts":[{"text":"read a.go"}]},
	    {"role":"model","parts":[{"text":"plan","thought":true,"thoughtSignature":"sig"},{"functionCall":{"name":"read","args":{"path":"x"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}
	  ],
	  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},
	  "generationConfig":{"maxOutputTokens":100,"temperature":0.2,"topP":0.9,"stopSequences":["END"],"thinkingConfig":{"thinkingBudget":-1}}
	}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs, _ := up["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages: %s", f.got)
	}
	msg := func(i int) map[string]any { return msgs[i].(map[string]any) }
	if msg(0)["role"] != "system" || msg(0)["content"] != "be brief" {
		t.Errorf("system message: %v", msg(0))
	}
	asst := msg(2)
	calls, _ := asst["tool_calls"].([]any)
	if asst["role"] != "assistant" || len(calls) != 1 || asst["reasoning_content"] != "plan" {
		t.Fatalf("assistant turn: %v", asst)
	}
	id, _ := calls[0].(map[string]any)["id"].(string)
	if id == "" {
		t.Fatalf("call without id: %v", calls[0])
	}
	if msg(3)["role"] != "tool" || msg(3)["tool_call_id"] != id || msg(3)["content"] != "package x" {
		t.Errorf("tool result does not pair with call %q: %v", id, msg(3))
	}
	tools, _ := up["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	params := fn["parameters"].(map[string]any)
	props := params["properties"].(map[string]any)
	if params["type"] != "object" || props["path"].(map[string]any)["type"] != "string" {
		t.Errorf("schema not lowercased: %v", params)
	}
	if up["reasoning_effort"] != "medium" {
		t.Errorf("thinking budget -1 should be medium: %v", up["reasoning_effort"])
	}
	if up["max_tokens"] != float64(100) || up["temperature"] != 0.2 || up["top_p"] != 0.9 || up["tool_choice"] != "auto" {
		t.Errorf("params: %s", f.got)
	}
	if stop, _ := up["stop"].([]any); len(stop) != 1 || stop[0] != "END" {
		t.Errorf("stop: %v", up["stop"])
	}
	if up["stream"] != true {
		t.Errorf("upstream must stream: %s", f.got)
	}
}

func TestGeminiOverResponsesUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`data: {"type":"response.output_text.delta","delta":"Hey"}`,
		`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":6,"output_tokens":2}}}`)}
	setup(t, provider.Responses, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	m := decode(t, body)
	if ps := partList(candidate(t, m)); len(ps) != 1 || ps[0]["text"] != "Hey" {
		t.Errorf("parts: %s", body)
	}
	if candidate(t, m)["finishReason"] != "STOP" {
		t.Errorf("finishReason: %s", body)
	}
	if um, _ := m["usageMetadata"].(map[string]any); um["promptTokenCount"] != float64(6) || um["candidatesTokenCount"] != float64(2) {
		t.Errorf("usage: %s", body)
	}
	if f.path != "/v1/responses" {
		t.Errorf("path %s", f.path)
	}
}

func TestGeminiOverAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Yo"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"systemInstruction":{"parts":[{"text":"sys"}]},
	  "contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":50}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	m := decode(t, body)
	if ps := partList(candidate(t, m)); len(ps) != 1 || ps[0]["text"] != "Yo" {
		t.Errorf("parts: %s", body)
	}
	if candidate(t, m)["finishReason"] != "STOP" {
		t.Errorf("finishReason: %s", body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if f.path != "/v1/messages" || up["system"] != "sys" || up["max_tokens"] != float64(50) {
		t.Errorf("upstream %s: %s", f.path, f.got)
	}
}

func TestGeminiUnknownModelAndMethod(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	for _, path := range []string{"/v1beta/models/nope:generateContent", "/v1beta/models/nope:countTokens"} {
		code, body := post(t, path, `{"contents":[]}`)
		if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) || !strings.Contains(body, `"code":404`) {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
	code, body := post(t, "/v1beta/models/fake/m1:bogus", `{}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("unknown method: %d %s", code, body)
	}
}

func TestGeminiCountTokens(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	const text = "hello there, count these tokens please"
	direct := `{"contents":[{"role":"user","parts":[{"text":"` + text + `"}]}]}`
	wrapped := `{"generateContentRequest":{"model":"models/fake/m1","contents":[{"role":"user","parts":[{"text":"` + text + `"}]}]}}`
	c1, b1 := post(t, "/v1beta/models/fake/m1:countTokens", direct)
	c2, b2 := post(t, "/v1beta/models/fake/m1:countTokens", wrapped)
	if c1 != 200 || c2 != 200 {
		t.Fatalf("%d %s / %d %s", c1, b1, c2, b2)
	}
	if !strings.HasPrefix(b1, `{"totalTokens":`) || b1 != b2 {
		t.Errorf("direct %s, wrapped %s", b1, b2)
	}
	if m := decode(t, b1); m["totalTokens"].(float64) <= 0 {
		t.Errorf("no tokens counted: %s", b1)
	}
}

func TestGeminiModelsList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	b := rec.Body.String()
	if !strings.Contains(b, `"name":"models/fake/m1"`) || !strings.Contains(b, `"supportedGenerationMethods":["generateContent"`) {
		t.Errorf("list: %s", b)
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models/fake/m1", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"models/fake/m1"`) {
		t.Errorf("one model: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRootAndNotFoundMentionGemini(t *testing.T) {
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "v1beta") || !strings.Contains(rec.Body.String(), "streamGenerateContent") {
		t.Errorf("root: %s", rec.Body.String())
	}
	code, body := post(t, "/nope", `{}`)
	if code != 404 || !strings.Contains(body, "v1beta") {
		t.Errorf("404 hint: %d %s", code, body)
	}
}

func TestGeminiCallIsRecorded(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1beta/models/fake/m1:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	calls := s.Recent()
	if len(calls) != 1 || calls[0].From != provider.Gemini || calls[0].Provider != "fake" || calls[0].Usage.Input != 10 {
		t.Fatalf("call log: %+v", calls)
	}
	found := false
	for _, r := range usage.Load(time.Time{}) {
		if r.Provider == "fake" && r.Model == "m1" && r.Input == 10 {
			found = true
		}
	}
	if !found {
		t.Errorf("usage record missing")
	}
}

func TestParseGeminiPairingAndParts(t *testing.T) {
	r, err := parseGemini([]byte(`{"contents":[
	  {"role":"model","parts":[{"functionCall":{"id":"keep","name":"a","args":{}}},{"functionCall":{"name":"b","args":{"x":1}}}]},
	  {"role":"user","parts":[
	    {"functionResponse":{"name":"b","response":{"output":"B"}}},
	    {"functionResponse":{"id":"keep","name":"a","response":{"error":"boom"}}},
	    {"text":"look"},
	    {"inlineData":{"mimeType":"image/png","data":"AAAA"}},
	    {"inlineData":{"mimeType":"application/pdf","data":"JVBER"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 2 {
		t.Fatalf("%d messages", len(r.Messages))
	}
	a, u := r.Messages[0].Parts, r.Messages[1].Parts
	if len(a) != 2 || a[0].ID != "keep" || a[1].ID == "" || a[1].ID == "keep" || string(a[1].Args) != `{"x":1}` {
		t.Fatalf("calls: %+v", a)
	}
	if len(u) != 5 {
		t.Fatalf("results and parts: %+v", u)
	}
	if u[0].Kind != ToolResult || u[0].CallID != a[1].ID || u[0].Text != "B" {
		t.Errorf("unnamed response did not pair with latest call of its name: %+v", u[0])
	}
	if u[1].Kind != ToolResult || u[1].CallID != "keep" || !u[1].IsError || u[1].Text != "boom" {
		t.Errorf("kept id / error response: %+v", u[1])
	}
	if u[2].Kind != Text || u[2].Text != "look" {
		t.Errorf("text: %+v", u[2])
	}
	if u[3].Kind != Image || u[3].MediaType != "image/png" || u[3].Data != "AAAA" {
		t.Errorf("image: %+v", u[3])
	}
	if u[4].Kind != Text || !strings.Contains(u[4].Text, "application/pdf") {
		t.Errorf("attachment placeholder: %+v", u[4])
	}
}

func TestParseGeminiToolChoiceThinkingAndJSON(t *testing.T) {
	two := `"functionDeclarations":[{"name":"read","parameters":{"type":"OBJECT"}},{"name":"write"}]`
	cases := []struct {
		name, body, choice string
		tools              int
	}{
		{"none", `{"toolConfig":{"functionCallingConfig":{"mode":"NONE"}}}`, "none", 0},
		{"auto", `{"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}}}`, "auto", 0},
		{"any-one-allowed", `{"tools":[{` + two + `}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}}}`, "name:read", 1},
		{"any-many", `{"tools":[{` + two + `}],"toolConfig":{"functionCallingConfig":{"mode":"ANY"}}}`, "required", 2},
		{"any-only-one-declared", `{"tools":[{"functionDeclarations":[{"name":"read"}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY"}}}`, "name:read", 1},
	}
	for _, c := range cases {
		r, err := parseGemini([]byte(c.body))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if r.ToolChoice != c.choice || len(r.Tools) != c.tools {
			t.Errorf("%s: choice %q tools %d, want %q %d", c.name, r.ToolChoice, len(r.Tools), c.choice, c.tools)
		}
	}

	for body, want := range map[string]string{
		`{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`:    "medium",
		`{"generationConfig":{"thinkingConfig":{"thinkingBudget":2000}}}`:  "low",
		`{"generationConfig":{"thinkingConfig":{"thinkingBudget":8000}}}`:  "medium",
		`{"generationConfig":{"thinkingConfig":{"thinkingBudget":30000}}}`: "high",
		`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}`: "high",
		`{"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`:     "",
	} {
		r, err := parseGemini([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if r.Effort != want {
			t.Errorf("%s: effort %q want %q", body, r.Effort, want)
		}
	}

	r, err := parseGemini([]byte(`{"generationConfig":{"responseMimeType":"application/json",
	  "responseSchema":{"type":"OBJECT","properties":{"n":{"type":"INTEGER"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.System, "JSON") || !strings.Contains(r.System, `"type":"object"`) || !strings.Contains(r.System, `"type":"integer"`) {
		t.Errorf("json mode instruction: %q", r.System)
	}
}

func TestGeminiStreamErrorIsDelivered(t *testing.T) {
	f := &fake{t: t, reply: sse(`data: {"error":{"message":"overloaded"}}`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 || !strings.Contains(body, "overloaded") {
		t.Errorf("%d %s", code, body)
	}
}
