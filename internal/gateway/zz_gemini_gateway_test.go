package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// zzFake is an upstream that records what it got and replies with a script.
type zzFake struct {
	t     *testing.T
	got   []byte
	path  string
	head  http.Header
	reply string // SSE body
	ctype string
	code  int
}

func (f *zzFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.got, _ = io.ReadAll(r.Body)
	f.path, f.head = r.URL.Path, r.Header
	ct := f.ctype
	if ct == "" {
		ct = "text/event-stream"
	}
	w.Header().Set("Content-Type", ct)
	if f.code != 0 {
		w.WriteHeader(f.code)
	}
	io.WriteString(w, f.reply)
}

func zzSSE(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n\n")
	}
	return b.String()
}

// zzSetup points dial's provider file at a temp dir and adds one provider
// speaking only the given protocol, backed by the fake.
func zzSetup(t *testing.T, proto provider.Protocol, f *zzFake) *httptest.Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}}
	switch proto {
	case provider.Chat:
		p.Chat = up.URL + "/v1"
	case provider.Responses:
		p.Responses = up.URL + "/v1"
	case provider.Anthropic:
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return up
}

func zzPost(t *testing.T, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func zzEvents(body string) []map[string]any {
	var out []map[string]any
	readSSE(strings.NewReader(body), func(_, data string) error {
		var m map[string]any
		if json.Unmarshal([]byte(data), &m) == nil {
			out = append(out, m)
		}
		return nil
	})
	return out
}

// zzGeminiReplyText reads the text and finish reason of a whole Gemini reply.
func zzGeminiReplyText(t *testing.T, body string) (string, string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	cand := m["candidates"].([]any)[0].(map[string]any)
	var text string
	for _, p := range cand["content"].(map[string]any)["parts"].([]any) {
		if s, ok := p.(map[string]any)["text"].(string); ok {
			text += s
		}
	}
	return text, cand["finishReason"].(string)
}

// zzGeminiUsage reads usageMetadata of a whole Gemini reply.
func zzGeminiUsage(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	u, ok := m["usageMetadata"].(map[string]any)
	if !ok {
		t.Fatalf("no usageMetadata: %s", body)
	}
	return u
}

// zzPartsOf is the parts of one streamed Gemini event.
func zzPartsOf(t *testing.T, ev map[string]any) []any {
	t.Helper()
	cands, ok := ev["candidates"].([]any)
	if !ok || len(cands) == 0 {
		t.Fatalf("no candidates: %v", ev)
	}
	return cands[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)
}

// chatText is a Chat Completions reply that says "ok" and stops.
func zzChatText(text string) string {
	return zzSSE(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"`+text+`"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
		`data: [DONE]`)
}

func TestZzGeminiGenerateOverChat(t *testing.T) {
	f := &zzFake{t: t, reply: zzSSE(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"Hello"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":" world"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`)}
	zzSetup(t, provider.Chat, f)
	code, body := zzPost(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	text, finish := zzGeminiReplyText(t, body)
	if text != "Hello world" || finish != "STOP" || !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("reply: %s", body)
	}
	u := zzGeminiUsage(t, body)
	if u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(5) {
		t.Errorf("usage: %v", u)
	}
	if f.path != "/v1/chat/completions" || !strings.Contains(string(f.got), `"stream":true`) {
		t.Errorf("upstream %s: %s", f.path, f.got)
	}
}

func TestZzGeminiGenerateOverResponses(t *testing.T) {
	f := &zzFake{t: t, reply: zzSSE(
		`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`data: {"type":"response.output_text.delta","delta":"Hi"}`,
		`data: {"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":7,"output_tokens":3,"output_tokens_details":{"reasoning_tokens":2}}}}`)}
	zzSetup(t, provider.Responses, f)
	code, body := zzPost(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if text, finish := zzGeminiReplyText(t, body); text != "Hi" || finish != "STOP" {
		t.Errorf("reply: %s", body)
	}
	u := zzGeminiUsage(t, body)
	if u["promptTokenCount"] != float64(7) || u["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage: %v", u)
	}
	if f.path != "/v1/responses" {
		t.Errorf("upstream path %s", f.path)
	}
}

func TestZzGeminiGenerateOverAnthropic(t *testing.T) {
	f := &zzFake{t: t, reply: zzSSE(
		`data: {"type":"message_start","message":{"id":"msg1","model":"m1","usage":{"input_tokens":4}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Yo"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`data: {"type":"message_stop"}`)}
	zzSetup(t, provider.Anthropic, f)
	code, body := zzPost(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if text, finish := zzGeminiReplyText(t, body); text != "Yo" || finish != "STOP" {
		t.Errorf("reply: %s", body)
	}
	u := zzGeminiUsage(t, body)
	if u["promptTokenCount"] != float64(4) || u["candidatesTokenCount"] != float64(2) {
		t.Errorf("usage: %v", u)
	}
	if f.path != "/v1/messages" {
		t.Errorf("upstream path %s", f.path)
	}
}

// TestGeminiStreamOrder: thinking, then text, then the function call, each as
// one event, and a last event with finishReason and usage.
func TestZzGeminiStreamOrder(t *testing.T) {
	f := &zzFake{t: t, reply: zzSSE(
		`data: {"id":"c2","model":"m1","choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"content":"Hi"}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c2","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c2","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":6,"completion_tokens_details":{"reasoning_tokens":4}}}`,
		`data: [DONE]`)}
	zzSetup(t, provider.Chat, f)
	code, body := zzPost(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"read"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := zzEvents(body)
	if len(evs) != 4 {
		t.Fatalf("want 4 events, got %d: %s", len(evs), body)
	}
	th := zzPartsOf(t, evs[0])[0].(map[string]any)
	if th["thought"] != true || th["text"] != "hmm" {
		t.Errorf("thought event: %v", th)
	}
	if tx := zzPartsOf(t, evs[1])[0].(map[string]any); tx["text"] != "Hi" {
		t.Errorf("text event: %v", tx)
	}
	fc := zzPartsOf(t, evs[2])[0].(map[string]any)["functionCall"].(map[string]any)
	args := fc["args"].(map[string]any)
	if fc["id"] != "call_9" || fc["name"] != "read" || args["path"] != "a.go" {
		t.Errorf("function call event: %v", fc)
	}
	last := evs[3]["candidates"].([]any)[0].(map[string]any)
	if last["finishReason"] != "STOP" {
		t.Errorf("last event: %v", evs[3])
	}
	u := evs[3]["usageMetadata"].(map[string]any)
	if u["thoughtsTokenCount"] != float64(4) || u["promptTokenCount"] != float64(10) {
		t.Errorf("usage: %v", u)
	}
}

// TestGeminiRequestTranslation checks what a Chat upstream is sent for a
// Gemini request with a system instruction, a call and its response, typed
// schemas, a tool choice and a thinking budget of -1.
func TestZzGeminiRequestTranslation(t *testing.T) {
	f := &zzFake{t: t, reply: zzChatText("ok")}
	zzSetup(t, provider.Chat, f)
	reqBody := `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[
	    {"role":"user","parts":[{"text":"read a.go"}]},
	    {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}
	  ],
	  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
	    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}}}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
	  "generationConfig":{"thinkingConfig":{"thinkingBudget":-1},"maxOutputTokens":300,"temperature":0.2}}`
	code, body := zzPost(t, "/v1beta/models/fake/m1:generateContent", reqBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body: %s", f.got)
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
	res := msgs[3].(map[string]any)
	if res["role"] != "tool" || res["tool_call_id"] == "" || res["tool_call_id"] != call["id"] {
		t.Errorf("call and result not paired: %s", f.got)
	}
	if res["content"] != "package x" {
		t.Errorf("result content: %v", res["content"])
	}
	fn := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	params := fn["parameters"].(map[string]any)
	prop := params["properties"].(map[string]any)["path"].(map[string]any)
	if params["type"] != "object" || prop["type"] != "string" {
		t.Errorf("schema not lower-cased: %v", params)
	}
	tc := up["tool_choice"].(map[string]any)
	if tc["function"].(map[string]any)["name"] != "read" {
		t.Errorf("tool_choice: %v", tc)
	}
	if up["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort: %v", up["reasoning_effort"])
	}
	if up["max_tokens"] != float64(300) || up["temperature"] != 0.2 {
		t.Errorf("limits: max_tokens=%v temperature=%v", up["max_tokens"], up["temperature"])
	}
}

// TestGeminiCallIDs: calls with and without ids keep or pair ids correctly.
func TestZzGeminiCallIDs(t *testing.T) {
	f := &zzFake{t: t, reply: zzChatText("ok")}
	zzSetup(t, provider.Chat, f)
	reqBody := `{"contents":[
	  {"role":"model","parts":[{"functionCall":{"id":"fc1","name":"a","args":{}}}]},
	  {"role":"user","parts":[{"functionResponse":{"id":"fc1","name":"a","response":{"result":"A"}}}]},
	  {"role":"model","parts":[{"functionCall":{"name":"b","args":{}}}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"b","response":{"result":"B"}}}]}]}`
	if code, body := zzPost(t, "/v1beta/models/fake/m1:generateContent", reqBody); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages: %s", f.got)
	}
	first := msgs[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["id"]
	if first != "fc1" || msgs[1].(map[string]any)["tool_call_id"] != "fc1" {
		t.Errorf("explicit id not kept: %s", f.got)
	}
	secondCall := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["id"].(string)
	if secondCall == "" || msgs[3].(map[string]any)["tool_call_id"] != secondCall {
		t.Errorf("generated id not paired by name: %s", f.got)
	}
}

func TestZzGeminiAnthropicUpstreamTranslation(t *testing.T) {
	f := &zzFake{t: t, reply: zzSSE(
		`data: {"type":"message_start","message":{"id":"msg1","model":"m1","usage":{"input_tokens":4}}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`)}
	zzSetup(t, provider.Anthropic, f)
	reqBody := `{"systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[{"role":"user","parts":[{"text":"hi"}]}],
	  "generationConfig":{"maxOutputTokens":500,"thinkingConfig":{"thinkingBudget":-1}}}`
	if code, body := zzPost(t, "/v1beta/models/fake/m1:generateContent", reqBody); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["system"] != "be brief" {
		t.Errorf("system field: %v", up["system"])
	}
	th := up["thinking"].(map[string]any)
	if th["type"] != "enabled" || th["budget_tokens"] != float64(10000) {
		t.Errorf("thinking: %v", th)
	}
}

func TestZzGeminiInlineData(t *testing.T) {
	f := &zzFake{t: t, reply: zzChatText("ok")}
	zzSetup(t, provider.Chat, f)
	reqBody := `{"contents":[{"role":"user","parts":[{"text":"look"},
	  {"inlineData":{"mimeType":"image/png","data":"AAAA"}},
	  {"inlineData":{"mimeType":"application/pdf","data":"BBBB"}}]}]}`
	if code, body := zzPost(t, "/v1beta/models/fake/m1:generateContent", reqBody); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	got := string(f.got)
	if !strings.Contains(got, `"url":"data:image/png;base64,AAAA"`) {
		t.Errorf("image not passed: %s", got)
	}
	if !strings.Contains(got, "application/pdf") || strings.Contains(got, "BBBB") {
		t.Errorf("non-image attachment should be a text placeholder: %s", got)
	}
}

func TestZzGeminiJSONModeAndToolChoiceNone(t *testing.T) {
	f := &zzFake{t: t, reply: zzChatText("{}")}
	zzSetup(t, provider.Chat, f)
	reqBody := `{"contents":[{"role":"user","parts":[{"text":"who"}]}],
	  "tools":[{"functionDeclarations":[{"name":"t","parameters":{"type":"OBJECT"}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"NONE"}},
	  "generationConfig":{"responseMimeType":"application/json",
	    "responseSchema":{"type":"OBJECT","properties":{"name":{"type":"STRING"}}}}}`
	if code, body := zzPost(t, "/v1beta/models/fake/m1:generateContent", reqBody); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	sys := up["messages"].([]any)[0].(map[string]any)
	content, _ := sys["content"].(string)
	if sys["role"] != "system" || !strings.Contains(content, "JSON") || !strings.Contains(content, `"name"`) || !strings.Contains(content, `"type":"object"`) {
		t.Errorf("json instruction: %v", sys)
	}
	if up["tool_choice"] != "none" {
		t.Errorf("tool_choice: %v", up["tool_choice"])
	}
}

func TestZzGeminiUnknownModel404(t *testing.T) {
	zzSetup(t, provider.Chat, &zzFake{t: t, reply: zzChatText("ok")})
	code, body := zzPost(t, "/v1beta/models/nope/x:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) || !strings.Contains(body, `"code":404`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestZzGeminiCountTokens(t *testing.T) {
	zzSetup(t, provider.Chat, &zzFake{t: t, reply: zzChatText("ok")})
	direct := `{"contents":[{"role":"user","parts":[{"text":"hello world, this is a test of counting"}]}]}`
	wrapped := `{"generateContentRequest":{"model":"models/fake/m1","contents":[{"role":"user","parts":[{"text":"hello world, this is a test of counting"}]}]}}`
	code1, b1 := zzPost(t, "/v1beta/models/fake/m1:countTokens", direct)
	code2, b2 := zzPost(t, "/v1beta/models/fake/m1:countTokens", wrapped)
	if code1 != 200 || code2 != 200 || !strings.HasPrefix(b1, `{"totalTokens":`) || b1 != b2 {
		t.Errorf("direct %d %s; wrapped %d %s", code1, b1, code2, b2)
	}
	if strings.Contains(b1, `"totalTokens":0`) {
		t.Errorf("no tokens counted: %s", b1)
	}
}

func TestZzGeminiListModels(t *testing.T) {
	zzSetup(t, provider.Chat, &zzFake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"models/fake/m1"`) ||
		!strings.Contains(rec.Body.String(), `"supportedGenerationMethods"`) {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestZzGeminiRootAndNotFoundMentionV1beta(t *testing.T) {
	zzSetup(t, provider.Chat, &zzFake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("root: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/nothing-here", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("404 hint: %d %s", rec.Code, rec.Body.String())
	}
}

func TestZzGeminiUpstreamErrorIsGeminiShaped(t *testing.T) {
	f := &zzFake{t: t, ctype: "application/json", code: 402, reply: `{"error":{"message":"Insufficient Balance","type":"x"}}`}
	zzSetup(t, provider.Chat, f)
	code, body := zzPost(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 402 || !strings.Contains(body, `"error":{"code":402`) || !strings.Contains(body, "Fake: Insufficient Balance") {
		t.Errorf("%d %s", code, body)
	}
}

func TestZzGeminiCallIsLogged(t *testing.T) {
	zzSetup(t, provider.Chat, &zzFake{t: t, reply: zzChatText("ok")})
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1beta/models/fake/m1:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)))
	calls := s.Recent()
	if len(calls) != 1 || string(calls[0].From) != "gemini" || calls[0].Status != 200 || calls[0].Provider != "fake" {
		t.Errorf("call log: %+v", calls)
	}
}

func TestZzGeminiThinkingMapping(t *testing.T) {
	ptr := func(n int) *int { return &n }
	cases := []struct {
		in     *gThinking
		effort string
		on     bool
	}{
		{&gThinking{ThinkingBudget: ptr(-1)}, "medium", true},
		{&gThinking{ThinkingBudget: ptr(0)}, "", false},
		{&gThinking{ThinkingBudget: ptr(512)}, "low", true},
		{&gThinking{ThinkingBudget: ptr(5000)}, "medium", true},
		{&gThinking{ThinkingBudget: ptr(20000)}, "high", true},
		{&gThinking{IncludeThoughts: true}, "", true},
	}
	for _, c := range cases {
		if e, on := geminiThinking(c.in); e != c.effort || on != c.on {
			t.Errorf("budget %+v: got %q %v", c.in, e, on)
		}
	}
}

func TestZzLowerSchema(t *testing.T) {
	got := string(geminiLowerSchema(json.RawMessage(`{"type":"OBJECT","properties":{"type":{"type":"STRING"}},"items":{"type":"ARRAY"}}`)))
	want := `{"items":{"type":"array"},"properties":{"type":{"type":"string"}},"type":"object"}`
	if got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

// TestAuditGeminiThoughtGoesBackToChat: a thought in the Gemini history is
// sent back to a Chat upstream as the assistant's reasoning_content.
func TestZzAuditGeminiThoughtGoesBackToChat(t *testing.T) {
	f := &zzFake{t: t, reply: zzChatText("ok")}
	zzSetup(t, provider.Chat, f)
	body := `{"contents":[
	  {"role":"user","parts":[{"text":"q"}]},
	  {"role":"model","parts":[{"thought":true,"text":"plan"},{"text":"answer"}]},
	  {"role":"user","parts":[{"text":"again"}]}]}`
	if code, b := zzPost(t, "/v1beta/models/fake/m1:generateContent", body); code != 200 {
		t.Fatalf("status %d: %s", code, b)
	}
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body: %s", f.got)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages: %s", f.got)
	}
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" || asst["reasoning_content"] != "plan" || asst["content"] != "answer" {
		t.Errorf("thought not sent back: %s", f.got)
	}
}

// TestAuditGeminiThoughtSignatureToAnthropic: a signed thought goes back as a
// thinking block carrying its signature.
func TestZzAuditGeminiThoughtSignatureToAnthropic(t *testing.T) {
	f := &zzFake{t: t, reply: zzSSE(
		`data: {"type":"message_start","message":{"id":"msg1","model":"m1","usage":{"input_tokens":4}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Yo"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`data: {"type":"message_stop"}`)}
	zzSetup(t, provider.Anthropic, f)
	body := `{"contents":[
	  {"role":"user","parts":[{"text":"q"}]},
	  {"role":"model","parts":[{"thought":true,"text":"plan","thoughtSignature":"sig1"},{"text":"answer"}]},
	  {"role":"user","parts":[{"text":"again"}]}]}`
	if code, b := zzPost(t, "/v1beta/models/fake/m1:generateContent", body); code != 200 {
		t.Fatalf("status %d: %s", code, b)
	}
	got := string(f.got)
	if !strings.Contains(got, `"type":"thinking"`) || !strings.Contains(got, `"signature":"sig1"`) || !strings.Contains(got, `"thinking":"plan"`) {
		t.Errorf("signed thought not sent back: %s", got)
	}
}

// TestAuditGeminiReplyMetadata: a Gemini reply names the model and carries a response id.
func TestZzAuditGeminiReplyMetadata(t *testing.T) {
	zzSetup(t, provider.Chat, &zzFake{t: t, reply: zzChatText("ok")})
	code, body := zzPost(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	id, _ := m["responseId"].(string)
	if code != 200 || m["modelVersion"] != "m1" || id == "" {
		t.Errorf("%d: modelVersion=%v responseId=%v", code, m["modelVersion"], m["responseId"])
	}
}

// TestAuditGeminiToolChoiceModes: ANY with several names is "required", AUTO is "auto".
func TestZzAuditGeminiToolChoiceModes(t *testing.T) {
	cases := []struct{ cfg, want string }{
		{`{"mode":"ANY","allowedFunctionNames":["a","b"]}`, "required"},
		{`{"mode":"ANY"}`, "required"},
		{`{"mode":"AUTO"}`, "auto"},
	}
	for _, c := range cases {
		f := &zzFake{t: t, reply: zzChatText("ok")}
		zzSetup(t, provider.Chat, f)
		body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		  "tools":[{"functionDeclarations":[{"name":"a","parameters":{"type":"OBJECT"}},{"name":"b","parameters":{"type":"OBJECT"}}]}],
		  "toolConfig":{"functionCallingConfig":` + c.cfg + `}}`
		if code, b := zzPost(t, "/v1beta/models/fake/m1:generateContent", body); code != 200 {
			t.Fatalf("status %d: %s", code, b)
		}
		var up map[string]any
		json.Unmarshal(f.got, &up)
		if up["tool_choice"] != c.want {
			t.Errorf("%s: tool_choice = %v, want %s", c.cfg, up["tool_choice"], c.want)
		}
	}
}
