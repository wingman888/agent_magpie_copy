package gateway

// Black-box checks of the acceptance criteria, written against the external
// behaviour only: a fake upstream, a temp HOME and config, and the HTTP handler.

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// bb_up is a fake upstream: it records what it was sent and answers with a script.
type bb_up struct {
	mu     sync.Mutex
	path   string
	head   http.Header
	body   []byte
	reply  string
	noType bool // answer without any Content-Type header
}

func (u *bb_up) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.path, u.head, u.body = r.URL.Path, r.Header.Clone(), b
	u.mu.Unlock()
	if u.noType {
		w.Header()["Content-Type"] = nil
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	io.WriteString(w, u.reply)
}

// bb_sent is the JSON body the upstream received.
func bb_sent(t *testing.T, u *bb_up) map[string]any {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal(u.body, &m); err != nil {
		t.Fatalf("upstream body is not JSON: %v\n%s", err, u.body)
	}
	return m
}

func bb_sse(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n\n")
	}
	return b.String()
}

// bb_env gives the test its own home and dial/agent config directories.
func bb_env(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

// bb_provider registers a fake upstream as provider id, speaking one protocol,
// with the single model m1 exposed.
func bb_provider(t *testing.T, id string, speaks provider.Protocol, u *bb_up) {
	t.Helper()
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: id, Name: id, Key: "k", Models: []string{"m1"}}
	switch speaks {
	case provider.Chat:
		p.Chat = srv.URL + "/v1"
	case provider.Responses:
		p.Responses = srv.URL + "/v1"
	default:
		p.Anthropic = srv.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// bb_serve sends one request through a fresh gateway.
func bb_serve(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func bb_obj(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return m
}

// bb_parts is the parts of the first candidate of a Gemini reply or chunk.
func bb_parts(t *testing.T, v map[string]any) []map[string]any {
	t.Helper()
	cs, _ := v["candidates"].([]any)
	if len(cs) == 0 {
		t.Fatalf("no candidates in %v", v)
	}
	cand, _ := cs[0].(map[string]any)
	content, _ := cand["content"].(map[string]any)
	raw, _ := content["parts"].([]any)
	var out []map[string]any
	for _, p := range raw {
		if m, ok := p.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// bb_chunks decodes the data events of an SSE body.
func bb_chunks(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	readSSE(strings.NewReader(body), func(_, data string) error {
		var m map[string]any
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			t.Fatalf("bad event %q: %v", data, err)
		}
		out = append(out, m)
		return nil
	})
	return out
}

// bb_messages is the messages array a Chat upstream received.
func bb_messages(t *testing.T, sent map[string]any) []map[string]any {
	t.Helper()
	raw, _ := sent["messages"].([]any)
	var out []map[string]any
	for _, m := range raw {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no messages in %v", sent)
	}
	return out
}

// ---- acceptance 1: generateContent over every upstream protocol ----------------

func TestBB_GenerateContentOverEveryUpstream(t *testing.T) {
	chat := bb_sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"Hello"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)
	responses := bb_sse(
		`data: {"type":"response.created","response":{"id":"resp_1","model":"m1"}}`,
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5,"output_tokens_details":{"reasoning_tokens":2}}}}`)
	anthropic := bb_sse(
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"m1\",\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}")
	cases := []struct {
		name   string
		speaks provider.Protocol
		reply  string
		path   string
	}{
		{"chat", provider.Chat, chat, "/v1/chat/completions"},
		{"responses", provider.Responses, responses, "/v1/responses"},
		{"anthropic", provider.Anthropic, anthropic, "/v1/messages"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bb_env(t)
			up := &bb_up{reply: c.reply}
			bb_provider(t, "fx", c.speaks, up)
			rec := bb_serve(t, "POST", "/v1beta/models/fx/m1:generateContent",
				`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if up.path != c.path {
				t.Errorf("upstream path %q, want %q", up.path, c.path)
			}
			body := rec.Body.String()
			out := bb_obj(t, body)
			ps := bb_parts(t, out)
			if len(ps) != 1 || ps[0]["text"] != "Hello" {
				t.Errorf("parts = %v", ps)
			}
			cand := out["candidates"].([]any)[0].(map[string]any)
			if cand["finishReason"] != "STOP" || !strings.Contains(body, `"finishReason":"STOP"`) {
				t.Errorf("finishReason: %s", body)
			}
			um, ok := out["usageMetadata"].(map[string]any)
			if !ok {
				t.Fatalf("no usageMetadata: %s", body)
			}
			if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(5) {
				t.Errorf("usage = %v", um)
			}
			if _, ok := um["thoughtsTokenCount"]; !ok {
				t.Errorf("no thoughtsTokenCount: %v", um)
			}
		})
	}
}

