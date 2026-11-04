package gateway

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

// acctFakeJWT makes an unsigned token with the given claims, enough for dial,
// which reads claims without verifying them.
func acctFakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// acctCodexLogin writes a ChatGPT-mode auth.json under a temp HOME and points the
// ChatGPT backend at a stand-in.
func acctCodexLogin(t *testing.T, up *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	exp := time.Now().Add(time.Hour).Unix()
	access := acctFakeJWT(map[string]any{"exp": exp})
	id := acctFakeJWT(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}})
	auth := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": id, "access_token": access, "refresh_token": "rt", "account_id": "acct_1"}}
	b, _ := json.Marshal(auth)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
}

// acctBackend records one request and replies with a script. noType leaves out
// the Content-Type header, as the ChatGPT backend does.
type acctBackend struct {
	got    map[string]any
	raw    []byte
	path   string
	head   http.Header
	reply  string
	noType bool
}

func (b *acctBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.raw, _ = io.ReadAll(r.Body)
	b.got = map[string]any{}
	json.Unmarshal(b.raw, &b.got)
	b.path, b.head = r.URL.Path, r.Header
	if b.noType {
		w.Header()["Content-Type"] = nil
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	io.WriteString(w, b.reply)
}

const acctCodexReply = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\"}}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello \"}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"delta\":\"there\"}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\",\"status\":\"completed\"," +
	"\"usage\":{\"input_tokens\":7,\"output_tokens\":2,\"total_tokens\":9}}}\n\n"

func TestCodexNonStreamResponses(t *testing.T) {
	b := &acctBackend{reply: acctCodexReply}
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	acctCodexLogin(t, up)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":99,"temperature":0.5,"reasoning":{"effort":"low"}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if b.path != "/backend-api/codex/responses" {
		t.Errorf("path %s", b.path)
	}
	if b.got["stream"] != true || b.got["store"] != false {
		t.Errorf("stream/store: %s", b.raw)
	}
	if _, ok := b.got["input"].([]any); !ok {
		t.Errorf("input is not a list: %s", b.raw)
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := b.got[k]; ok {
			t.Errorf("%s was sent: %s", k, b.raw)
		}
	}
	if b.got["model"] != "gpt-5.5" {
		t.Errorf("model %v", b.got["model"])
	}
	if b.got["reasoning"] == nil {
		t.Errorf("reasoning dropped: %s", b.raw)
	}
	if got := b.head.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") || got == "Bearer " {
		t.Errorf("authorization %q", got)
	}
	if b.head.Get("chatgpt-account-id") != "acct_1" {
		t.Errorf("account id %q", b.head.Get("chatgpt-account-id"))
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("client reply is not JSON: %v: %s", err, body)
	}
	if out["object"] != "response" {
		t.Errorf("object %v", out["object"])
	}
	if !strings.Contains(body, "Hello there") {
		t.Errorf("text missing: %s", body)
	}
	if !strings.Contains(body, `"total_tokens":9`) {
		t.Errorf("usage missing: %s", body)
	}
}

func TestCodexStreamPassthrough(t *testing.T) {
	b := &acctBackend{reply: acctCodexReply}
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	acctCodexLogin(t, up)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":[{"role":"user","content":"hi"}],"max_output_tokens":50}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if b.got["stream"] != true || b.got["store"] != false {
		t.Errorf("stream/store: %s", b.raw)
	}
	if _, ok := b.got["max_output_tokens"]; ok {
		t.Errorf("max_output_tokens was sent: %s", b.raw)
	}
	evs := events(body)
	if len(evs) != 4 {
		t.Fatalf("want 4 events, got %d: %s", len(evs), body)
	}
	if evs[len(evs)-1]["type"] != "response.completed" {
		t.Errorf("last event %v", evs[len(evs)-1]["type"])
	}
}

