package gateway

// Acceptance checks, one per item of the feature list. They read the
// external behaviour only: what a client sends and gets back, and what the
// fake upstream received.

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

const zvAccUserHi = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`

var zvAccChatReply = sse(
	`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"Hello"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"content":" there"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
	`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":4}}`,
	`data: [DONE]`)

var zvAccResponsesReply = "event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","delta":"Hello"}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","delta":" there"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"m1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello there"}]}],"usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}}}` + "\n\n"

var zvAccAnthropicReply = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":12,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" there"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// zvAccSSEData returns the JSON of every data: line in a Gemini SSE body, in order.
func zvAccSSEData(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			t.Fatalf("bad SSE event %q: %v", data, err)
		}
		out = append(out, m)
	}
	return out
}

// zvAccObj asserts v is a JSON object and returns it.
func zvAccObj(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("want object, got %T: %v", v, v)
	}
	return m
}

// zvAccArr asserts v is a JSON array and returns it.
func zvAccArr(t *testing.T, v any) []any {
	t.Helper()
	a, ok := v.([]any)
	if !ok {
		t.Fatalf("want array, got %T: %v", v, v)
	}
	return a
}

// 1. generateContent answers in Gemini shape from each kind of upstream.
func TestZv_AccGenerateContentThroughEachUpstream(t *testing.T) {
	cases := []struct {
		name  string
		proto provider.Protocol
		reply string
		path  string
	}{
		{"chat", provider.Chat, zvAccChatReply, "/v1/chat/completions"},
		{"responses", provider.Responses, zvAccResponsesReply, "/v1/responses"},
		{"anthropic", provider.Anthropic, zvAccAnthropicReply, "/v1/messages"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{t: t, reply: c.reply}
			setup(t, c.proto, f)
			code, body := post(t, "/v1beta/models/fake/m1:generateContent", zvAccUserHi)
			if code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			if f.path != c.path {
				t.Errorf("upstream path %s, want %s", f.path, c.path)
			}
			out := zvAccDecode(t, body)
			parts := zvAccCandidateParts(t, out)
			if len(parts) != 1 || parts[0].(map[string]any)["text"] != "Hello there" {
				t.Errorf("parts: %v", parts)
			}
			if zvAccFirstCandidate(t, out)["finishReason"] != "STOP" {
				t.Errorf("finishReason: %v", zvAccFirstCandidate(t, out))
			}
			if out["usageMetadata"] == nil {
				t.Errorf("no usageMetadata: %s", body)
			}
			if !strings.Contains(body, `"finishReason":"STOP"`) {
				t.Errorf("reply is not compact JSON: %s", body)
			}
		})
	}
}

// 2. streamGenerateContent?alt=sse emits thinking, text, then the call, with ids and usage.
func TestZv_AccStreamPartsInOrderWithIDsAndUsage(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c2","model":"m1","choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"content":"Answer"}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c2","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c2","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":3}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", zvAccUserHi)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := zvAccSSEData(t, body)
	if len(evs) == 0 {
		t.Fatalf("no events: %s", body)
	}
	var parts []map[string]any
	for _, ev := range evs {
		cand := zvAccObj(t, zvAccArr(t, ev["candidates"])[0])
		for _, p := range zvAccArr(t, zvAccObj(t, cand["content"])["parts"]) {
			parts = append(parts, zvAccObj(t, p))
		}
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 3: %s", len(parts), body)
	}
	if parts[0]["thought"] != true || parts[0]["text"] != "think" {
		t.Errorf("first part should be thinking: %v", parts[0])
	}
	if parts[1]["text"] != "Answer" || parts[1]["thought"] == true {
		t.Errorf("second part should be text: %v", parts[1])
	}
	fc := zvAccObj(t, parts[2]["functionCall"])
	args := zvAccObj(t, fc["args"])
	if fc["id"] != "call_9" || fc["name"] != "read" || args["path"] != "a.go" {
		t.Errorf("functionCall = %v", fc)
	}
	last := evs[len(evs)-1]
	lastCand := zvAccObj(t, zvAccArr(t, last["candidates"])[0])
	if lastCand["finishReason"] != "STOP" {
		t.Errorf("last event finishReason: %v", lastCand)
	}
	um := zvAccObj(t, last["usageMetadata"])
	if um["thoughtsTokenCount"] != float64(3) {
		t.Errorf("usageMetadata: %v", um)
	}
}

// 3. systemInstruction, uppercase schema types, call/response history and
// thinkingBudget -1 reach the upstream in its own terms.
const zvAccToolReq = `{
 "systemInstruction":{"parts":[{"text":"be brief"}]},
 "contents":[
  {"role":"user","parts":[{"text":"read a.go"}]},
  {"role":"model","parts":[{"functionCall":{"id":"call_1","name":"read","args":{"path":"x"}}}]},
  {"role":"user","parts":[{"functionResponse":{"id":"call_1","name":"read","response":{"output":"package x"}}}]}],
 "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
   "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
 "generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`

func TestZv_AccRequestTranslationToChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: zvAccChatReply}
	setup(t, provider.Chat, f)
	if code, body := post(t, "/v1beta/models/fake/m1:generateContent", zvAccToolReq); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body: %v", err)
	}
	msgs := zvAccArr(t, up["messages"])
	sys := zvAccObj(t, msgs[0])
	if sys["role"] != "system" || sys["content"] != "be brief" {
		t.Errorf("first message = %v", sys)
	}
	var sawCall, sawResult bool
	for _, m := range msgs[1:] {
		mm := zvAccObj(t, m)
		if tcs, ok := mm["tool_calls"].([]any); ok && len(tcs) > 0 {
			if zvAccObj(t, tcs[0])["id"] == "call_1" {
				sawCall = true
			}
		}
		if mm["role"] == "tool" && mm["tool_call_id"] == "call_1" {
			sawResult = true
		}
	}
	if !sawCall || !sawResult {
		t.Errorf("call/result not paired as call_1: %v", msgs)
	}
	tools := zvAccArr(t, up["tools"])
	params := zvAccObj(t, zvAccObj(t, zvAccObj(t, tools[0])["function"])["parameters"])
	if params["type"] != "object" || zvAccObj(t, zvAccObj(t, params["properties"])["path"])["type"] != "string" {
		t.Errorf("schema types not lower-cased: %v", params)
	}
	if up["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort = %v, want medium", up["reasoning_effort"])
	}
}

