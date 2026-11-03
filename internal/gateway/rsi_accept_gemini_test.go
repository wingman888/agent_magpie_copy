package gateway_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

type upstream struct {
	got   []byte
	path  string
	head  http.Header
	reply string
	ctype string // "" = text/event-stream, "none" = no header
}

func (f *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.got, _ = io.ReadAll(r.Body)
	f.path, f.head = r.URL.Path, r.Header
	switch f.ctype {
	case "none":
		w.Header()["Content-Type"] = nil
	case "":
		w.Header().Set("Content-Type", "text/event-stream")
	default:
		w.Header().Set("Content-Type", f.ctype)
	}
	io.WriteString(w, f.reply)
}

func sseBody(lines ...string) string {
	return strings.Join(lines, "\n\n") + "\n\n"
}

func isolate(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	return home
}

func setupFake(t *testing.T, proto provider.Protocol, f *upstream) {
	t.Helper()
	isolate(t)
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
}

func doReq(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	gateway.New().Handler().ServeHTTP(rec, httptest.NewRequest(method, path, rd))
	return rec.Code, rec.Body.String()
}

func sseEvents(body string) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if d, ok := strings.CutPrefix(line, "data:"); ok {
			var m map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(d)), &m) == nil {
				out = append(out, m)
			}
		}
	}
	return out
}

const geminiReq = `{"systemInstruction":{"parts":[{"text":"be brief"}]},
  "contents":[{"role":"user","parts":[{"text":"read a.go"}]},
    {"role":"model","parts":[{"functionCall":{"id":"fc0","name":"read_file","args":{"path":"x"}}}]},
    {"role":"user","parts":[{"functionResponse":{"id":"fc0","name":"read_file","response":{"output":"package x"}}}]}],
  "tools":[{"functionDeclarations":[{"name":"read_file","description":"read","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}}}}]}],
  "generationConfig":{"maxOutputTokens":100,"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`

