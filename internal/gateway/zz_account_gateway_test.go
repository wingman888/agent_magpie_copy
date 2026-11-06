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

// zzCodexUp is a fake ChatGPT Codex backend: it records what it was sent and
// answers with a script.
type zzCodexUp struct {
	header http.Header
	body   map[string]any
	reply  string
	noType bool
	code   int
}

func (c *zzCodexUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.header = r.Header.Clone()
	c.body = nil
	json.NewDecoder(r.Body).Decode(&c.body)
	switch {
	case c.noType:
		w.Header()["Content-Type"] = nil // the backend sent no type at all
	default:
		w.Header().Set("Content-Type", "text/event-stream")
	}
	if c.code != 0 {
		w.WriteHeader(c.code)
	}
	io.WriteString(w, c.reply)
}

func TestZzCodexUnauthorizedSaysRelogin(t *testing.T) {
	up := &zzCodexUp{code: http.StatusUnauthorized, reply: `{"detail":"token revoked"}`}
	zzCodexSetup(t, up)
	code, body := zzAcctPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 401 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "codex login") {
		t.Errorf("no relogin hint: %s", body)
	}
}

func zzAccountJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// zzCodexSetup signs a fake Codex account in under a temp HOME and points the
// Codex backend at the fake. It returns the access token the account holds.
func zzCodexSetup(t *testing.T, up *zzCodexUp) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	old := provider.CodexBase
	provider.CodexBase = srv.URL
	t.Cleanup(func() { provider.CodexBase = old })

	access := zzAccountJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	doc := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     zzAccountJWT(map[string]any{"email": "dev@example.com"}),
		"access_token": access, "refresh_token": "rt", "account_id": "acct-1"}}
	b, _ := json.Marshal(doc)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return access
}

func zzCodexCompleted(id string) string {
	return zzAcctSSE(
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"`+id+`"}}`,
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		`data: {"type":"response.completed","response":{"id":"`+id+`","object":"response","status":"completed","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":4,"output_tokens":1}}}`)
}

func TestZzCodexNonStreamResponsesAnswersJSON(t *testing.T) {
	up := &zzCodexUp{reply: zzCodexCompleted("resp_1")}
	access := zzCodexSetup(t, up)
	code, body := zzAcctPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hello","max_output_tokens":50,"temperature":0.5}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("not JSON: %v: %s", err, body)
	}
	if v["id"] != "resp_1" || v["status"] != "completed" {
		t.Fatalf("reply = %s", body)
	}
	if got := up.header.Get("Authorization"); got != "Bearer "+access {
		t.Errorf("authorization = %q", got)
	}
	if got := up.header.Get("chatgpt-account-id"); got != "acct-1" {
		t.Errorf("chatgpt-account-id = %q", got)
	}
	if up.body["model"] != "gpt-5.5" || up.body["stream"] != true || up.body["store"] != false {
		t.Errorf("upstream body = %v", up.body)
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := up.body[k]; ok {
			t.Errorf("%s reached the Codex backend", k)
		}
	}
	items, ok := up.body["input"].([]any)
	if !ok || len(items) != 1 {
		t.Errorf("input not turned into a list: %#v", up.body["input"])
	}
}

func TestZzCodexStreamForwardedWithoutContentType(t *testing.T) {
	up := &zzCodexUp{noType: true, reply: zzCodexCompleted("resp_2")}
	zzCodexSetup(t, up)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"hi","temperature":1}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "response.completed") {
		t.Errorf("stream body missing the final event: %s", rec.Body.String())
	}
	if up.body["stream"] != true {
		t.Errorf("upstream stream = %v", up.body["stream"])
	}
	if _, ok := up.body["temperature"]; ok {
		t.Error("temperature reached the Codex backend")
	}
}

func TestZzCodexChatClientThroughTranslation(t *testing.T) {
	up := &zzCodexUp{reply: zzCodexCompleted("resp_3")}
	zzCodexSetup(t, up)
	code, body := zzAcctPost(t, "/v1/chat/completions", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}],"max_tokens":20}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "Hello") {
		t.Errorf("reply lacks the text: %s", body)
	}
	if up.body["stream"] != true || up.body["store"] != false {
		t.Errorf("upstream body = %v", up.body)
	}
	if _, ok := up.body["max_output_tokens"]; ok {
		t.Error("max_output_tokens reached the Codex backend")
	}
}

func TestZzCodexLoggedOutIsUnknown(t *testing.T) {
	up := &zzCodexUp{reply: zzCodexCompleted("resp_4")}
	zzCodexSetup(t, up)
	if err := os.Remove(filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	code, body := zzAcctPost(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 404 {
		t.Fatalf("status %d: %s", code, body)
	}
	if up.body != nil {
		t.Error("a logged-out account still reached the backend")
	}
}

func zzAcctSSE(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n\n")
	}
	return b.String()
}

func zzAcctPost(t *testing.T, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}