func TestZv_AccRequestTranslationToResponsesAndAnthropicUpstreams(t *testing.T) {
	f := &fake{t: t, reply: zvAccResponsesReply}
	setup(t, provider.Responses, f)
	if code, body := post(t, "/v1beta/models/fake/m1:generateContent", zvAccToolReq); code != 200 {
		t.Fatalf("responses upstream: status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if _, ok := up["reasoning"]; !ok {
		t.Errorf("responses upstream lacks reasoning: %v", up)
	}

	fa := &fake{t: t, reply: zvAccAnthropicReply}
	setup(t, provider.Anthropic, fa)
	if code, body := post(t, "/v1beta/models/fake/m1:generateContent", zvAccToolReq); code != 200 {
		t.Fatalf("anthropic upstream: status %d: %s", code, body)
	}
	up = nil
	json.Unmarshal(fa.got, &up)
	if _, ok := up["system"]; !ok {
		t.Errorf("anthropic upstream lacks system: %v", up)
	}
}

// 4. unknown models, countTokens, and the model list.
func TestZv_AccUnknownModelCountTokensAndList(t *testing.T) {
	f := &fake{t: t, reply: zvAccChatReply}
	setup(t, provider.Chat, f)

	code, body := post(t, "/v1beta/models/nope/x:generateContent", zvAccUserHi)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("unknown model: %d %s", code, body)
	}

	code, body = post(t, "/v1beta/models/fake/m1:countTokens", zvAccUserHi)
	if code != 200 || !strings.Contains(body, `"totalTokens":`) || strings.Contains(body, `"totalTokens": `) {
		t.Errorf("countTokens: %d %s", code, body)
	}
	if n := zvAccDecode(t, body)["totalTokens"]; n == nil {
		t.Errorf("countTokens has no totalTokens: %s", body)
	}
	wrapped := `{"generateContentRequest":{"model":"models/fake/m1","contents":[{"role":"user","parts":[{"text":"hello there"}]}]}}`
	code, body = post(t, "/v1beta/models/fake/m1:countTokens", wrapped)
	if code != 200 || !strings.Contains(body, `"totalTokens":`) {
		t.Errorf("wrapped countTokens: %d %s", code, body)
	}

	code, body = zvAccGetReq(t, "/v1beta/models")
	if code != 200 {
		t.Fatalf("models: %d %s", code, body)
	}
	models := zvAccArr(t, zvAccDecode(t, body)["models"])
	found := false
	for _, m := range models {
		mm := zvAccObj(t, m)
		if mm["name"] == "models/fake/m1" {
			found = true
			if mm["supportedGenerationMethods"] == nil {
				t.Errorf("no supportedGenerationMethods: %v", mm)
			}
		}
	}
	if !found {
		t.Errorf("models/fake/m1 not listed: %s", body)
	}
}

// 6 (gateway side). A signed-in Codex login serves codex/<model> through all
// three APIs and the Gemini API.
func TestZv_AccCodexModelThroughEveryAPI(t *testing.T) {
	zvAccCodexHome(t)
	up, _ := zvAccStartCodex(t)
	cases := []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`},
		{"/v1/messages", `{"model":"codex/gpt-5.5","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1beta/models/codex/gpt-5.5:generateContent", zvAccUserHi},
	}
	for _, c := range cases {
		code, body := post(t, c.path, c.body)
		if code != 200 {
			t.Errorf("%s: status %d: %s", c.path, code, body)
			continue
		}
		if !strings.Contains(body, "hello") {
			t.Errorf("%s: reply lacks the text: %s", c.path, body)
		}
	}
	if up.path != "/backend-api/codex/responses" {
		t.Errorf("upstream path %s", up.path)
	}
}

