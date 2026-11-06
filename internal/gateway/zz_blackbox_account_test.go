package gateway

// Independent black-box checks of agent accounts as providers: Codex
// upstream behaviour, Gemini over Codex, and what /v1/models shows for
// Codex, Copilot and Claude Code.

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

// zzbbCodex is a fake ChatGPT Codex backend that records what it was sent.
type zzbbCodex struct {
	path   string
	header http.Header
	body   map[string]any
	reply  string
	noType bool
}

func (c *zzbbCodex) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.path = r.URL.Path
	c.header = r.Header.Clone()
	raw, _ := io.ReadAll(r.Body)
	c.body = nil
	json.Unmarshal(raw, &c.body)
	if c.noType {
		w.Header()["Content-Type"] = nil // the backend sends no Content-Type at all
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	io.WriteString(w, c.reply)
}

func zzbbJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// zzbbCodexSetup writes a ChatGPT login under a temp HOME and points the Codex
// backend at the fake. It returns the access token the login holds.
func zzbbCodexSetup(t *testing.T, up *zzbbCodex) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "no-claude"))
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	old := provider.CodexBase
	provider.CodexBase = srv.URL
	t.Cleanup(func() { provider.CodexBase = old })

	access := zzbbJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	doc := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     zzbbJWT(map[string]any{"email": "dev@example.com"}),
		"access_token": access, "refresh_token": "rt-1", "account_id": "acct-1"}}
	b, _ := json.Marshal(doc)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return access
}

func zzbbCodexCompleted(id string) string {
	return zzbbSSE(
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"`+id+`"}}`,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"`+id+`","object":"response","status":"completed","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":4,"output_tokens":1}}}`)
}

// Non-streaming Responses through Codex: full JSON back; upstream gets the
// account token and id, stream:true, store:false, no sampling or length fields,
// and a list input.
func TestZzBBCodexNonStreamResponses(t *testing.T) {
	up := &zzbbCodex{reply: zzbbCodexCompleted("resp_1")}
	access := zzbbCodexSetup(t, up)
	code, body := zzbbDo(t, "POST", "/v1/responses",
		`{"model":"codex/gpt-5.5","input":"hello","max_output_tokens":50,"temperature":0.5}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	m := zzbbJSON(t, body)
	if m["id"] != "resp_1" || m["status"] != "completed" {
		t.Errorf("client reply = %s", body)
	}
	if strings.Contains(body, "event:") || strings.Contains(body, "data:") {
		t.Errorf("client got an event stream for a non-stream request: %s", body)
	}
	if got := up.header.Get("Authorization"); got != "Bearer "+access {
		t.Errorf("Authorization = %q", got)
	}
	if got := up.header.Get("chatgpt-account-id"); got != "acct-1" {
		t.Errorf("chatgpt-account-id = %q", got)
	}
	if up.body["stream"] != true || up.body["store"] != false {
		t.Errorf("stream/store: %v", up.body)
	}
	if _, ok := up.body["max_output_tokens"]; ok {
		t.Errorf("max_output_tokens sent upstream: %v", up.body)
	}
	if _, ok := up.body["temperature"]; ok {
		t.Errorf("temperature sent upstream: %v", up.body)
	}
	if _, ok := up.body["input"].([]any); !ok {
		t.Errorf("input is not a list: %v", up.body["input"])
	}
	if up.body["model"] != "gpt-5.5" {
		t.Errorf("model = %v, want gpt-5.5 without prefix", up.body["model"])
	}
}

// Streaming Responses through Codex is forwarded; the same fields stay off.
func TestZzBBCodexStreamResponses(t *testing.T) {
	up := &zzbbCodex{reply: zzbbCodexCompleted("resp_2")}
	zzbbCodexSetup(t, up)
	code, body := zzbbDo(t, "POST", "/v1/responses",
		`{"model":"codex/gpt-5.5","stream":true,"input":"hello","max_output_tokens":50,"temperature":0.5}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "response.completed") {
		t.Errorf("stream not forwarded: %s", body)
	}
	if up.body["stream"] != true || up.body["store"] != false {
		t.Errorf("stream/store: %v", up.body)
	}
	if _, ok := up.body["max_output_tokens"]; ok {
		t.Errorf("max_output_tokens sent upstream on stream: %v", up.body)
	}
	if _, ok := up.body["temperature"]; ok {
		t.Errorf("temperature sent upstream on stream: %v", up.body)
	}
	if _, ok := up.body["input"].([]any); !ok {
		t.Errorf("input is not a list on stream: %v", up.body["input"])
	}
}