// ---- acceptance 2: streaming order and the final event --------------------------

func TestBB_StreamOrderThoughtTextCall(t *testing.T) {
	bb_env(t)
	up := &bb_up{reply: bb_sse(
		`data: {"type":"response.created","response":{"id":"resp_2","model":"m1"}}`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"hmm"}`,
		`data: {"type":"response.output_text.delta","delta":"Hi"}`,
		`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"read","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","delta":"{\"path\":"}`,
		`data: {"type":"response.function_call_arguments.delta","delta":"\"a.go\",\"n\":2}"}`,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"read","arguments":"{\"path\":\"a.go\",\"n\":2}"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_2","status":"completed","output":[{"type":"function_call"}],"usage":{"input_tokens":4,"output_tokens":6,"output_tokens_details":{"reasoning_tokens":3}}}}`)}
	bb_provider(t, "fx", provider.Responses, up)
	rec := bb_serve(t, "POST", "/v1beta/models/fx/m1:streamGenerateContent?alt=sse",
		`{"contents":[{"role":"user","parts":[{"text":"read"}]}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	evs := bb_chunks(t, rec.Body.String())
	if len(evs) != 3 {
		t.Fatalf("%d events, want 3 (thought, text, call): %s", len(evs), rec.Body.String())
	}
	if p := bb_parts(t, evs[0]); len(p) != 1 || p[0]["text"] != "hmm" || p[0]["thought"] != true {
		t.Errorf("event 0 part = %v", p)
	}
	if p := bb_parts(t, evs[1]); len(p) != 1 || p[0]["text"] != "Hi" {
		t.Errorf("event 1 part = %v", p)
	}
	last := evs[2]
	ps := bb_parts(t, last)
	if len(ps) != 1 {
		t.Fatalf("last event parts = %v", ps)
	}
	fc, _ := ps[0]["functionCall"].(map[string]any)
	args, _ := fc["args"].(map[string]any)
	if fc["id"] != "call_abc" || fc["name"] != "read" || args["path"] != "a.go" || args["n"] != float64(2) {
		t.Errorf("function call = %v", fc)
	}
	cand := last["candidates"].([]any)[0].(map[string]any)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason = %v", cand["finishReason"])
	}
	um, _ := last["usageMetadata"].(map[string]any)
	if um["thoughtsTokenCount"] != float64(3) || um["promptTokenCount"] != float64(4) {
		t.Errorf("usageMetadata = %v", um)
	}
}

// ---- acceptance 3: request translation -----------------------------------------

func TestBB_RequestTranslationToChat(t *testing.T) {
	bb_env(t)
	up := &bb_up{reply: bb_sse(
		`data: {"id":"c1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	bb_provider(t, "fx", provider.Chat, up)
	req := `{
	 "systemInstruction":{"parts":[{"text":"Be brief."}]},
	 "contents":[
	  {"role":"user","parts":[{"text":"read a.go"},{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}}]},
	  {"role":"model","parts":[{"functionCall":{"id":"call_7","name":"read","args":{"path":"a.go"}}}]},
	  {"role":"user","parts":[{"functionResponse":{"id":"call_7","name":"read","response":{"output":"package a"}}}]}
	 ],
	 "tools":[{"functionDeclarations":[{"name":"read","description":"read a file","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
	 "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
	 "generationConfig":{"maxOutputTokens":100,"temperature":0.5,"topP":0.9,"stopSequences":["END"],"thinkingConfig":{"thinkingBudget":-1}}
	}`
	rec := bb_serve(t, "POST", "/v1beta/models/fx/m1:generateContent", req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	sent := bb_sent(t, up)
	msgs := bb_messages(t, sent)
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "Be brief." {
		t.Errorf("system message = %v", msgs[0])
	}
	parts, ok := msgs[1]["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("user message with image = %v", msgs[1])
	}
	img, _ := parts[1].(map[string]any)
	iu, _ := img["image_url"].(map[string]any)
	if img["type"] != "image_url" || iu["url"] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Errorf("image part = %v", img)
	}
	var call map[string]any
	for _, m := range msgs {
		if raw, ok := m["tool_calls"].([]any); ok && len(raw) > 0 {
			call = raw[0].(map[string]any)
		}
	}
	if call == nil || call["id"] != "call_7" {
		t.Fatalf("no assistant tool_calls with id call_7: %v", msgs)
	}
	var tool map[string]any
	for _, m := range msgs {
		if m["role"] == "tool" {
			tool = m
		}
	}
	if tool == nil || tool["tool_call_id"] != "call_7" || tool["content"] != "package a" {
		t.Errorf("tool message = %v", tool)
	}
	tools := sent["tools"].([]any)
	params := tools[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	if params["type"] != "object" || params["properties"].(map[string]any)["path"].(map[string]any)["type"] != "string" {
		t.Errorf("schema not lower-cased: %v", params)
	}
	if tc, _ := sent["tool_choice"].(map[string]any); tc == nil || tc["function"].(map[string]any)["name"] != "read" {
		t.Errorf("tool_choice = %v", sent["tool_choice"])
	}
	if sent["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort = %v, want medium", sent["reasoning_effort"])
	}
	if sent["max_tokens"] != float64(100) || sent["temperature"] != 0.5 || sent["top_p"] != 0.9 {
		t.Errorf("sampling = max_tokens %v temperature %v top_p %v", sent["max_tokens"], sent["temperature"], sent["top_p"])
	}
	if stop, _ := sent["stop"].([]any); len(stop) != 1 || stop[0] != "END" {
		t.Errorf("stop = %v", sent["stop"])
	}
}

func TestBB_FunctionResponseWithoutIDPairsByName(t *testing.T) {
	bb_env(t)
	up := &bb_up{reply: bb_sse(`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`)}
	bb_provider(t, "fx", provider.Chat, up)
	req := `{"contents":[
	  {"role":"user","parts":[{"text":"go"}]},
	  {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"a"}}}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"A"}}}]}]}`
	if rec := bb_serve(t, "POST", "/v1beta/models/fx/m1:generateContent", req); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	msgs := bb_messages(t, bb_sent(t, up))
	var callID, toolID string
	for _, m := range msgs {
		if raw, ok := m["tool_calls"].([]any); ok && len(raw) > 0 {
			callID, _ = raw[0].(map[string]any)["id"].(string)
		}
		if m["role"] == "tool" {
			toolID, _ = m["tool_call_id"].(string)
		}
	}
	if callID == "" || toolID != callID {
		t.Errorf("call id %q, tool_call_id %q: must be equal and non-empty", callID, toolID)
	}
}

func TestBB_ResponsesUpstreamGetsMediumEffort(t *testing.T) {
	bb_env(t)
	up := &bb_up{reply: bb_sse(
		`data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[],"usage":{}}}`)}
	bb_provider(t, "fx", provider.Responses, up)
	req := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`
	if rec := bb_serve(t, "POST", "/v1beta/models/fx/m1:generateContent", req); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	sent := bb_sent(t, up)
	r, _ := sent["reasoning"].(map[string]any)
	if r["effort"] != "medium" {
		t.Errorf("reasoning = %v, want effort medium", sent["reasoning"])
	}
}

func TestBB_AnthropicUpstreamSystemThinkingAndCarryBack(t *testing.T) {
	bb_env(t)
	up := &bb_up{reply: bb_sse(
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"m1\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}")}
	bb_provider(t, "fx", provider.Anthropic, up)
	req := `{"systemInstruction":{"parts":[{"text":"Be brief."}]},
	 "contents":[{"role":"user","parts":[{"text":"hi"}]},
	   {"role":"model","parts":[{"text":"hmm","thought":true,"thoughtSignature":"sig1"},{"text":"ok"}]},
	   {"role":"user","parts":[{"text":"again"}]}],
	 "generationConfig":{"maxOutputTokens":321,"thinkingConfig":{"thinkingBudget":-1}}}`
	if rec := bb_serve(t, "POST", "/v1beta/models/fx/m1:generateContent", req); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	sent := bb_sent(t, up)
	if sent["system"] != "Be brief." {
		t.Errorf("system = %v", sent["system"])
	}
	if mt, _ := sent["max_tokens"].(float64); mt < 321 {
		t.Errorf("max_tokens = %v, want at least 321", sent["max_tokens"])
	}
	th, _ := sent["thinking"].(map[string]any)
	if th == nil || th["type"] != "enabled" {
		t.Errorf("thinking = %v", sent["thinking"])
	}
	msgs := bb_messages(t, sent)
	found := false
	if blocks, ok := msgs[1]["content"].([]any); ok {
		for _, b := range blocks {
			m, _ := b.(map[string]any)
			if m["type"] == "thinking" && m["thinking"] == "hmm" && m["signature"] == "sig1" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("history thinking not carried back: %v", msgs[1])
	}
}

// ---- acceptance 4: errors, countTokens, models, root ----------------------------

func TestBB_GeminiErrorsCountTokensModelsRoot(t *testing.T) {
	bb_env(t)
	bb_provider(t, "fx", provider.Chat, &bb_up{reply: ""})

	rec := bb_serve(t, "POST", "/v1beta/models/nope/x:generateContent", `{"contents":[]}`)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"status":"NOT_FOUND"`) {
		t.Errorf("unknown model: %d %s", rec.Code, rec.Body.String())
	}
	rec = bb_serve(t, "POST", "/v1beta/models/fx/m1:frobnicate", `{}`)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"status":"NOT_FOUND"`) {
		t.Errorf("unknown method: %d %s", rec.Code, rec.Body.String())
	}

	rec = bb_serve(t, "POST", "/v1beta/models/fx/m1:countTokens", `{"contents":[{"role":"user","parts":[{"text":"hello there, count me"}]}]}`)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), `{"totalTokens":`) {
		t.Errorf("countTokens: %d %s", rec.Code, rec.Body.String())
	}
	if n, _ := bb_obj(t, rec.Body.String())["totalTokens"].(float64); n <= 0 {
		t.Errorf("totalTokens = %v", n)
	}
	rec = bb_serve(t, "POST", "/v1beta/models/fx/m1:countTokens",
		`{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello there, count me"}]}]}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"totalTokens":`) {
		t.Errorf("countTokens wrapped: %d %s", rec.Code, rec.Body.String())
	}

	rec = bb_serve(t, "GET", "/v1beta/models", "")
	models, _ := bb_obj(t, rec.Body.String())["models"].([]any)
	found := false
	for _, m := range models {
		mm, _ := m.(map[string]any)
		if mm["name"] == "models/fx/m1" {
			ms, _ := mm["supportedGenerationMethods"].([]any)
			found = len(ms) > 0
		}
	}
	if !found {
		t.Errorf("GET /v1beta/models = %s", rec.Body.String())
	}

	rec = bb_serve(t, "GET", "/", "")
	if !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("root does not mention v1beta: %s", rec.Body.String())
	}
	rec = bb_serve(t, "GET", "/nope", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("404 hint: %d %s", rec.Code, rec.Body.String())
	}
}

