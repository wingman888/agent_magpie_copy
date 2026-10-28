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
	"sync"
	"testing"
	"time"

	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

type upstream struct {
	mu   sync.Mutex
	got  []byte
	path string
	head http.Header
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.got, u.path, u.head = b, r.URL.Path, r.Header
	u.mu.Unlock()
	w.Header()["Content-Type"] = nil // like the ChatGPT backend
	io.WriteString(w, strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
		`data: {"type":"response.output_text.delta","delta":"pong"}`,
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`,
	}, "\n\n")+"\n\n")
}

func claims(m map[string]any) string {
	b, _ := json.Marshal(m)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

func post(t *testing.T, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	gateway.New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func TestAcceptCodexAccountThroughGateway(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	auth := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":"r","account_id":"acct-1"}}`,
		claims(map[string]any{"email": "me@example.com"}), claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}))
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(auth), 0o600)

	u := &upstream{}
	srv := httptest.NewServer(u)
	defer srv.Close()
	old := provider.CodexBase
	provider.CodexBase = srv.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	// non-streaming client: translated, answered as JSON
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping","max_output_tokens":20,"temperature":0.3}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if u.path != "/backend-api/codex/responses" || u.head.Get("chatgpt-account-id") != "acct-1" || !strings.HasPrefix(u.head.Get("Authorization"), "Bearer h.") {
		t.Fatalf("upstream call: %s %v", u.path, u.head)
	}
	var up map[string]any
	json.Unmarshal(u.got, &up)
	if _, ok := up["max_output_tokens"]; ok || up["temperature"] != nil || up["stream"] != true || up["store"] != false || up["model"] != "gpt-5.5" {
		t.Fatalf("upstream body: %s", u.got)
	}
	var res map[string]any
	json.Unmarshal([]byte(body), &res)
	if res["object"] != "response" || !strings.Contains(body, "pong") {
		t.Fatalf("reply: %s", body)
	}

	// streaming client: relayed
	code, body = post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping","stream":true,"max_output_tokens":20}`)
	if code != 200 || !strings.Contains(body, "response.output_text.delta") {
		t.Fatalf("stream: %d %s", code, body)
	}
	up = nil
	json.Unmarshal(u.got, &up)
	if _, ok := up["max_output_tokens"]; ok {
		t.Fatalf("relayed body: %s", u.got)
	}
	if _, isList := up["input"].([]any); !isList {
		t.Fatalf("relayed input not a list: %s", u.got)
	}
}
