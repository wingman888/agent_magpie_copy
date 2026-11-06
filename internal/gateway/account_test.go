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

	"github.com/yetone/dial/internal/provider"
)

// acctBackend stands in for the ChatGPT backend behind a Codex sign-in. It
// records what it was sent and replies with a script.
type acctBackend struct {
	got    map[string]any
	path   string
	auth   string
	acct   string
	reply  string
	noType bool // leave out Content-Type, as the ChatGPT backend does
	code   int
}

func (b *acctBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	b.got = nil
	json.Unmarshal(raw, &b.got)
	b.path = r.URL.Path
	b.auth = r.Header.Get("Authorization")
	b.acct = r.Header.Get("chatgpt-account-id")
	if b.noType {
		w.Header()["Content-Type"] = nil
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	if b.code != 0 {
		w.WriteHeader(b.code)
	}
	io.WriteString(w, b.reply)
}

// acctJWT builds an unsigned JWT with the given claims.
func acctJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// acctSetup writes a ChatGPT-mode ~/.codex/auth.json under a temp HOME and
// points the Codex backend at the stand-in.
func acctSetup(t *testing.T, b *acctBackend, access, refresh string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	auth := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      acctJWT(map[string]any{"email": "dev@example.com"}),
			"access_token":  access,
			"refresh_token": refresh,
			"account_id":    "acct-1",
		},
	}
	raw, _ := json.Marshal(auth)
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
}

// acctRun sends a Responses request through the gateway.
func acctRun(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	return rec
}

// acctCheckUpstream asserts what the ChatGPT backend received.
func acctCheckUpstream(t *testing.T, b *acctBackend) {
	t.Helper()
	if b.path != "/backend-api/codex/responses" {
		t.Errorf("upstream path %q", b.path)
	}
	if b.got["stream"] != true || b.got["store"] != false {
		t.Errorf("stream/store: %v %v", b.got["stream"], b.got["store"])
	}
	if _, ok := b.got["input"].([]any); !ok {
		t.Errorf("input is not a list: %v", b.got["input"])
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := b.got[k]; ok {
			t.Errorf("%s was sent upstream", k)
		}
	}
	if b.got["model"] != "gpt-5.5" {
		t.Errorf("model %v", b.got["model"])
	}
	if b.auth != "Bearer tok-acc" || b.acct != "acct-1" {
		t.Errorf("auth %q account %q", b.auth, b.acct)
	}
}

const acctCompleted = `data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5","status":"in_progress"}}

data: {"type":"response.output_text.delta","delta":"Hello "}

data: {"type":"response.output_text.delta","delta":"world"}

data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello world"}]}],"usage":{"input_tokens":12,"output_tokens":4,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}

`

func TestAcctCodexNonStreamReturnsResponseObject(t *testing.T) {
	b := &acctBackend{reply: acctCompleted}
	acctSetup(t, b, "tok-acc", "rt")
	rec := acctRun(t, `{"model":"codex/gpt-5.5","input":"hi there","instructions":"be brief","max_output_tokens":50,"temperature":0.5}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %v: %s", err, rec.Body.String())
	}
	if out["object"] != "response" {
		t.Errorf("object %v", out["object"])
	}
	if !strings.Contains(rec.Body.String(), "Hello world") {
		t.Errorf("text missing: %s", rec.Body.String())
	}
	if u, _ := out["usage"].(map[string]any); u["output_tokens"] != float64(4) {
		t.Errorf("usage %v", out["usage"])
	}
	acctCheckUpstream(t, b)
	if in, _ := b.got["input"].([]any); len(in) != 1 {
		t.Errorf("string input should become one message: %v", b.got["input"])
	}
}

func TestAcctCodexStreamPassesEventsThrough(t *testing.T) {
	b := &acctBackend{reply: acctCompleted}
	acctSetup(t, b, "tok-acc", "rt")
	rec := acctRun(t, `{"model":"codex/gpt-5.5","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"max_output_tokens":9}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content type %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "response.completed") {
		t.Errorf("event stream not relayed: %s", rec.Body.String())
	}
	acctCheckUpstream(t, b)
}

func TestAcctCodexSniffsStreamWithoutContentType(t *testing.T) {
	b := &acctBackend{reply: acctCompleted, noType: true}
	acctSetup(t, b, "tok-acc", "rt")
	rec := acctRun(t, `{"model":"codex/gpt-5.5","stream":true,"input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content type %q, want text/event-stream", ct)
	}
	acctCheckUpstream(t, b)
}

func TestAcctCodexRejectedTokenAsksForLogin(t *testing.T) {
	b := &acctBackend{code: http.StatusUnauthorized, reply: `{"detail":"Unauthorized"}`, noType: true}
	acctSetup(t, b, "tok-acc", "rt")
	rec := acctRun(t, `{"model":"codex/gpt-5.5","input":"hi"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "请重新登录") {
		t.Errorf("no re-login hint: %s", rec.Body.String())
	}
}

func TestAcctCodexExpiredWithoutRefreshAsksForLogin(t *testing.T) {
	b := &acctBackend{reply: acctCompleted}
	acctSetup(t, b, acctJWT(map[string]any{"exp": 1}), "")
	rec := acctRun(t, `{"model":"codex/gpt-5.5","input":"hi"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "请重新登录") {
		t.Errorf("no re-login hint: %s", rec.Body.String())
	}
	if b.got != nil {
		t.Errorf("backend was called with no usable token: %v", b.got)
	}
}