// ---- acceptance 6/7: Codex as an account provider -------------------------------

// bb_jwt makes an unsigned JWT with the given payload, as Codex stores them.
func bb_jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// bb_codexLogin writes a ChatGPT login into the home's Codex file and returns the access token.
func bb_codexLogin(t *testing.T, home string, exp time.Time) string {
	t.Helper()
	access := bb_jwt(map[string]any{"exp": exp.Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
	id := bb_jwt(map[string]any{"email": "ada@example.com"})
	b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil,
		"tokens": map[string]any{"id_token": id, "access_token": access, "refresh_token": "rt", "account_id": "acct-1"}})
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return access
}

// bb_codexBackend points the ChatGPT Codex backend at a fake for the test.
func bb_codexBackend(t *testing.T, u *bb_up) {
	t.Helper()
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	old := provider.CodexBase
	provider.CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
}

const bb_codexReply = `data: {"type":"response.created","response":{"id":"resp_9"}}`

func bb_codexCompleted() string {
	return `data: {"type":"response.output_text.delta","delta":"Hi"}` + "\n\n" +
		`data: {"type":"response.completed","response":{"id":"resp_9","object":"response","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":1}}}`
}

func TestBB_CodexNonStreamResponsesIsCollected(t *testing.T) {
	home := bb_env(t)
	access := bb_codexLogin(t, home, time.Now().Add(time.Hour))
	up := &bb_up{reply: bb_sse(bb_codexReply, bb_codexCompleted())}
	bb_codexBackend(t, up)
	rec := bb_serve(t, "POST", "/v1/responses",
		`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":16,"temperature":0.2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	out := bb_obj(t, rec.Body.String())
	if out["id"] != "resp_9" || out["status"] != "completed" {
		t.Errorf("client got %s, want the completed response object", rec.Body.String())
	}
	if up.path != "/backend-api/codex/responses" {
		t.Errorf("upstream path %q", up.path)
	}
	if got := up.head.Get("Authorization"); got != "Bearer "+access {
		t.Errorf("Authorization = %q", got)
	}
	if got := up.head.Get("chatgpt-account-id"); got != "acct-1" {
		t.Errorf("chatgpt-account-id = %q", got)
	}
	sent := bb_sent(t, up)
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := sent[k]; ok {
			t.Errorf("upstream body has %s", k)
		}
	}
	if sent["stream"] != true || sent["store"] != false || sent["model"] != "gpt-5.5" {
		t.Errorf("upstream stream/store/model = %v/%v/%v", sent["stream"], sent["store"], sent["model"])
	}
	if _, ok := sent["input"].([]any); !ok {
		t.Errorf("input is not a list: %v", sent["input"])
	}
}

func TestBB_CodexStreamingPassesThroughWithoutContentType(t *testing.T) {
	home := bb_env(t)
	bb_codexLogin(t, home, time.Now().Add(time.Hour))
	up := &bb_up{noType: true, reply: bb_sse(bb_codexReply, bb_codexCompleted())}
	bb_codexBackend(t, up)
	rec := bb_serve(t, "POST", "/v1/responses",
		`{"model":"codex/gpt-5.5","input":"hi","stream":true,"max_output_tokens":16,"temperature":0.2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "response.completed") {
		t.Errorf("stream not relayed: %s", rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Errorf("client Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	sent := bb_sent(t, up)
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := sent[k]; ok {
			t.Errorf("streamed upstream body has %s", k)
		}
	}
}

func TestBB_CodexThroughGeminiAndModelLists(t *testing.T) {
	home := bb_env(t)
	bb_codexLogin(t, home, time.Now().Add(time.Hour))
	up := &bb_up{noType: true, reply: bb_sse(bb_codexReply, bb_codexCompleted())}
	bb_codexBackend(t, up)
	rec := bb_serve(t, "POST", "/v1beta/models/codex/gpt-5.5:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("gemini via codex: %d %s", rec.Code, rec.Body.String())
	}
	out := bb_obj(t, rec.Body.String())
	if ps := bb_parts(t, out); len(ps) != 1 || ps[0]["text"] != "Hi" {
		t.Errorf("parts = %v", ps)
	}
	if !strings.Contains(rec.Body.String(), `"finishReason":"STOP"`) {
		t.Errorf("finish = %s", rec.Body.String())
	}

	rec = bb_serve(t, "GET", "/v1/models", "")
	if !strings.Contains(rec.Body.String(), `"id":"codex/gpt-5.5"`) {
		t.Errorf("/v1/models lacks codex/gpt-5.5: %s", rec.Body.String())
	}
	rec = bb_serve(t, "GET", "/v1beta/models", "")
	if !strings.Contains(rec.Body.String(), `"name":"models/codex/gpt-5.5"`) {
		t.Errorf("/v1beta/models lacks codex/gpt-5.5: %s", rec.Body.String())
	}
}

// ---- acceptance 10 (API side): Claude never listed ------------------------------

func TestBB_ClaudeLoginNeverListedByAPI(t *testing.T) {
	home := bb_env(t)
	dir := filepath.Join(home, "claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"SECRET-TOKEN"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := bb_serve(t, "GET", "/v1/models", "")
	body := rec.Body.String()
	if strings.Contains(body, "claude/") || strings.Contains(body, "SECRET-TOKEN") {
		t.Errorf("claude leaked into /v1/models: %s", body)
	}
	if !strings.Contains(body, "已登录但不提供") {
		t.Errorf("no unavailable note for Claude: %s", body)
	}
	rec = bb_serve(t, "GET", "/v1beta/models", "")
	if strings.Contains(rec.Body.String(), "claude/") {
		t.Errorf("claude leaked into Gemini models: %s", rec.Body.String())
	}
}
