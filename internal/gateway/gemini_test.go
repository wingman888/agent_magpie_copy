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

// gmnFake is an upstream that records what it got and replies with a script.
// It is a private copy of the package's shared test helper, so this file stands alone.
type gmnFake struct {
	t     *testing.T
	got   []byte
	path  string
	head  http.Header
	reply string // SSE body
	ctype string
	code  int
}

func (f *gmnFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

func gmnSSE(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n\n")
	}
	return b.String()
}

// gmnSetup points dial's provider file at a temp dir and adds one provider
// speaking only the given protocol, backed by the fake.
func gmnSetup(t *testing.T, proto provider.Protocol, f *gmnFake) *httptest.Server {
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

func gmnPost(t *testing.T, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func gmnEvents(body string) []map[string]any {
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

// gmnGet sends a GET to a fresh gateway.
func gmnGet(t *testing.T, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code, rec.Body.String()
}

// gmnUpstreamBody decodes what the fake upstream received.
func gmnUpstreamBody(t *testing.T, f *gmnFake) map[string]any {
	t.Helper()
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body: %v: %s", err, f.got)
	}
	return up
}

// gmnDecode decodes a JSON object reply.
func gmnDecode(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("reply is not JSON: %v: %s", err, body)
	}
	return m
}

// gmnCandidate returns candidates[0] of a Gemini reply or event.
func gmnCandidate(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	cs, ok := m["candidates"].([]any)
	if !ok || len(cs) == 0 {
		t.Fatalf("no candidates: %v", m)
	}
	return cs[0].(map[string]any)
}

// gmnParts returns the parts of a candidate.
func gmnParts(t *testing.T, cand map[string]any) []map[string]any {
	t.Helper()
	raw := cand["content"].(map[string]any)["parts"].([]any)
	var out []map[string]any
	for _, p := range raw {
		out = append(out, p.(map[string]any))
	}
	return out
}

func TestGmnGenerateToChatUpstream(t *testing.T) {
	f := &gmnFake{t: t, reply: gmnSSE(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`)}
	gmnSetup(t, provider.Chat, f)
	code, body := gmnPost(t, "/v1beta/models/fake/m1:generateContent", `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[{"role":"user","parts":[{"text":"hello"}]}],
	  "generationConfig":{"maxOutputTokens":100,"temperature":0.2}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.path != "/v1/chat/completions" {
		t.Errorf("upstream path %s", f.path)
	}
	up := gmnUpstreamBody(t, f)
	msgs := up["messages"].([]any)
	if sys := msgs[0].(map[string]any); sys["role"] != "system" || sys["content"] != "be brief" {
		t.Errorf("system message: %v", sys)
	}
	if up["max_tokens"] != float64(100) || up["temperature"] != 0.2 {
		t.Errorf("params: %v", up)
	}
	m := gmnDecode(t, body)
	cand := gmnCandidate(t, m)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason %v", cand["finishReason"])
	}
	if p := gmnParts(t, cand); len(p) != 1 || p[0]["text"] != "Hi there" {
		t.Errorf("parts: %v", p)
	}
	u := m["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(5) {
		t.Errorf("usage: %v", u)
	}
	if !strings.Contains(body, `"finishReason":"STOP"`) || strings.Contains(body, `": `) {
		t.Errorf("reply is not compact: %s", body)
	}
}

func TestGmnStreamOrderAndChatTranslation(t *testing.T) {
	f := &gmnFake{t: t, reply: gmnSSE(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Let me check"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":8,"completion_tokens_details":{"reasoning_tokens":3}}}`,
		`data: [DONE]`)}
	gmnSetup(t, provider.Chat, f)
	code, body := gmnPost(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[
	    {"role":"user","parts":[{"text":"read a.go"}]},
	    {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}],
	  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
	    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
	  "generationConfig":{"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}

	// what the upstream saw
	up := gmnUpstreamBody(t, f)
	if up["stream"] != true || up["reasoning_effort"] != "medium" {
		t.Errorf("stream/effort: %v", up)
	}
	choice := up["tool_choice"].(map[string]any)
	if fn := choice["function"].(map[string]any); choice["type"] != "function" || fn["name"] != "read" {
		t.Errorf("ANY with one allowed function should name it: %v", choice)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages: %v", msgs)
	}
	if sys := msgs[0].(map[string]any); sys["content"] != "be brief" {
		t.Errorf("system content must be a plain string: %v", sys)
	}
	call := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	id, _ := call["id"].(string)
	tool := msgs[3].(map[string]any)
	if id == "" || tool["role"] != "tool" || tool["tool_call_id"] != id || tool["content"] != "package x" {
		t.Errorf("call and result not paired by name: call %v, result %v", call, tool)
	}
	tp := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	if tp["type"] != "object" || tp["properties"].(map[string]any)["path"].(map[string]any)["type"] != "string" {
		t.Errorf("schema types not lower-cased: %v", tp)
	}

	// what the client got: thought, text, then the function call, which also
	// carries the finish and usage (no extra empty event)
	evs := gmnEvents(body)
	if len(evs) != 3 {
		t.Fatalf("want 3 events, got %d: %s", len(evs), body)
	}
	if strings.Contains(body, `"index"`) {
		t.Errorf("stream events carry an index field: %s", body)
	}
	if p := gmnParts(t, gmnCandidate(t, evs[0])); len(p) != 1 || p[0]["thought"] != true || p[0]["text"] != "hmm" {
		t.Errorf("thought event: %v", p)
	}
	if p := gmnParts(t, gmnCandidate(t, evs[1])); len(p) != 1 || p[0]["text"] != "Let me check" {
		t.Errorf("text event: %v", p)
	}
	fc := gmnParts(t, gmnCandidate(t, evs[2]))[0]["functionCall"].(map[string]any)
	args := fc["args"].(map[string]any)
	if fc["id"] != "call_9" || fc["name"] != "read" || args["path"] != "a.go" {
		t.Errorf("function call event: %v", fc)
	}
	last := gmnCandidate(t, evs[2])
	if last["finishReason"] != "STOP" {
		t.Errorf("finish: %v", last)
	}
	u := evs[2]["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(8) || u["thoughtsTokenCount"] != float64(3) {
		t.Errorf("usage: %v", u)
	}
}

func TestGmnJSONModeAndNoneChoice(t *testing.T) {
	f := &gmnFake{t: t, reply: gmnSSE(
		`data: {"id":"c2","model":"m1","choices":[{"delta":{"content":"{\"n\":4}"}}]}`,
		`data: {"id":"c2","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	gmnSetup(t, provider.Chat, f)
	code, body := gmnPost(t, "/v1beta/models/fake/m1:generateContent", `{
	  "contents":[{"role":"user","parts":[{"text":"give n"}]}],
	  "tools":[{"functionDeclarations":[{"name":"read","parameters":{"type":"OBJECT"}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"NONE"}},
	  "generationConfig":{"responseMimeType":"application/json",
	    "responseSchema":{"type":"OBJECT","properties":{"n":{"type":"INTEGER"}}}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	up := gmnUpstreamBody(t, f)
	sys := up["messages"].([]any)[0].(map[string]any)
	content, _ := sys["content"].(string)
	if sys["role"] != "system" || !strings.Contains(content, "JSON") || !strings.Contains(content, `"integer"`) || !strings.Contains(content, `"object"`) {
		t.Errorf("JSON instruction with lower-cased schema missing: %v", sys)
	}
	if _, ok := up["response_format"]; ok {
		t.Errorf("no upstream response_format should be set: %v", up)
	}
	if up["tool_choice"] != "none" {
		t.Errorf("NONE should map to none: %v", up["tool_choice"])
	}
	if p := gmnParts(t, gmnCandidate(t, gmnDecode(t, body))); len(p) != 1 || p[0]["text"] != `{"n":4}` {
		t.Errorf("reply text: %v", p)
	}
}

func TestGmnToResponsesUpstream(t *testing.T) {
	f := &gmnFake{t: t, reply: gmnSSE(
		`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":7,"output_tokens":3}}}`)}
	gmnSetup(t, provider.Responses, f)
	code, body := gmnPost(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":50}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.path != "/v1/responses" {
		t.Errorf("upstream path %s", f.path)
	}
	if up := gmnUpstreamBody(t, f); up["max_output_tokens"] != float64(50) {
		t.Errorf("max_output_tokens: %v", up)
	}
	m := gmnDecode(t, body)
	cand := gmnCandidate(t, m)
	if p := gmnParts(t, cand); len(p) != 1 || p[0]["text"] != "Hello" || cand["finishReason"] != "STOP" {
		t.Errorf("reply: %v", cand)
	}
	u := m["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(7) || u["candidatesTokenCount"] != float64(3) {
		t.Errorf("usage: %v", u)
	}
}

func TestGmnToAnthropicUpstream(t *testing.T) {
	f := &gmnFake{t: t, reply: gmnSSE(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":4,"output_tokens":1}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hey"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`data: {"type":"message_stop"}`)}
	gmnSetup(t, provider.Anthropic, f)
	code, body := gmnPost(t, "/v1beta/models/fake/m1:generateContent", `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[{"role":"user","parts":[{"text":"hi"}]}],
	  "generationConfig":{"maxOutputTokens":100}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.path != "/v1/messages" {
		t.Errorf("upstream path %s", f.path)
	}
	up := gmnUpstreamBody(t, f)
	if up["system"] != "be brief" || up["max_tokens"] != float64(100) {
		t.Errorf("system and max_tokens: %v", up)
	}
	m := gmnDecode(t, body)
	cand := gmnCandidate(t, m)
	if p := gmnParts(t, cand); len(p) != 1 || p[0]["text"] != "Hey" || cand["finishReason"] != "STOP" {
		t.Errorf("reply: %v", cand)
	}
	u := m["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(4) || u["candidatesTokenCount"] != float64(2) {
		t.Errorf("usage: %v", u)
	}
}

func TestGmnCountTokens(t *testing.T) {
	gmnSetup(t, provider.Chat, &gmnFake{t: t})
	direct := `{"contents":[{"role":"user","parts":[{"text":"hello there, count me please"}]}]}`
	code, body := gmnPost(t, "/v1beta/models/fake/m1:countTokens", direct)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	m := gmnDecode(t, body)
	n, _ := m["totalTokens"].(float64)
	if n <= 0 {
		t.Errorf("totalTokens: %s", body)
	}
	if b, _ := json.Marshal(m); string(b) != body {
		t.Errorf("not compact JSON: %s", body)
	}
	code, wrapped := gmnPost(t, "/v1beta/models/fake/m1:countTokens",
		`{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello there, count me please"}]}]}}`)
	if code != 200 || wrapped != body {
		t.Errorf("wrapped request should count the same: %d %s", code, wrapped)
	}
}

func TestGmnModelList(t *testing.T) {
	gmnSetup(t, provider.Chat, &gmnFake{t: t})
	code, body := gmnGet(t, "/v1beta/models")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	found := false
	for _, m := range out.Models {
		if m.Name == "models/fake/m1" {
			found = true
			want := "generateContent streamGenerateContent countTokens"
			if got := strings.Join(m.SupportedGenerationMethods, " "); got != want {
				t.Errorf("methods %q, want %q", got, want)
			}
		}
	}
	if !found {
		t.Errorf("models/fake/m1 not listed: %s", body)
	}
}

func TestGmnUnknownModelIsNotFound(t *testing.T) {
	gmnSetup(t, provider.Chat, &gmnFake{t: t})
	code, body := gmnPost(t, "/v1beta/models/nope/x:generateContent", `{"contents":[]}`)
	if code != 404 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.HasPrefix(body, `{"error":{"code":404,`) || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("error shape: %s", body)
	}
}

func TestGmnRootAndNotFoundMentionV1beta(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if _, body := gmnGet(t, "/"); !strings.Contains(body, "v1beta") {
		t.Errorf("root info lacks v1beta: %s", body)
	}
	code, body := gmnGet(t, "/nothing-here")
	if code != 404 || !strings.Contains(body, "v1beta") {
		t.Errorf("404 hint lacks v1beta: %d %s", code, body)
	}
}

func TestGmnCallIsRecorded(t *testing.T) {
	f := &gmnFake{t: t, reply: gmnSSE(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	gmnSetup(t, provider.Chat, f)
	srv := New()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1beta/models/fake/m1:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)))
	calls := srv.Recent()
	if len(calls) != 1 || calls[0].From != provider.Gemini || calls[0].Status != 200 || calls[0].Model != "fake/m1" || calls[0].Provider != "fake" {
		t.Errorf("call log: %+v", calls)
	}
}

func TestGmnThinkingBudgets(t *testing.T) {
	cases := []struct {
		budget   string
		thinking bool
		effort   string
	}{
		{"-1", true, "medium"},
		{"0", false, ""},
		{"2000", true, "low"},
		{"30000", true, "xhigh"},
	}
	for _, c := range cases {
		r, err := parseGemini([]byte(`{"contents":[],"generationConfig":{"thinkingConfig":{"thinkingBudget":` + c.budget + `}}}`))
		if err != nil {
			t.Fatal(err)
		}
		if r.Thinking != c.thinking || r.Effort != c.effort {
			t.Errorf("budget %s: thinking %v effort %q, want %v %q", c.budget, r.Thinking, r.Effort, c.thinking, c.effort)
		}
	}
}

func TestGmnToolChoiceMapping(t *testing.T) {
	decl := `"tools":[{"functionDeclarations":[{"name":"a"},{"name":"b"}]}]`
	cases := map[string]string{
		`{"mode":"NONE"}`:                                  "none",
		`{"mode":"AUTO"}`:                                  "auto",
		`{"mode":"ANY"}`:                                   "required",
		`{"mode":"ANY","allowedFunctionNames":["b","a"]}`:  "required",
		`{"mode":"ANY","allowedFunctionNames":["b"]}`:      "name:b",
	}
	for cfg, want := range cases {
		r, err := parseGemini([]byte(`{"contents":[],` + decl + `,"toolConfig":{"functionCallingConfig":` + cfg + `}}`))
		if err != nil {
			t.Fatal(err)
		}
		if r.ToolChoice != want {
			t.Errorf("%s: got %q want %q", cfg, r.ToolChoice, want)
		}
	}
}

func TestGmnSchemaTypesLowerCased(t *testing.T) {
	got := string(lowerSchema(json.RawMessage(`{"type":"OBJECT","properties":{"type":{"type":"STRING"},"xs":{"type":"ARRAY","items":{"type":"INTEGER"}}}}`)))
	want := `{"properties":{"type":{"type":"string"},"xs":{"items":{"type":"integer"},"type":"array"}},"type":"object"}`
	if got != want {
		t.Errorf("lowerSchema:\n got %s\nwant %s", got, want)
	}
}

func TestGmnFunctionCallWithoutIDIsPairedByName(t *testing.T) {
	r, err := parseGemini([]byte(`{"contents":[
	  {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}},{"functionCall":{"id":"keep","name":"write","args":{}}}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"ok"}}},{"functionResponse":{"id":"keep","name":"write","response":{"output":"done"}}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 2 {
		t.Fatalf("messages: %+v", r.Messages)
	}
	calls := r.Messages[0].Parts
	results := r.Messages[1].Parts
	if calls[0].ID == "" || results[0].CallID != calls[0].ID || results[1].CallID != "keep" || calls[1].ID != "keep" {
		t.Errorf("pairing: calls %+v results %+v", calls, results)
	}
}

// A thought that comes back as its signature alone (no text) is carried into
// the history rather than dropped, so the model can resume its reasoning.
func TestGmnThoughtWithOnlySignatureIsKept(t *testing.T) {
	r, err := parseGemini([]byte(`{"contents":[
	  {"role":"model","parts":[{"thought":true,"thoughtSignature":"sig-1"},{"text":"answer"}]},
	  {"role":"user","parts":[{"text":"next"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) < 1 {
		t.Fatalf("messages: %+v", r.Messages)
	}
	parts := r.Messages[0].Parts
	if len(parts) != 2 || parts[0].Kind != Thinking || parts[0].Signature != "sig-1" || parts[0].Text != "" {
		t.Fatalf("signature-only thought lost: %+v", parts)
	}
}

// An upstream that fails mid-stream ends the Gemini stream with an error and
// no finish reason: the client must not read the cut-off reply as complete.
func TestGmnStreamUpstreamErrorEndsWithError(t *testing.T) {
	f := &gmnFake{t: t, reply: gmnSSE(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"Hi"}}]}`,
		`data: {"error":{"message":"boom"}}`)}
	gmnSetup(t, provider.Chat, f)
	code, body := gmnPost(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if strings.Contains(body, `"finishReason"`) {
		t.Errorf("a failed stream must not carry a finish reason: %s", body)
	}
	evs := gmnEvents(body)
	if len(evs) != 2 {
		t.Fatalf("want text then error, got %d events: %s", len(evs), body)
	}
	if p := gmnParts(t, gmnCandidate(t, evs[0])); len(p) != 1 || p[0]["text"] != "Hi" {
		t.Errorf("text event: %v", p)
	}
	e, ok := evs[1]["error"].(map[string]any)
	if !ok || e["message"] != "boom" || e["status"] != "UNAVAILABLE" {
		t.Errorf("error event: %v", evs[1])
	}
}

// A stream with no parts still ends with one event that carries the finish.
func TestGmnStreamWithoutPartsStillFinishes(t *testing.T) {
	f := &gmnFake{t: t, reply: gmnSSE(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	gmnSetup(t, provider.Chat, f)
	code, body := gmnPost(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := gmnEvents(body)
	if len(evs) != 1 {
		t.Fatalf("want one finish event, got %d: %s", len(evs), body)
	}
	if c := gmnCandidate(t, evs[0]); c["finishReason"] != "STOP" || len(gmnParts(t, c)) != 0 {
		t.Errorf("finish event: %v", c)
	}
	if _, ok := evs[0]["usageMetadata"]; !ok {
		t.Errorf("finish event lacks usage: %s", body)
	}
}

// A /v1beta path dial does not serve gets a Gemini-shaped 404, not an OpenAI one.
func TestGmnUnregisteredPathIsGeminiNotFound(t *testing.T) {
	for _, path := range []string{"/v1beta/models/fake/m1", "/v1beta/tunedModels", "/v1beta"} {
		code, body := gmnGet(t, path)
		if code != 404 || !strings.HasPrefix(body, `{"error":{"code":404,`) || !strings.Contains(body, `"status":"NOT_FOUND"`) || !strings.Contains(body, "v1beta") {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
}

// Every streamed event, the held-back intermediate ones included, carries the
// model and the same response id; the final event is no exception.
func TestGmnStreamEveryEventCarriesModelAndResponseID(t *testing.T) {
	f := &gmnFake{t: t, reply: gmnSSE(
		`data: {"id":"c7","model":"m1","choices":[{"delta":{"role":"assistant","content":"a"}}]}`,
		`data: {"id":"c7","choices":[{"delta":{"content":"b"}}]}`,
		`data: {"id":"c7","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	gmnSetup(t, provider.Chat, f)
	code, body := gmnPost(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := gmnEvents(body)
	if len(evs) != 2 {
		t.Fatalf("want two events, got %d: %s", len(evs), body)
	}
	first, last := evs[0], evs[1]
	if first["modelVersion"] == nil || first["modelVersion"] == "" {
		t.Errorf("intermediate event lacks modelVersion: %v", first)
	}
	if id, _ := first["responseId"].(string); id == "" {
		t.Errorf("intermediate event lacks responseId: %v", first)
	}
	if first["responseId"] != last["responseId"] || first["modelVersion"] != last["modelVersion"] {
		t.Errorf("events disagree: first %v last %v", first, last)
	}
}

// The prompt side counts cache writes as well as fresh and cache-read input,
// while cachedContentTokenCount still reports only the cache reads.
func TestGmnPromptCountsCacheWrites(t *testing.T) {
	f := &gmnFake{t: t, reply: gmnSSE(
		`data: {"type":"message_start","message":{"id":"msg_9","model":"m1","usage":{"input_tokens":4,"cache_read_input_tokens":3,"cache_creation_input_tokens":6}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`data: {"type":"message_stop"}`)}
	gmnSetup(t, provider.Anthropic, f)
	code, body := gmnPost(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	u := gmnDecode(t, body)["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(13) || u["cachedContentTokenCount"] != float64(3) || u["candidatesTokenCount"] != float64(2) {
		t.Errorf("usage: %v", u)
	}
}
