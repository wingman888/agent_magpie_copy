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

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(b) + ".sig"
}

// codexBackend is a ChatGPT backend stand-in: it sends no Content-Type.
type codexBackend struct {
	got  map[string]any
	head http.Header
	path string
	code int
}

func (c *codexBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	json.NewDecoder(r.Body).Decode(&c.got)
	c.head, c.path = r.Header, r.URL.Path
	w.Header()["Content-Type"] = nil // no sniffing either
	if c.code != 0 {
		w.WriteHeader(c.code)
		io.WriteString(w, `{"detail":"token expired"}`)
		return
	}
	io.WriteString(w, "\n"+sse(
		`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"Hello "}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"world"}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.5","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`))
}

func codexSetup(t *testing.T, be *codexBackend) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(h, "none"))
	os.MkdirAll(filepath.Join(h, ".codex"), 0o755)
	b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": jwt(map[string]any{"email": "me@example.com"}), "access_token": jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
		"refresh_token": "rt", "account_id": "acct-9"}})
	os.WriteFile(filepath.Join(h, ".codex", "auth.json"), b, 0o600)
	up := httptest.NewServer(be)
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
}

func checkBackend(t *testing.T, be *codexBackend) {
	t.Helper()
	if be.path != "/backend-api/codex/responses" {
		t.Errorf("path %s", be.path)
	}
	if be.got["stream"] != true || be.got["store"] != false || be.got["model"] != "gpt-5.5" {
		t.Errorf("backend body: %v", be.got)
	}
	if _, ok := be.got["max_output_tokens"]; ok {
		t.Errorf("max_output_tokens kept: %v", be.got)
	}
	if _, ok := be.got["temperature"]; ok {
		t.Errorf("temperature kept: %v", be.got)
	}
	if in, ok := be.got["input"].([]any); !ok || len(in) != 1 {
		t.Errorf("input: %v", be.got["input"])
	}
	if !strings.HasPrefix(be.head.Get("Authorization"), "Bearer ") || be.head.Get("chatgpt-account-id") != "acct-9" {
		t.Errorf("headers: %v", be.head)
	}
}

func TestCodexNonStreaming(t *testing.T) {
	be := &codexBackend{}
	codexSetup(t, be)
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":50,"temperature":0.3,"reasoning":{"effort":"low"}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	checkBackend(t, be)
	if be.got["reasoning"] == nil {
		t.Errorf("reasoning dropped: %v", be.got)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not json: %s", body)
	}
	if out["object"] != "response" || !strings.Contains(body, "Hello world") || out["usage"] == nil {
		t.Errorf("reply: %s", body)
	}
}

func TestCodexStreaming(t *testing.T) {
	be := &codexBackend{}
	codexSetup(t, be)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"hi","max_output_tokens":50,"temperature":0.3}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	checkBackend(t, be)
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Errorf("content type %q", rec.Header().Get("Content-Type"))
	}
	var types []string
	for _, e := range events(rec.Body.String()) {
		types = append(types, e["type"].(string))
	}
	if strings.Join(types, " ") != "response.created response.output_text.delta response.output_text.delta response.completed" {
		t.Errorf("events: %v", types)
	}
}

func TestCodexChatClientTranslates(t *testing.T) {
	be := &codexBackend{}
	codexSetup(t, be)
	code, body := post(t, "/v1/chat/completions", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}],"max_tokens":20}`)
	if code != 200 || !strings.Contains(body, "Hello world") {
		t.Fatalf("%d %s", code, body)
	}
	if be.got["stream"] != true || be.got["store"] != false || be.got["max_output_tokens"] != nil {
		t.Errorf("backend: %v", be.got)
	}
}

func TestCodexSignedOut(t *testing.T) {
	be := &codexBackend{code: 401}
	codexSetup(t, be)
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 401 || !strings.Contains(body, "codex login") {
		t.Errorf("%d %s", code, body)
	}
	// the catalog entry survives only while the sign-in does: resolve
	// before removing it so the request reaches the signing step
	p, _, ok := provider.Resolve("codex/gpt-5.5")
	if !ok {
		t.Fatal("no codex")
	}
	os.Remove(filepath.Join(os.Getenv("HOME"), ".codex", "auth.json"))
	rec := httptest.NewRecorder()
	st, msg := New().passthrough(rec, httptest.NewRequest("POST", "/v1/responses", nil), p, provider.Responses, "gpt-5.5", []byte(`{"input":"hi"}`), &Usage{})
	if st != 401 || !strings.Contains(msg, "codex login") {
		t.Errorf("%d %s", st, msg)
	}
}

func TestSniffWithoutContentType(t *testing.T) {
	for in, want := range map[string]bool{"data: {}\n\n": true, "\n\nevent: x\ndata: {}\n\n": true, `{"a":1}`: false, "": false, "da": false} {
		res := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(in))}
		sniffStream(res)
		if got := isStream(res); got != want {
			t.Errorf("%q: %v", in, got)
		}
		if b, _ := io.ReadAll(res.Body); string(b) != in {
			t.Errorf("body lost: %q", b)
		}
	}
}

func TestCopilotThroughGateway(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	os.MkdirAll(filepath.Join(h, ".config", "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(h, ".config", "github-copilot", "hosts.json"), []byte(`{"github.com":{"user":"octo","oauth_token":"gho_gw"}}`), 0o600)
	var head http.Header
	var path string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			json.NewEncoder(w).Encode(map[string]any{"token": "sess-gw", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]any{"api": srv.URL + "/cp"}})
			return
		}
		head, path = r.Header, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"yo"}}]}`)
	}))
	defer srv.Close()
	old := provider.CopilotTokenURL
	provider.CopilotTokenURL = srv.URL + "/token"
	defer func() { provider.CopilotTokenURL = old }()
	code, body := post(t, "/v1/chat/completions", `{"model":"copilot/claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "yo") {
		t.Fatalf("%d %s", code, body)
	}
	if path != "/cp/chat/completions" || head.Get("Authorization") != "Bearer sess-gw" || head.Get("X-Initiator") != "user" {
		t.Errorf("%s %v", path, head)
	}
}
