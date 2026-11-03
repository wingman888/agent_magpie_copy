package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// geminiHi is a Gemini request for one short reply.
const geminiHi = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`

// getPath sends a GET to the gateway and returns the status and body.
func getPath(t *testing.T, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code, rec.Body.String()
}

// candidate is the first candidate of a Gemini response or event.
func candidate(t *testing.T, ev map[string]any) map[string]any {
	t.Helper()
	cs, ok := ev["candidates"].([]any)
	if !ok || len(cs) == 0 {
		t.Fatalf("no candidates: %v", ev)
	}
	return cs[0].(map[string]any)
}

// partsOf is the parts of an event's first candidate.
func partsOf(t *testing.T, ev map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, p := range candidate(t, ev)["content"].(map[string]any)["parts"].([]any) {
		out = append(out, p.(map[string]any))
	}
	return out
}

func TestGeminiGenerateContentEachUpstream(t *testing.T) {
	replies := map[provider.Protocol]string{
		provider.Chat: sse(
			`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"Hello "}}]}`,
			`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
			`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			`data: [DONE]`),
		provider.Responses: sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
			`data: {"type":"response.output_text.delta","delta":"Hello "}`,
			`data: {"type":"response.output_text.delta","delta":"there"}`,
			`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}`),
		provider.Anthropic: sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":10,"output_tokens":1}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"there"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`data: {"type":"message_stop"}`),
	}
	for proto, reply := range replies {
		t.Run(string(proto), func(t *testing.T) {
			setup(t, proto, &fake{t: t, reply: reply})
			code, body := post(t, "/v1beta/models/fake/m1:generateContent", geminiHi)
			if code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			var out map[string]any
			if err := json.Unmarshal([]byte(body), &out); err != nil {
				t.Fatalf("not JSON: %v: %s", err, body)
			}
			c := candidate(t, out)
			if c["finishReason"] != "STOP" {
				t.Errorf("finishReason: %v", c["finishReason"])
			}
			var text string
			for _, p := range partsOf(t, out) {
				text += p["text"].(string)
			}
			if text != "Hello there" {
				t.Errorf("text %q in %s", text, body)
			}
			um := out["usageMetadata"].(map[string]any)
			if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(5) {
				t.Errorf("usage: %v", um)
			}
			if _, ok := um["thoughtsTokenCount"]; !ok {
				t.Errorf("no thoughtsTokenCount: %v", um)
			}
			if !strings.Contains(body, `"finishReason":"STOP"`) {
				t.Errorf("reply is not compact: %s", body)
			}
		})
	}
}

func TestGeminiStreamOrderAndFinal(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":7,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiHi)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := events(body)
	if len(evs) != 5 {
		t.Fatalf("%d events: %s", len(evs), body)
	}
	if p := partsOf(t, evs[0])[0]; p["text"] != "hmm" || p["thought"] != true {
		t.Errorf("thought: %v", p)
	}
	if p := partsOf(t, evs[1])[0]; p["text"] != "Hi " {
		t.Errorf("first text: %v", p)
	}
	if p := partsOf(t, evs[2])[0]; p["text"] != "there" {
		t.Errorf("second text: %v", p)
	}
	fc := partsOf(t, evs[3])[0]["functionCall"].(map[string]any)
	args := fc["args"].(map[string]any)
	if fc["id"] != "call_1" || fc["name"] != "read" || args["path"] != "a.go" {
		t.Errorf("function call: %v", fc)
	}
	last := candidate(t, evs[4])
	if last["finishReason"] != "STOP" {
		t.Errorf("final finishReason: %v", last)
	}
	um := evs[4]["usageMetadata"].(map[string]any)
	if um["thoughtsTokenCount"] != float64(2) || um["promptTokenCount"] != float64(10) {
		t.Errorf("final usage: %v", um)
	}
}

func TestGeminiTranslatesRequestToChat(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[
	    {"role":"user","parts":[{"text":"read a.go"}]},
	    {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}
	  ],
	  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
	    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
	  "generationConfig":{"maxOutputTokens":100,"thinkingConfig":{"thinkingBudget":-1}}
	}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body: %v", err)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages: %s", f.got)
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "be brief" {
		t.Errorf("system: %v", sys)
	}
	call := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	tool := msgs[3].(map[string]any)
	fn := call["function"].(map[string]any)
	if call["id"] == "" || tool["role"] != "tool" || tool["tool_call_id"] != call["id"] {
		t.Errorf("call and result are not paired: %s", f.got)
	}
	if fn["name"] != "read" || fn["arguments"] != `{"path":"x"}` || tool["content"] != "package x" {
		t.Errorf("call %v result %v", fn, tool)
	}
	params := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	prop := params["properties"].(map[string]any)["path"].(map[string]any)
	if params["type"] != "object" || prop["type"] != "string" {
		t.Errorf("schema types not lower-cased: %v", params)
	}
	if up["reasoning_effort"] != "medium" || up["max_tokens"] != float64(100) || up["stream"] != true {
		t.Errorf("params: %s", f.got)
	}
	tc := up["tool_choice"].(map[string]any)["function"].(map[string]any)
	if tc["name"] != "read" {
		t.Errorf("tool_choice: %v", up["tool_choice"])
	}
}

// Thoughts from an earlier model turn go back upstream as reasoning_content,
// on any Chat upstream, not only DeepSeek hosts.
func TestGeminiHistoryThoughtReturnsAsReasoningContent(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "contents":[
	    {"role":"user","parts":[{"text":"hi"}]},
	    {"role":"model","parts":[{"text":"let me think","thought":true},{"text":"answer"}]},
	    {"role":"user","parts":[{"text":"again"}]}
	  ]
	}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body: %v", err)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages: %s", f.got)
	}
	am := msgs[1].(map[string]any)
	if am["role"] != "assistant" || am["reasoning_content"] != "let me think" || am["content"] != "answer" {
		t.Errorf("assistant history = %v; upstream: %s", am, f.got)
	}
}

func TestGeminiUnknownModelIsNotFound(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope:generateContent", geminiHi)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) || !strings.Contains(body, `"code":404`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestGeminiCountTokens(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	plain := `{"contents":[{"role":"user","parts":[{"text":"abcdefghijklmnopqrstuvwxyz"}]}]}`
	for _, b := range []string{plain, `{"generateContentRequest":` + plain + `}`} {
		code, body := post(t, "/v1beta/models/fake/m1:countTokens", b)
		if code != 200 || strings.TrimSpace(body) != `{"totalTokens":6}` {
			t.Errorf("%d %s", code, body)
		}
	}
}

func TestGeminiModelsList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := getPath(t, "/v1beta/models")
	if code != 200 || !strings.Contains(body, `"name":"models/fake/m1"`) ||
		!strings.Contains(body, `"supportedGenerationMethods":["generateContent","streamGenerateContent","countTokens"]`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestGeminiInfoAndNotFoundMentionGemini(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	_, info := getPath(t, "/")
	if !strings.Contains(info, "v1beta") || !strings.Contains(info, ":streamGenerateContent") {
		t.Errorf("info: %s", info)
	}
	code, nf := getPath(t, "/nowhere")
	if code != 404 || !strings.Contains(nf, "v1beta") {
		t.Errorf("404: %d %s", code, nf)
	}
}

func TestGeminiAnyWithOneFunctionNamesIt(t *testing.T) {
	cases := []struct {
		cfg  string
		want string
	}{
		{`{"mode":"ANY","allowedFunctionNames":["read"]}`, "name:read"},
		{`{"mode":"ANY","allowedFunctionNames":["read","write"]}`, "required"},
		{`{"mode":"NONE"}`, "none"},
		{`{"mode":"AUTO"}`, "auto"},
	}
	for _, c := range cases {
		r, err := parseGemini([]byte(`{"toolConfig":{"functionCallingConfig":` + c.cfg + `}}`))
		if err != nil || r.ToolChoice != c.want {
			t.Errorf("%s: got %q (%v), want %q", c.cfg, r.ToolChoice, err, c.want)
		}
	}
}

func TestGeminiThinkingBudgetMapping(t *testing.T) {
	cases := []struct {
		cfg  string
		want string
	}{
		{`{"thinkingBudget":-1}`, "medium"},
		{`{"thinkingBudget":0}`, ""},
		{`{"thinkingBudget":2000}`, "low"},
		{`{"thinkingBudget":8000}`, "medium"},
		{`{"thinkingBudget":30000}`, "high"},
		{`{"thinkingLevel":"HIGH"}`, "high"},
	}
	for _, c := range cases {
		r, err := parseGemini([]byte(`{"generationConfig":{"thinkingConfig":` + c.cfg + `}}`))
		if err != nil || r.Effort != c.want || r.Thinking != (c.want != "") {
			t.Errorf("%s: effort %q thinking %v (%v), want %q", c.cfg, r.Effort, r.Thinking, err, c.want)
		}
	}
}

func TestGeminiJSONModeAddsInstruction(t *testing.T) {
	r, err := parseGemini([]byte(`{"systemInstruction":{"parts":[{"text":"sys"}]},
	  "generationConfig":{"responseMimeType":"application/json",
	    "responseSchema":{"type":"OBJECT","properties":{"a":{"type":"STRING"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.System, "sys\n\n") || !strings.Contains(r.System, "JSON") ||
		!strings.Contains(r.System, `"type":"object"`) || !strings.Contains(r.System, `"type":"string"`) ||
		strings.Contains(r.System, "OBJECT") {
		t.Errorf("system: %q", r.System)
	}
}

func TestGeminiPartsAndCallPairing(t *testing.T) {
	r, err := parseGemini([]byte(`{
	  "contents":[
	    {"role":"user","parts":[
	      {"inlineData":{"mimeType":"image/png","data":"AAAA"}},
	      {"inlineData":{"mimeType":"application/pdf","data":"BBBB"}},
	      {"fileData":{"mimeType":"image/jpeg","fileUri":"https://x/y.jpg"}}]},
	    {"role":"model","parts":[
	      {"thought":true,"text":"hmm","thoughtSignature":"sig"},
	      {"functionCall":{"id":"abc","name":"read","args":{"path":"a"}}},
	      {"functionCall":{"name":"read","args":{"path":"b"}}}]},
	    {"role":"user","parts":[
	      {"functionResponse":{"name":"read","response":{"output":"A"}}},
	      {"functionResponse":{"id":"abc","name":"read","response":{"error":"boom"}}}]}
	  ]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 3 {
		t.Fatalf("messages: %+v", r.Messages)
	}
	m0 := r.Messages[0].Parts
	if m0[0].Kind != Image || m0[0].MediaType != "image/png" || m0[0].Data != "AAAA" ||
		m0[1].Kind != Text || m0[1].Text != "[attachment: application/pdf]" ||
		m0[2].Kind != Image || m0[2].URL != "https://x/y.jpg" {
		t.Errorf("user parts: %+v", m0)
	}
	m1 := r.Messages[1]
	if r.Messages[1].Role != "assistant" || m1.Parts[0].Kind != Thinking || m1.Parts[0].Signature != "sig" {
		t.Errorf("thought: %+v", m1.Parts[0])
	}
	if m1.Parts[1].ID != "abc" || m1.Parts[2].ID == "" || m1.Parts[2].ID == "abc" {
		t.Errorf("call ids: %q %q", m1.Parts[1].ID, m1.Parts[2].ID)
	}
	m2 := r.Messages[2].Parts
	// a response without an id pairs with the latest call of its name
	if m2[0].Kind != ToolResult || m2[0].CallID != m1.Parts[2].ID || m2[0].Text != "A" {
		t.Errorf("unpaired response: %+v", m2[0])
	}
	if m2[1].CallID != "abc" || m2[1].Text != "boom" || !m2[1].IsError {
		t.Errorf("response with id: %+v", m2[1])
	}
}
