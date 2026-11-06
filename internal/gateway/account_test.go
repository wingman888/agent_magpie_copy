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

// codexUp is a fake ChatGPT Codex backend: it records what it was sent and
// answers with a script.
type codexUp struct {
	header http.Header
	body   map[string]any
	reply  string
	noType bool
}

func (c *codexUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.header = r.Header.Clone()
	c.body = nil
	json.NewDecoder(r.Body).Decode(&c.body)
	if c.noType {
		w.Header()["Content-Type"] = nil
	}
	io.WriteString(w, c.reply)
}

func accountJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// codexSetup signs a fake Codex account in under a temp HOME and points the
// Codex backend at the fake. It returns the access token the account holds.
func codexSetup(t *testing.T, up *codexUp) string {
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

	access := accountJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	doc := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     accountJWT(map[string]any{"email": "dev@example.com"}),
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

func codexCompleted(id string) string {
	return sse(
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"`+id+`"}}`,
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		`data: {"type":"response.completed","response":{"id":"`+id+`","object":"response","status":"completed","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":4,"output_tokens":1}}}`)
}

func TestCodexNonStreamResponsesAnswersJSON(t *testing.T) {
	up := &codexUp{reply: codexCompleted("resp_1")}
	access := codexSetup(t, up)
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hello","max_output_tokens":50,"temperature":0.5}`)
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

func TestCodexStreamForwardedWithoutContentType(t *testing.T) {
	up := &codexUp{noType: true, reply: codexCompleted("resp_2")}
	codexSetup(t, up)
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

func TestCodexChatClientThroughTranslation(t *testing.T) {
	up := &codexUp{reply: codexCompleted("resp_3")}
	codexSetup(t, up)
	code, body := post(t, "/v1/chat/completions", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}],"max_tokens":20}`)
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

func TestCodexLoggedOutIsUnknown(t *testing.T) {
	up := &codexUp{reply: codexCompleted("resp_4")}
	codexSetup(t, up)
	if err := os.Remove(filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 404 {
		t.Fatalf("status %d: %s", code, body)
	}
	if up.body != nil {
		t.Error("a logged-out account still reached the backend")
	}
}