// 7. Non-streamed Responses to Codex is aggregated; streamed is forwarded;
// both carry the account token and no sampling or output-limit fields.
func TestZv_AccCodexResponsesRules(t *testing.T) {
	access := zvAccCodexHome(t)
	up, _ := zvAccStartCodex(t)

	code, body := post(t, "/v1/responses",
		`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":50,"temperature":0.2,"top_p":0.9}`)
	if code != 200 {
		t.Fatalf("non-stream: %d %s", code, body)
	}
	out := zvAccDecode(t, body)
	if out["object"] != "response" || out["usage"] == nil {
		t.Errorf("non-stream reply lacks object/usage: %s", body)
	}
	if !strings.Contains(body, `"hello"`) {
		t.Errorf("non-stream reply lacks text: %s", body)
	}
	if up.head.Get("Authorization") != "Bearer "+access || up.head.Get("chatgpt-account-id") != "acct-1" {
		t.Errorf("headers: %v", up.head)
	}
	zvAccCheckCodexBody(t, up)

	code, body = post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":true,"max_output_tokens":50,"temperature":0.2}`)
	if code != 200 || !strings.Contains(body, "response.output_text.delta") {
		t.Fatalf("stream: %d %s", code, body)
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := up.body[k]; ok {
			t.Errorf("streamed upstream body carries %s", k)
		}
	}
	if up.body["stream"] != true {
		t.Errorf("streamed upstream body: %v", up.body)
	}
}

// 8 (gateway side). The Copilot account's built-in models are listed under copilot/.
func TestZv_AccCopilotListedUnderCopilotPrefix(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(h, ".claude"))
	dir := filepath.Join(h, "config", "github-copilot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "apps.json"),
		[]byte(`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_test"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, body := zvAccGetReq(t, "/v1/models")
	if code != 200 {
		t.Fatalf("models: %d", code)
	}
	if !strings.Contains(body, `"id":"copilot/`) {
		t.Errorf("no copilot/ models listed: %s", body)
	}
	if strings.Contains(body, `"id":"copilot/auto"`) {
		t.Errorf("copilot/auto must not be listed: %s", body)
	}
}

// 10 (gateway side). A signed-in Claude Code is neither a provider nor a model,
// and its token never reaches the model list.
func TestZv_AccClaudeNotInModelList(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(h, ".claude"))
	const secret = "sk-ant-oat-SECRET-TOKEN-987"
	dir := filepath.Join(h, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"),
		[]byte(`{"claudeAiOauth":{"accessToken":"`+secret+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fake{t: t, reply: zvAccChatReply}
	setup(t, provider.Chat, f)
	code, body := zvAccGetReq(t, "/v1/models")
	if code != 200 {
		t.Fatalf("models: %d", code)
	}
	if strings.Contains(strings.ToLower(body), "claude") || strings.Contains(body, secret) {
		t.Errorf("model list leaks claude: %s", body)
	}
}

// Local copies of helpers this file needs (kept here so the file stands alone).

func zvAccAcctJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func zvAccCodexHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(h, ".claude"))
	access := zvAccAcctJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": zvAccAcctJWT(map[string]any{"email": "me@example.com"}), "access_token": access,
		"refresh_token": "rt", "account_id": "acct-1"}})
	if err := os.MkdirAll(filepath.Join(h, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h, ".codex", "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return access
}

type zvAccCodexUpstream struct {
	path  string
	head  http.Header
	body  map[string]any
	reply string
}

func (f *zvAccCodexUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.path, f.head = r.URL.Path, r.Header
	raw, _ := io.ReadAll(r.Body)
	f.body = map[string]any{}
	json.Unmarshal(raw, &f.body)
	w.Header()["Content-Type"] = nil
	io.WriteString(w, f.reply)
}

const zvAccCodexReply = "event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","delta":"hel"}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","delta":"lo"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}` + "\n\n"

func zvAccStartCodex(t *testing.T) (*zvAccCodexUpstream, *httptest.Server) {
	t.Helper()
	up := &zvAccCodexUpstream{reply: zvAccCodexReply}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	old := provider.CodexBase
	provider.CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
	gw := httptest.NewServer(New().Handler())
	t.Cleanup(gw.Close)
	return up, gw
}

func zvAccCheckCodexBody(t *testing.T, up *zvAccCodexUpstream) {
	t.Helper()
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := up.body[k]; ok {
			t.Fatalf("upstream body must not carry %s: %v", k, up.body)
		}
	}
	if up.body["stream"] != true || up.body["store"] != false {
		t.Fatalf("stream/store wrong: %v", up.body)
	}
	if up.body["model"] != "gpt-5.5" {
		t.Fatalf("model = %v, want gpt-5.5", up.body["model"])
	}
	if _, ok := up.body["input"].([]any); !ok {
		t.Fatalf("input must be a list: %v", up.body["input"])
	}
}

func zvAccGetReq(t *testing.T, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

func zvAccDecode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %v: %s", err, s)
	}
	return m
}

func zvAccFirstCandidate(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	return m["candidates"].([]any)[0].(map[string]any)
}

func zvAccCandidateParts(t *testing.T, m map[string]any) []any {
	t.Helper()
	return zvAccFirstCandidate(t, m)["content"].(map[string]any)["parts"].([]any)
}