func TestCodexNoContentTypeIsStream(t *testing.T) {
	b := &acctBackend{reply: acctCodexReply, noType: true}
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	acctCodexLogin(t, up)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"hi"}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content type %q", ct)
	}
	if len(events(rec.Body.String())) != 4 {
		t.Errorf("events not relayed: %s", rec.Body.String())
	}
}

func TestCodexSignedOutIsExplicit(t *testing.T) {
	b := &acctBackend{reply: acctCodexReply}
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	acctCodexLogin(t, up)
	if err := os.Remove(filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 401 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "已登出") || !strings.Contains(body, "请重新登录") {
		t.Errorf("message does not say signed out: %s", body)
	}
	if b.got != nil {
		t.Errorf("backend was called: %s", b.raw)
	}
}

// acctCopilotLogin writes a GitHub login and a token exchange that points the
// session at the given API stand-in.
func acctCopilotLogin(t *testing.T, api *httptest.Server, oauth string) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := filepath.Join(cfg, "github-copilot")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	apps := `{"github.com:octo":{"user":"octo","oauth_token":"` + oauth + `"}}`
	if err := os.WriteFile(filepath.Join(dir, "apps.json"), []byte(apps), 0o600); err != nil {
		t.Fatal(err)
	}
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token "+oauth {
			http.Error(w, "bad oauth", 401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "sess_" + oauth, "expires_at": time.Now().Add(time.Hour).Unix(),
			"endpoints": map[string]string{"api": api.URL}})
	}))
	t.Cleanup(tok.Close)
	old := provider.CopilotTokenURL
	provider.CopilotTokenURL = tok.URL
	t.Cleanup(func() { provider.CopilotTokenURL = old })
}

const acctChatReply = "data: {\"id\":\"c1\",\"model\":\"claude-sonnet-4.5\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hi \"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"copilot\"},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n" +
	"data: [DONE]\n\n"

func TestCopilotNonStreamChat(t *testing.T) {
	b := &acctBackend{reply: acctChatReply}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.ServeHTTP(w, r)
	}))
	t.Cleanup(api.Close)
	acctCopilotLogin(t, api, "gho_chat")

	code, body := post(t, "/v1/chat/completions", `{"model":"copilot/claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if b.path != "/chat/completions" {
		t.Errorf("path %s", b.path)
	}
	if b.got["model"] != "claude-sonnet-4.5" || b.got["stream"] != true {
		t.Errorf("upstream body: %s", b.raw)
	}
	if got := b.head.Get("Authorization"); got != "Bearer sess_gho_chat" {
		t.Errorf("authorization %q", got)
	}
	if got := b.head.Get("X-Initiator"); got != "user" {
		t.Errorf("X-Initiator %q", got)
	}
	if !strings.Contains(body, "Hi copilot") {
		t.Errorf("text missing: %s", body)
	}
}

func TestCopilotAgentInitiatedWhenLastIsTool(t *testing.T) {
	b := &acctBackend{reply: acctChatReply}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.ServeHTTP(w, r)
	}))
	t.Cleanup(api.Close)
	acctCopilotLogin(t, api, "gho_tool")

	code, body := post(t, "/v1/chat/completions", `{"model":"copilot/claude-sonnet-4.5","stream":true,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"","tool_calls":[{"id":"t1","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"t1","content":"x"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := b.head.Get("X-Initiator"); got != "agent" {
		t.Errorf("X-Initiator %q", got)
	}
	if !strings.Contains(body, "copilot") {
		t.Errorf("stream not relayed: %s", body)
	}
}

func TestCopilotSignedOutIsExplicit(t *testing.T) {
	api := httptest.NewServer(&acctBackend{reply: acctChatReply})
	t.Cleanup(api.Close)
	acctCopilotLogin(t, api, "gho_gone")
	if err := os.Remove(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json")); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"copilot/claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}]}`)
	if code != 401 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "已登出") || !strings.Contains(body, "请重新登录") {
		t.Errorf("message does not say signed out: %s", body)
	}
}
