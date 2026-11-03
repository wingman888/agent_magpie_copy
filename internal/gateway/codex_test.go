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
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// backend stands in for the ChatGPT Codex backend. It records what it got.
type backend struct {
	raw     []byte
	got     map[string]any
	path    string
	head    http.Header
	reply   string
	status  int
	noCType bool // the backend sends no Content-Type, as ChatGPT's does
}

func (b *backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.raw, _ = io.ReadAll(r.Body)
	b.path, b.head = r.URL.Path, r.Header.Clone()
	b.got = nil
	json.Unmarshal(b.raw, &b.got)
	if b.noCType {
		w.Header()["Content-Type"] = nil // present but empty: no sniffing either
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	if b.status != 0 {
		w.WriteHeader(b.status)
	}
	io.WriteString(w, b.reply)
}

// jwt makes an unsigned token carrying the claims; dial reads claims only.
func jwt(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func tokenExpiring(in time.Duration) string {
	return jwt(map[string]any{"exp": time.Now().Add(in).Unix()})
}

const testAccount = "acct_test_1"

// codexHome points HOME at a temp dir holding a ChatGPT sign-in for Codex
// with the given access token, and returns the auth.json path.
func codexHome(t *testing.T, access string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	auth := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"custom":         "keep me",
		"last_refresh":   "2026-01-01T00:00:00Z",
		"tokens": map[string]any{
			"id_token": jwt(map[string]any{"email": "me@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}}),
			"access_token":  access,
			"refresh_token": "rt_old",
			"account_id":    testAccount,
		},
	}
	b, _ := json.Marshal(auth)
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// pointCodex aims the backend and the refresh endpoint at stand-ins.
func pointCodex(t *testing.T, base, refresh string) {
	t.Helper()
	oldBase, oldRefresh := provider.CodexBase, provider.CodexRefreshURL
	provider.CodexBase, provider.CodexRefreshURL = base, refresh
	t.Cleanup(func() { provider.CodexBase, provider.CodexRefreshURL = oldBase, oldRefresh })
}

func serveBackend(t *testing.T, b *backend) string {
	t.Helper()
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	return up.URL
}

// noRefresh fails the test if dial tries to refresh a token that is fine.
func noRefresh(t *testing.T) string {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("token refreshed when it was not due")
		w.WriteHeader(500)
	}))
	t.Cleanup(up.Close)
	return up.URL
}

func post2(t *testing.T, path, body string) (int, http.Header, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Header(), rec.Body.String()
}

// codexStream is a Responses stream whose events carry only data: lines.
func codexStream(text string) string {
	return sse(
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}`,
		`data: {"type":"response.output_text.delta","delta":"`+text+`"}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.5","status":"completed","usage":{"input_tokens":7,"output_tokens":2}}}`)
}

func TestCodexNonStreamingIsRewrittenAndCollected(t *testing.T) {
	codexHome(t, tokenExpiring(time.Hour))
	be := &backend{noCType: true, reply: codexStream("Hello there")}
	pointCodex(t, serveBackend(t, be), noRefresh(t))

	code, _, body := post2(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":16,"temperature":0.2,"reasoning":{"effort":"low"}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}

	// what the backend got
	if be.path != "/responses" {
		t.Errorf("backend path %q", be.path)
	}
	if be.got["stream"] != true || be.got["store"] != false {
		t.Errorf("stream/store: %v %v", be.got["stream"], be.got["store"])
	}
	if be.got["model"] != "gpt-5.5" {
		t.Errorf("model %v", be.got["model"])
	}
	items, ok := be.got["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input is not a one-item list: %v", be.got["input"])
	}
	item := items[0].(map[string]any)
	content := item["content"].([]any)[0].(map[string]any)
	if item["role"] != "user" || content["text"] != "hi" {
		t.Errorf("input item %v", item)
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, has := be.got[k]; has {
			t.Errorf("backend got %s", k)
		}
	}
	if _, ok := be.got["reasoning"]; !ok {
		t.Errorf("reasoning was dropped")
	}
	if got := be.head.Get("Authorization"); got != "Bearer "+tokenOf(t) {
		t.Errorf("authorization %q", got)
	}
	if got := be.head.Get("chatgpt-account-id"); got != testAccount {
		t.Errorf("chatgpt-account-id %q", got)
	}

	// what the client got: one Responses object with the text and usage
	var out struct {
		Object string `json:"object"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not JSON: %v: %s", err, body)
	}
	if out.Object != "response" || len(out.Output) == 0 || out.Output[0].Content[0].Text != "Hello there" {
		t.Errorf("reply %s", body)
	}
	if out.Usage.InputTokens != 7 || out.Usage.OutputTokens != 2 {
		t.Errorf("usage %+v", out.Usage)
	}
}

// tokenOf reads the access token the test wrote, so assertions can name it.
func tokenOf(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	json.Unmarshal(b, &f)
	return f.Tokens.AccessToken
}

func TestCodexStreamingIsRelayedAndRecognizedWithoutContentType(t *testing.T) {
	codexHome(t, tokenExpiring(time.Hour))
	reply := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"Hi"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.5","status":"completed","usage":{"input_tokens":3,"output_tokens":1}}}` + "\n\n"
	be := &backend{noCType: true, reply: reply}
	pointCodex(t, serveBackend(t, be), noRefresh(t))

	code, hdr, body := post2(t, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"hi","max_output_tokens":16,"temperature":1}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if body != reply {
		t.Errorf("stream not relayed as sent:\n%s", body)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content type %q", ct)
	}
	if be.got["stream"] != true || be.got["store"] != false {
		t.Errorf("backend stream/store: %v %v", be.got["stream"], be.got["store"])
	}
	if _, has := be.got["temperature"]; has {
		t.Errorf("backend got temperature")
	}
	if _, has := be.got["max_output_tokens"]; has {
		t.Errorf("backend got max_output_tokens")
	}
	if _, ok := be.got["input"].([]any); !ok {
		t.Errorf("input is not a list: %v", be.got["input"])
	}
}

func TestCodexStreamWithContentTypeIsRelayed(t *testing.T) {
	codexHome(t, tokenExpiring(time.Hour))
	be := &backend{reply: codexStream("ok")}
	pointCodex(t, serveBackend(t, be), noRefresh(t))
	code, hdr, body := post2(t, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"hi"}`)
	if code != 200 || body != be.reply || !strings.HasPrefix(hdr.Get("Content-Type"), "text/event-stream") {
		t.Errorf("status %d type %q body %s", code, hdr.Get("Content-Type"), body)
	}
}

func TestCodexExpiringTokenIsRefreshedAndWrittenBack(t *testing.T) {
	path := codexHome(t, tokenExpiring(60*time.Second))
	before, _ := os.ReadFile(path)
	var prior map[string]any
	json.Unmarshal(before, &prior)
	priorIDToken := prior["tokens"].(map[string]any)["id_token"]

	fresh := tokenExpiring(time.Hour)
	var calls atomic.Int32
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if r.Method != "POST" || in["grant_type"] != "refresh_token" || in["refresh_token"] != "rt_old" ||
			in["client_id"] != "app_EMoamEEZ73f0CkXaXp7hrann" {
			t.Errorf("refresh request %s %v", r.Method, in)
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": fresh, "refresh_token": "rt_new"})
	}))
	defer refresh.Close()

	be := &backend{noCType: true, reply: codexStream("ok")}
	pointCodex(t, serveBackend(t, be), refresh.URL)
	code, _, body := post2(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if calls.Load() != 1 {
		t.Errorf("refresh called %d times", calls.Load())
	}
	if be.head.Get("Authorization") != "Bearer "+fresh {
		t.Errorf("backend got the old token: %q", be.head.Get("Authorization"))
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(after, &got); err != nil {
		t.Fatal(err)
	}
	tok := got["tokens"].(map[string]any)
	if tok["access_token"] != fresh || tok["refresh_token"] != "rt_new" {
		t.Errorf("tokens not written back: %v", tok)
	}
	if tok["account_id"] != testAccount || tok["id_token"] != priorIDToken {
		t.Errorf("token fields not kept: %v", tok)
	}
	if got["custom"] != "keep me" || got["auth_mode"] != "chatgpt" {
		t.Errorf("other fields not kept: %v", got)
	}
	if got["last_refresh"] == "2026-01-01T00:00:00Z" {
		t.Errorf("last_refresh not updated")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("auth.json mode %o, want 600", perm)
	}
}

func TestCodexRefreshFailureAsksForLogin(t *testing.T) {
	path := codexHome(t, tokenExpiring(60*time.Second))
	before, _ := os.ReadFile(path)
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	defer refresh.Close()
	be := &backend{noCType: true, reply: codexStream("ok")}
	pointCodex(t, serveBackend(t, be), refresh.URL)

	code, _, body := post2(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 401 || !strings.Contains(body, "codex login") || !strings.Contains(body, "signed out") {
		t.Errorf("status %d, want 401 asking for codex login: %s", code, body)
	}
	if be.path != "" {
		t.Errorf("backend was called with a dead sign-in")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("auth.json changed after a failed refresh")
	}
}

func TestCodexRejectedTokenAsksForLogin(t *testing.T) {
	codexHome(t, tokenExpiring(time.Hour))
	be := &backend{status: 401, reply: `{"error":{"message":"token invalidated"}}`}
	pointCodex(t, serveBackend(t, be), noRefresh(t))
	code, _, body := post2(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 401 || !strings.Contains(body, "codex login") {
		t.Errorf("status %d: %s", code, body)
	}
}

func TestCodexFreshTokenIsNotRefreshed(t *testing.T) {
	codexHome(t, tokenExpiring(time.Hour))
	be := &backend{noCType: true, reply: codexStream("ok")}
	pointCodex(t, serveBackend(t, be), noRefresh(t))
	if code, _, body := post2(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
}
