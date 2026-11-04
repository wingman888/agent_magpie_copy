package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// codexSandbox points every credential and config location at a fresh
// directory and restores the provider's stand-in addresses afterwards.
func codexSandbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	oldBase, oldRefresh, oldClient := provider.CodexBase, provider.CodexRefreshURL, provider.CodexClientID
	t.Cleanup(func() {
		provider.CodexBase, provider.CodexRefreshURL, provider.CodexClientID = oldBase, oldRefresh, oldClient
	})
	return root
}

// jwt builds an unsigned token carrying the given claims.
func jwt(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// writeAuth writes a ChatGPT-mode auth.json with the given access token and
// extra fields, the way the Codex CLI keeps it.
func writeAuth(t *testing.T, access string, extra map[string]any) string {
	t.Helper()
	id := jwt(t, map[string]any{"email": "dev@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}})
	top := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"last_refresh":   "2026-01-01T00:00:00Z",
		"tokens": map[string]any{
			"id_token":      id,
			"access_token":  access,
			"refresh_token": "RT-OLD",
			"account_id":    "acct-1",
			"extra_field":   "keep-me",
		},
	}
	for k, v := range extra {
		top[k] = v
	}
	b, err := json.Marshal(top)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// freshAccess is a token that will not expire soon.
func freshAccess(t *testing.T) string {
	return jwt(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
}

// expiringAccess is a token that expires within the refresh slack.
func expiringAccess(t *testing.T) string {
	return jwt(t, map[string]any{"exp": time.Now().Add(time.Minute).Unix()})
}

// codexBackend is a stand-in for the ChatGPT Codex backend. It records what
// it was sent and replies with a script. A zero contentType sends no
// Content-Type header at all, as the real backend does.
type codexBackend struct {
	mu          sync.Mutex
	body        map[string]any
	auth        string
	account     string
	path        string
	reply       string
	code        int
	contentType string
}

func (b *codexBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.body = map[string]any{}
	json.Unmarshal(raw, &b.body)
	b.auth = r.Header.Get("Authorization")
	b.account = r.Header.Get("chatgpt-account-id")
	b.path = r.URL.Path
	if b.contentType == "" {
		w.Header()["Content-Type"] = nil // no header at all, so none is sniffed
	} else {
		w.Header().Set("Content-Type", b.contentType)
	}
	if b.code != 0 {
		w.WriteHeader(b.code)
	}
	io.WriteString(w, b.reply)
}

// codexReply is a Responses stream with one message and a completed event.
const codexReply = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}` + "\n\n" +
	"event: response.output_item.done\n" +
	`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello there"}]}}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","output":[],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}` + "\n\n"

// postJSON sends a request through the gateway handler and returns the recorder.
func postJSON(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	New().Handler().ServeHTTP(rec, req)
	return rec
}

func TestCodexNonStreamingIsAggregatedAndSentStreamed(t *testing.T) {
	codexSandbox(t)
	access := freshAccess(t)
	writeAuth(t, access, nil)
	backend := &codexBackend{reply: codexReply, contentType: "text/event-stream"}
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)
	provider.CodexBase = srv.URL + "/backend-api/codex"

	rec := postJSON(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"say hi","max_output_tokens":16,"temperature":0.2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	// What the backend received.
	got := backend.body
	if got["stream"] != true || got["store"] != false {
		t.Fatalf("stream/store = %v/%v", got["stream"], got["store"])
	}
	if got["model"] != "gpt-5.5" {
		t.Fatalf("model = %v; want the name without the codex/ prefix", got["model"])
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := got[k]; ok {
			t.Fatalf("%s must not reach the backend", k)
		}
	}
	items, ok := got["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input = %#v; want a list with one item", got["input"])
	}
	if backend.auth != "Bearer "+access {
		t.Fatalf("Authorization = %q", backend.auth)
	}
	if backend.account != "acct-1" {
		t.Fatalf("chatgpt-account-id = %q", backend.account)
	}
	if backend.path != "/backend-api/codex/responses" {
		t.Fatalf("path = %s", backend.path)
	}

	// What the client received: one JSON response object.
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("client reply is not JSON: %v\n%s", err, rec.Body.String())
	}
	if out["object"] != "response" {
		t.Fatalf("object = %v", out["object"])
	}
	output, _ := out["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output = %#v", out["output"])
	}
	content := output[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if content["text"] != "Hello there" {
		t.Fatalf("text = %v", content["text"])
	}
	usage, _ := out["usage"].(map[string]any)
	if usage["input_tokens"] != float64(10) || usage["output_tokens"] != float64(2) {
		t.Fatalf("usage = %v", out["usage"])
	}
}

func TestCodexStreamingIsRelayedWithoutContentType(t *testing.T) {
	codexSandbox(t)
	writeAuth(t, freshAccess(t), nil)
	backend := &codexBackend{reply: codexReply} // no Content-Type from the backend
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)
	provider.CodexBase = srv.URL

	rec := postJSON(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true,"max_output_tokens":8}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q; want text/event-stream", ct)
	}
	if !strings.Contains(rec.Body.String(), "event: response.completed") {
		t.Fatalf("stream was not relayed:\n%s", rec.Body.String())
	}
	if backend.body["stream"] != true {
		t.Fatalf("backend got stream = %v", backend.body["stream"])
	}
	if _, ok := backend.body["max_output_tokens"]; ok {
		t.Fatal("max_output_tokens must not reach the backend")
	}
}

func TestSniffEventStreamLeavesJSONAlone(t *testing.T) {
	codexSandbox(t)
	writeAuth(t, freshAccess(t), nil)
	backend := &codexBackend{reply: `{"ok":true}`} // no Content-Type, not a stream
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)
	provider.CodexBase = srv.URL

	p, _, ok := provider.Resolve("codex/gpt-5.5")
	if !ok {
		t.Fatal("codex/gpt-5.5 did not resolve")
	}
	s := New()
	res, err := s.forward(context.Background(), p, provider.Responses, "/responses", []byte(`{"model":"gpt-5.5","input":"x"}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "" {
		t.Fatalf("Content-Type = %q; a JSON-looking body must not be given a stream type", ct)
	}
	b, _ := io.ReadAll(res.Body)
	if string(b) != `{"ok":true}` {
		t.Fatalf("body = %s", b)
	}
}

func TestExpiringCodexTokenIsRefreshedAndWrittenBack(t *testing.T) {
	codexSandbox(t)
	path := writeAuth(t, expiringAccess(t), map[string]any{"custom_top": map[string]any{"n": 1}})
	newAccess := freshAccess(t)

	var refreshBody map[string]any
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&refreshBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": newAccess, "refresh_token": "RT-NEW"})
	}))
	t.Cleanup(refresh.Close)
	provider.CodexRefreshURL = refresh.URL
	provider.CodexClientID = "client-under-test"

	backend := &codexBackend{reply: codexReply, contentType: "text/event-stream"}
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)
	provider.CodexBase = srv.URL

	rec := postJSON(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if refreshBody["grant_type"] != "refresh_token" || refreshBody["refresh_token"] != "RT-OLD" || refreshBody["client_id"] != "client-under-test" {
		t.Fatalf("refresh request = %v", refreshBody)
	}
	if backend.auth != "Bearer "+newAccess {
		t.Fatalf("backend got Authorization %q; want the refreshed token", backend.auth)
	}

	// The agent's file: new tokens, everything else kept.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	tokens, _ := top["tokens"].(map[string]any)
	if tokens["access_token"] != newAccess || tokens["refresh_token"] != "RT-NEW" {
		t.Fatalf("tokens = %v", tokens)
	}
	if tokens["extra_field"] != "keep-me" || tokens["account_id"] != "acct-1" || tokens["id_token"] == nil {
		t.Fatalf("token fields lost: %v", tokens)
	}
	if top["auth_mode"] != "chatgpt" || top["last_refresh"] == "2026-01-01T00:00:00Z" {
		t.Fatalf("top-level fields = auth_mode %v, last_refresh %v", top["auth_mode"], top["last_refresh"])
	}
	if _, ok := top["OPENAI_API_KEY"]; !ok {
		t.Fatal("OPENAI_API_KEY was dropped")
	}
	if cu, _ := top["custom_top"].(map[string]any); cu["n"] != float64(1) {
		t.Fatalf("custom_top = %v", top["custom_top"])
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("auth.json mode = %o; want 600", perm)
	}
}