func TestAcceptGeminiStreamChatUpstream(t *testing.T) {
	f := &upstream{reply: sseBody(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"think"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Sure"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
	setupFake(t, provider.Chat, f)
	code, body := doReq(t, "POST", "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", geminiReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["model"] != "m1" || up["reasoning_effort"] != "medium" {
		t.Errorf("upstream: %s", f.got)
	}
	msgs, _ := up["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" || msgs[0].(map[string]any)["content"] != "be brief" ||
		msgs[3].(map[string]any)["tool_call_id"] != "fc0" || msgs[2].(map[string]any)["tool_calls"] == nil {
		t.Errorf("messages: %v", msgs)
	}
	if !strings.Contains(string(f.got), `"type":"object"`) || strings.Contains(string(f.got), `"OBJECT"`) || strings.Contains(string(f.got), `"STRING"`) {
		t.Errorf("schema types not lowercased: %s", f.got)
	}
	evs := sseEvents(body)
	var parts []map[string]any
	var finish string
	for _, e := range evs {
		c := e["candidates"].([]any)[0].(map[string]any)
		if ct, ok := c["content"].(map[string]any); ok {
			for _, p := range ct["parts"].([]any) {
				parts = append(parts, p.(map[string]any))
			}
		}
		if fr, ok := c["finishReason"]; ok {
			finish = fr.(string)
		}
	}
	if len(parts) != 3 || parts[0]["thought"] != true || parts[1]["text"] != "Sure" {
		t.Fatalf("parts: %v", parts)
	}
	fc, _ := parts[2]["functionCall"].(map[string]any)
	if fc == nil || fc["name"] != "read_file" || fc["id"] != "call_1" || fc["args"].(map[string]any)["path"] != "a.go" {
		t.Errorf("function call: %v", parts[2])
	}
	um, _ := evs[len(evs)-1]["usageMetadata"].(map[string]any)
	if finish != "STOP" || um == nil || um["promptTokenCount"] != float64(10) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("finish/usage: %s %v", finish, um)
	}

	// non-streaming
	code, body = doReq(t, "POST", "/v1beta/models/fake/m1:generateContent", geminiReq)
	if code != 200 || !strings.Contains(body, `"finishReason":"STOP"`) || !strings.Contains(body, `"name":"read_file"`) || !strings.Contains(body, "usageMetadata") {
		t.Errorf("generateContent: %d %s", code, body)
	}
}

func TestAcceptGeminiOtherUpstreams(t *testing.T) {
	// Responses upstream
	f := &upstream{reply: sseBody(
		`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":2}}}`)}
	setupFake(t, provider.Responses, f)
	code, body := doReq(t, "POST", "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 || !strings.Contains(body, "hello") || !strings.Contains(body, `"finishReason":"STOP"`) || !strings.Contains(body, "usageMetadata") {
		t.Errorf("responses upstream: %d %s", code, body)
	}

	// Anthropic upstream
	f = &upstream{reply: sseBody(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"bonjour"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		`data: {"type":"message_stop"}`)}
	setupFake(t, provider.Anthropic, f)
	code, body = doReq(t, "POST", "/v1beta/models/fake/m1:generateContent", `{"systemInstruction":{"parts":[{"text":"sys"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":50}}`)
	if code != 200 || !strings.Contains(body, "bonjour") || !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("anthropic upstream: %d %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if !strings.Contains(string(f.got), "sys") || up["max_tokens"] != float64(50) {
		t.Errorf("anthropic request: %s", f.got)
	}
}

func TestAcceptGeminiTranslationDetails(t *testing.T) {
	f := &upstream{reply: sseBody(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"{}"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setupFake(t, provider.Chat, f)

	// id-less function calls pair with their responses by name; images pass through;
	// ANY with a single allowed function forces it; JSON mode goes into the system prompt.
	req := `{"contents":[
	  {"role":"user","parts":[{"text":"look"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]},
	  {"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"a"}}}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"r":"ok"}}}]}],
	  "tools":[{"functionDeclarations":[{"name":"lookup","parameters":{"type":"OBJECT","properties":{"q":{"type":"STRING"}}}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["lookup"]}},
	  "generationConfig":{"temperature":0.4,"topP":0.9,"stopSequences":["END"],"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"answer":{"type":"STRING"}}},"thinkingConfig":{"thinkingBudget":0}}}`
	code, body := doReq(t, "POST", "/v1beta/models/fake/m1:generateContent", req)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	raw := string(f.got)
	msgs := up["messages"].([]any)
	var callID, toolCallID string
	for _, m := range msgs {
		mm := m.(map[string]any)
		if tcs, ok := mm["tool_calls"].([]any); ok && len(tcs) > 0 {
			callID, _ = tcs[0].(map[string]any)["id"].(string)
		}
		if mm["role"] == "tool" {
			toolCallID, _ = mm["tool_call_id"].(string)
		}
	}
	if callID == "" || callID != toolCallID {
		t.Errorf("call/response not paired (%q vs %q): %s", callID, toolCallID, raw)
	}
	if !strings.Contains(raw, "image_url") || !strings.Contains(raw, "AAAA") {
		t.Errorf("image lost: %s", raw)
	}
	tc, _ := up["tool_choice"].(map[string]any)
	if tc == nil || tc["function"].(map[string]any)["name"] != "lookup" {
		t.Errorf("tool_choice: %v", up["tool_choice"])
	}
	if up["temperature"] != 0.4 || up["top_p"] != 0.9 || up["stop"] == nil {
		t.Errorf("sampling: %s", raw)
	}
	sys := ""
	for _, m := range msgs {
		if mm := m.(map[string]any); mm["role"] == "system" {
			sys += fmt.Sprint(mm["content"])
		}
	}
	if !strings.Contains(strings.ToLower(sys), "json") || !strings.Contains(sys, "answer") {
		t.Errorf("JSON instruction missing from system prompt: %s", raw)
	}
	if strings.Contains(raw, `"response_format"`) && false {
		t.Log("response_format present")
	}

	// NONE mode
	code, _ = doReq(t, "POST", "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"x"}]}],
	  "tools":[{"functionDeclarations":[{"name":"lookup"}]}],"toolConfig":{"functionCallingConfig":{"mode":"NONE"}}}`)
	json.Unmarshal(f.got, &up)
	if code != 200 || up["tool_choice"] != "none" {
		t.Errorf("NONE: %d %s", code, f.got)
	}
}

func TestAcceptGeminiMiscEndpoints(t *testing.T) {
	setupFake(t, provider.Chat, &upstream{})
	code, body := doReq(t, "POST", "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("unknown model: %d %s", code, body)
	}
	code, body = doReq(t, "POST", "/v1beta/models/fake/m1:countTokens", geminiReq)
	if code != 200 || !strings.Contains(body, `"totalTokens":`) {
		t.Errorf("countTokens: %d %s", code, body)
	}
	code, body = doReq(t, "POST", "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":{"model":"models/fake/m1","contents":[{"role":"user","parts":[{"text":"hello there"}]}]}}`)
	if code != 200 || !strings.Contains(body, `"totalTokens":`) {
		t.Errorf("countTokens wrapped: %d %s", code, body)
	}
	code, body = doReq(t, "GET", "/v1beta/models", "")
	if code != 200 || !strings.Contains(body, `"name":"models/fake/m1"`) || !strings.Contains(body, "supportedGenerationMethods") {
		t.Errorf("models: %d %s", code, body)
	}
	_, body = doReq(t, "GET", "/nope/nothing", "")
	if !strings.Contains(body, "v1beta") {
		t.Errorf("404 hint lacks Gemini: %s", body)
	}
	_, body = doReq(t, "GET", "/", "")
	if !strings.Contains(body, "v1beta") {
		t.Errorf("root info lacks Gemini: %s", body)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func jwt(m map[string]any) string {
	b, _ := json.Marshal(m)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

func signInCodex(home string, t *testing.T) {
	writeJSON(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": "me@example.com"}),
			"access_token":  jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
			"refresh_token": "r", "account_id": "acct-1",
		},
	})
}

func TestAcceptCodexAccountUpstream(t *testing.T) {
	home := isolate(t)
	signInCodex(home, t)
	f := &upstream{ctype: "none", reply: sseBody(
		`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
		`data: {"type":"response.output_text.delta","delta":"pong"}`,
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`)}
	up := httptest.NewServer(f)
	defer up.Close()
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	code, body := doReq(t, "POST", "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping","max_output_tokens":20,"temperature":0.3}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.head.Get("chatgpt-account-id") != "acct-1" || !strings.HasPrefix(f.head.Get("Authorization"), "Bearer h.") {
		t.Fatalf("upstream headers: %v", f.head)
	}
	var m map[string]any
	json.Unmarshal(f.got, &m)
	if _, ok := m["max_output_tokens"]; ok || m["temperature"] != nil || m["stream"] != true || m["store"] != false || m["model"] != "gpt-5.5" {
		t.Fatalf("upstream body: %s", f.got)
	}
	if _, isList := m["input"].([]any); !isList {
		t.Fatalf("input not a list: %s", f.got)
	}
	var res map[string]any
	json.Unmarshal([]byte(body), &res)
	if res["object"] != "response" || !strings.Contains(body, "pong") {
		t.Fatalf("reply: %s", body)
	}

	// streaming relay, even without Content-Type
	code, body = doReq(t, "POST", "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping","stream":true,"max_output_tokens":20}`)
	if code != 200 || !strings.Contains(body, "response.output_text.delta") {
		t.Fatalf("stream: %d %s", code, body)
	}
	json.Unmarshal(f.got, &m)
	if _, ok := m["max_output_tokens"]; ok {
		t.Fatalf("relayed body: %s", f.got)
	}

	// also reachable from the Gemini door, answered as non-streaming JSON
	code, body = doReq(t, "POST", "/v1beta/models/codex/gpt-5.5:generateContent", `{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}`)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("gemini via codex: %d %s", code, body)
	}
}

func modelIDs(t *testing.T) string {
	_, body := doReq(t, "GET", "/v1/models", "")
	return body
}

func TestAcceptAccountProvidersLifecycle(t *testing.T) {
	home := isolate(t)
	signInCodex(home, t)
	writeJSON(t, filepath.Join(home, ".config", "github-copilot", "apps.json"), map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "octocat", "oauth_token": "gho_secret"},
	})
	body := modelIDs(t)
	if !strings.Contains(body, `"id":"codex/gpt-5.5"`) || !strings.Contains(body, `"id":"copilot/`) || strings.Contains(body, `"id":"copilot/auto"`) {
		t.Fatalf("catalog: %s", body)
	}
	ready := map[string]bool{}
	for _, p := range provider.All() {
		ready[p.ID] = p.Ready()
	}
	if !ready["codex"] || !ready["copilot"] {
		t.Fatalf("providers: %v", ready)
	}
	// saving writes picks only, never the login
	if err := provider.Save(provider.Provider{ID: "codex", Name: "x", Key: "sk-ignored", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(provider.Path())
	if s := string(b); strings.Contains(s, "sk-ignored") || strings.Contains(s, "gho_secret") || strings.Contains(s, "me@example.com") || !strings.Contains(s, "gpt-5.5") {
		t.Fatalf("stored: %s", s)
	}
	// delete only clears picks; the agent's login file is untouched
	authPath := filepath.Join(home, ".codex", "auth.json")
	before, _ := os.ReadFile(authPath)
	if err := provider.Delete("codex"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(authPath)
	if string(before) != string(after) {
		t.Fatal("codex login file changed")
	}
	found := false
	for _, p := range provider.All() {
		found = found || p.ID == "codex"
	}
	if !found {
		t.Fatal("codex vanished after delete")
	}
	// sign out: provider disappears, stale picks do not resurrect it
	provider.Save(provider.Provider{ID: "copilot", Models: []string{"gpt-5.5"}})
	os.Remove(filepath.Join(home, ".config", "github-copilot", "apps.json"))
	os.Remove(authPath)
	for _, p := range provider.All() {
		if p.ID == "codex" || p.ID == "copilot" {
			t.Fatalf("%s still listed after sign-out", p.ID)
		}
	}
	if strings.Contains(modelIDs(t), `"id":"codex/`) {
		t.Fatal("codex models still in catalog")
	}
}

func TestAcceptCodexApiKeyModeIsNotAnAccount(t *testing.T) {
	home := isolate(t)
	writeJSON(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "sk-x"})
	for _, p := range provider.All() {
		if p.ID == "codex" {
			t.Fatal("API-key mode Codex listed as account provider")
		}
	}
}

func TestAcceptClaudeLoginNotAProvider(t *testing.T) {
	home := isolate(t)
	writeJSON(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{"accessToken": "sk-ant-oat-secret"}})
	for _, p := range provider.All() {
		if p.ID == "claude" || p.ID == "anthropic" && p.Key != "" {
			t.Fatalf("claude exposed as provider: %+v", p.ID)
		}
	}
	body := modelIDs(t)
	if strings.Contains(body, `"id":"claude/`) || strings.Contains(body, "sk-ant-oat-secret") {
		t.Fatalf("catalog: %s", body)
	}
}
