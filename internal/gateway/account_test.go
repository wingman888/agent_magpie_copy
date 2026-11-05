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

func zvAcctJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// zvCodexHome signs dial in to Codex with a fresh, unexpired ChatGPT login and
// returns the access token it wrote.
func zvCodexHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(h, ".claude"))
	access := zvAcctJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": zvAcctJWT(map[string]any{"email": "me@example.com"}), "access_token": access,
		"refresh_token": "rt", "account_id": "acct-1"}})
	if err := os.MkdirAll(filepath.Join(h, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h, ".codex", "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return access
}

// zvCodexUpstream records what arrived and answers with a bare event stream:
// no Content-Type header, as the ChatGPT backend sends it.
type zvCodexUpstream struct {
	path  string
	head  http.Header
	body  map[string]any
	reply string
}

func (f *zvCodexUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.path, f.head = r.URL.Path, r.Header
	raw, _ := io.ReadAll(r.Body)
	f.body = map[string]any{}
	json.Unmarshal(raw, &f.body)
	w.Header()["Content-Type"] = nil
	io.WriteString(w, f.reply)
}

const zvCodexReply = "event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","delta":"hel"}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","delta":"lo"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}` + "\n\n"

// zvStartCodex points the Codex backend at a fake and returns the fake and a gateway in front of dial.
func zvStartCodex(t *testing.T) (*zvCodexUpstream, *httptest.Server) {
	t.Helper()
	up := &zvCodexUpstream{reply: zvCodexReply}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	old := provider.CodexBase
	provider.CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
	gw := httptest.NewServer(New().Handler())
	t.Cleanup(gw.Close)
	return up, gw
}

func zvCheckCodexBody(t *testing.T, up *zvCodexUpstream) {
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

func TestZv_CodexNonStreamResponsesIsAggregated(t *testing.T) {
	access := zvCodexHome(t)
	up, gw := zvStartCodex(t)
	res, err := http.Post(gw.URL+"/v1/responses", "application/json",
		strings.NewReader(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":50,"temperature":0.2}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}
	var out struct {
		Object string `json:"object"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("reply is not JSON: %s", b)
	}
	if out.Object != "response" || len(out.Output) == 0 || out.Output[0].Content[0].Text != "hello" {
		t.Fatalf("reply = %s", b)
	}
	if up.path != "/backend-api/codex/responses" {
		t.Fatalf("path = %s", up.path)
	}
	if up.head.Get("Authorization") != "Bearer "+access || up.head.Get("chatgpt-account-id") != "acct-1" {
		t.Fatalf("auth headers = %v", up.head)
	}
	zvCheckCodexBody(t, up)
}

func TestZv_CodexStreamResponsesIsForwarded(t *testing.T) {
	zvCodexHome(t)
	up, gw := zvStartCodex(t)
	res, err := http.Post(gw.URL+"/v1/responses", "application/json",
		strings.NewReader(`{"model":"codex/gpt-5.5","input":[{"role":"user","content":"hi"}],"stream":true,"max_output_tokens":9,"temperature":1}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(b), "response.completed") {
		t.Fatalf("status %d, body %s", res.StatusCode, b)
	}
	zvCheckCodexBody(t, up)
}

func TestZv_CodexChatClientIsTranslated(t *testing.T) {
	zvCodexHome(t)
	up, gw := zvStartCodex(t)
	res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}],"max_tokens":20,"temperature":0.5}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(b), "hello") {
		t.Fatalf("status %d, body %s", res.StatusCode, b)
	}
	zvCheckCodexBody(t, up)
}