func TestFailedCodexRefreshAsksForSignIn(t *testing.T) {
	codexSandbox(t)
	writeAuth(t, expiringAccess(t), nil)
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"refresh token was revoked"}}`)
	}))
	t.Cleanup(refresh.Close)
	provider.CodexRefreshURL = refresh.URL

	backend := &codexBackend{reply: codexReply, contentType: "text/event-stream"}
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)
	provider.CodexBase = srv.URL

	rec := postJSON(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d; want 401: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "codex login") {
		t.Fatalf("error does not ask for sign-in: %s", rec.Body.String())
	}
	if backend.body != nil {
		t.Fatal("the backend must not be called with an unrefreshed token")
	}
}

func TestSignedOutCodexFailsClearly(t *testing.T) {
	codexSandbox(t)
	path := writeAuth(t, freshAccess(t), nil)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A stale model selection left for the logged-out login must not turn into a provider.
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}

	rec := postJSON(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d; want 401: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "codex login") {
		t.Fatalf("error does not ask for sign-in: %s", rec.Body.String())
	}
}

func TestBackendRefusalOfLoginAsksForSignIn(t *testing.T) {
	codexSandbox(t)
	writeAuth(t, freshAccess(t), nil)
	backend := &codexBackend{code: http.StatusUnauthorized, reply: `{"detail":"token expired"}`, contentType: "application/json"}
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)
	provider.CodexBase = srv.URL

	rec := postJSON(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":true}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d; want 401: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "codex login") {
		t.Fatalf("error does not ask for sign-in: %s", rec.Body.String())
	}
}