// The backend sends no Content-Type; the non-stream answer must still be
// read as an event stream and returned as complete JSON.
func TestZzBBCodexNoContentType(t *testing.T) {
	up := &zzbbCodex{reply: zzbbCodexCompleted("resp_3"), noType: true}
	zzbbCodexSetup(t, up)
	code, body := zzbbDo(t, "POST", "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	m := zzbbJSON(t, body)
	if m["id"] != "resp_3" || m["status"] != "completed" {
		t.Errorf("reply = %s", body)
	}
}

// Gemini generateContent against codex/gpt-5.5 succeeds and answers in Gemini shape.
func TestZzBBCodexThroughGemini(t *testing.T) {
	up := &zzbbCodex{reply: zzbbCodexCompleted("resp_4")}
	zzbbCodexSetup(t, up)
	code, body := zzbbDo(t, "POST", "/v1beta/models/codex/gpt-5.5:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	m := zzbbJSON(t, body)
	cand := zzbbMap(zzbbList(m["candidates"])[0])
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason = %v: %s", cand["finishReason"], body)
	}
	parts := zzbbList(zzbbMap(cand["content"])["parts"])
	if len(parts) == 0 || zzbbMap(parts[0])["text"] != "Hello" {
		t.Errorf("text part: %s", body)
	}
	if zzbbMap(m["usageMetadata"]) == nil {
		t.Errorf("no usageMetadata: %s", body)
	}
	if up.path != "/responses" {
		t.Errorf("codex upstream path %s", up.path)
	}
}

// /v1/models shows codex/… models for a signed-in Codex account.
func TestZzBBCodexListedInModels(t *testing.T) {
	zzbbCodexSetup(t, &zzbbCodex{reply: zzbbCodexCompleted("x")})
	code, body := zzbbDo(t, "GET", "/v1/models", "")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, `"id":"codex/gpt-5.5"`) {
		t.Errorf("codex/gpt-5.5 not listed: %s", body)
	}
}

// Copilot: after a sign-in exists, /v1/models lists copilot/ models from the
// built-in list, never auto; the GitHub token never shows.
func TestZzBBCopilotListedInModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "no-claude"))
	dir := filepath.Join(home, "config", "github-copilot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	apps := `{"github.com:octo":{"user":"octo","oauth_token":"gho_BBSECRET"}}`
	if err := os.WriteFile(filepath.Join(dir, "apps.json"), []byte(apps), 0o600); err != nil {
		t.Fatal(err)
	}
	code, body := zzbbDo(t, "GET", "/v1/models", "")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	n := 0
	for _, x := range zzbbList(zzbbMap(zzbbJSON(t, body))["data"]) {
		id := zzbbStr(zzbbMap(x)["id"])
		if strings.HasPrefix(id, "copilot/") {
			n++
			if id == "copilot/auto" {
				t.Errorf("copilot/auto must not be listed")
			}
		}
	}
	if n == 0 {
		t.Errorf("no copilot/ models listed: %s", body)
	}
	if strings.Contains(body, "gho_BBSECRET") {
		t.Errorf("GitHub token leaked into /v1/models")
	}
}

// Claude Code signed in: never a provider, never a model; its token never shows.
func TestZzBBClaudeNotInModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	cdir := filepath.Join(home, "claude-cfg")
	t.Setenv("CLAUDE_CONFIG_DIR", cdir)
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatal(err)
	}
	creds := `{"claudeAiOauth":{"accessToken":"sk-ant-oat-BBSECRET"}}`
	if err := os.WriteFile(filepath.Join(cdir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	code, body := zzbbDo(t, "GET", "/v1/models", "")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if strings.Contains(body, "sk-ant-oat-BBSECRET") {
		t.Errorf("Claude token leaked into /v1/models")
	}
	for _, x := range zzbbList(zzbbMap(zzbbJSON(t, body))["data"]) {
		e := zzbbMap(x)
		if e["owned_by"] == "claude" || strings.HasPrefix(zzbbStr(e["id"]), "claude/") {
			t.Errorf("Claude Code appears as a model: %v", e)
		}
	}
}
