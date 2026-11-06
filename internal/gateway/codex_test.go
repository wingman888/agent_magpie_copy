package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// cdxEnv points HOME and the dial config at fresh temp dirs and restores the
// provider endpoints the tests override.
func cdxEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	origBase, origRefresh := provider.CodexBase, provider.CodexRefreshURL
	t.Cleanup(func() {
		provider.CodexBase = origBase
		provider.CodexRefreshURL = origRefresh
	})
	return home
}

// cdxJWT builds an unsigned JWT with the given claims.
func cdxJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// cdxWriteAuth writes a ChatGPT-mode auth.json with an access token that
// expires in the given duration. extra fields are merged into tokens.
func cdxWriteAuth(t *testing.T, home string, expIn time.Duration, refresh string) string {
	t.Helper()
	access := cdxJWT(map[string]any{"exp": time.Now().Add(expIn).Unix()})
	idTok := cdxJWT(map[string]any{"email": "dev@example.com"})
	auth := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"custom_top":     "keep-me",
		"tokens": map[string]any{
			"id_token":      idTok,
			"access_token":  access,
			"refresh_token": refresh,
			"account_id":    "acct-cdx-1",
			"extra_field":   "keep-me-too",
		},
	}
	b, _ := json.Marshal(auth)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return access
}

// cdxBackend is a stand-in ChatGPT backend. It records the request it got
// and answers with a fixed body.
type cdxBackend struct {
	mu      sync.Mutex
	body    []byte
	head    http.Header
	calls   int32
	reply   string
	noCType bool
	code    int
}

func (b *cdxBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&b.calls, 1)
	body, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	b.body, b.head = body, r.Header.Clone()
	b.mu.Unlock()
	if b.noCType {
		w.Header()["Content-Type"] = nil
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	if b.code != 0 {
		w.WriteHeader(b.code)
	}
	io.WriteString(w, b.reply)
}

func cdxCompleted(text string) string {
	return "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.5",` +
		`"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + text + `"}]}],` +
		`"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}` + "\n\n"
}

func cdxDo(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	New().Handler().ServeHTTP(rec, req)
	return rec
}

func cdxSentJSON(t *testing.T, b *cdxBackend) map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal(b.body, &m); err != nil {
		t.Fatalf("backend got non-JSON body %q: %v", b.body, err)
	}
	return m
}

func TestCdxNonStreamConvertsAndReassembles(t *testing.T) {
	home := cdxEnv(t)
	access := cdxWriteAuth(t, home, time.Hour, "rt-cdx")
	b := &cdxBackend{reply: cdxCompleted("hello back")}
	up := httptest.NewServer(b)
	defer up.Close()
	provider.CodexBase = up.URL

	rec := cdxDo(t, `{"model":"codex/gpt-5.5","input":"hello","max_output_tokens":10,"temperature":0.2,"reasoning":{"effort":"low"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	got := cdxSentJSON(t, b)
	if got["stream"] != true {
		t.Errorf("stream = %v, want true", got["stream"])
	}
	if got["store"] != false {
		t.Errorf("store = %v, want false", got["store"])
	}
	if got["model"] != "gpt-5.5" {
		t.Errorf("model = %v, want gpt-5.5", got["model"])
	}
	items, ok := got["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input = %#v, want a one-item list", got["input"])
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s should be removed before reaching the backend", k)
		}
	}
	if _, ok := got["reasoning"]; !ok {
		t.Errorf("reasoning should be kept")
	}
	b.mu.Lock()
	auth, acct := b.head.Get("Authorization"), b.head.Get("chatgpt-account-id")
	b.mu.Unlock()
	if auth != "Bearer "+access {
		t.Errorf("Authorization = %q", auth)
	}
	if acct != "acct-cdx-1" {
		t.Errorf("chatgpt-account-id = %q", acct)
	}

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("client reply is not JSON: %v: %s", err, rec.Body)
	}
	if out["object"] != "response" {
		t.Errorf("object = %v", out["object"])
	}
	if out["model"] != "codex/gpt-5.5" {
		t.Errorf("model = %v, want the client's own name", out["model"])
	}
	if !strings.Contains(rec.Body.String(), "hello back") {
		t.Errorf("generated text missing: %s", rec.Body)
	}
	if out["usage"] == nil {
		t.Errorf("usage missing: %s", rec.Body)
	}
}

func TestCdxStreamForwardsEvents(t *testing.T) {
	home := cdxEnv(t)
	cdxWriteAuth(t, home, time.Hour, "rt-cdx")
	reply := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n" + cdxCompleted("streamed")
	b := &cdxBackend{reply: reply, noCType: true}
	up := httptest.NewServer(b)
	defer up.Close()
	provider.CodexBase = up.URL

	rec := cdxDo(t, `{"model":"codex/gpt-5.5","input":[{"role":"user","content":"x"}],"stream":true,"max_output_tokens":5}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if rec.Body.String() != reply {
		t.Errorf("client stream differs from backend stream:\n%s", rec.Body)
	}
	got := cdxSentJSON(t, b)
	if got["stream"] != true {
		t.Errorf("backend stream = %v", got["stream"])
	}
	if _, ok := got["max_output_tokens"]; ok {
		t.Errorf("max_output_tokens should be removed")
	}
}

func TestCdxNoContentTypeIsSniffedAsStream(t *testing.T) {
	home := cdxEnv(t)
	cdxWriteAuth(t, home, time.Hour, "rt-cdx")
	b := &cdxBackend{reply: cdxCompleted("sniffed"), noCType: true}
	up := httptest.NewServer(b)
	defer up.Close()
	provider.CodexBase = up.URL

	// A non-streaming client still gets one JSON object, not a raw stream.
	rec := cdxDo(t, `{"model":"codex/gpt-5.5","input":"x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"object":"response"`) {
		t.Errorf("expected reassembled response, got %s", rec.Body)
	}

	// A streaming client gets the event stream labelled as such.
	rec = cdxDo(t, `{"model":"codex/gpt-5.5","input":"x","stream":true}`)
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}

func TestCdxRefreshNearExpiryWritesBackAndKeepsFields(t *testing.T) {
	home := cdxEnv(t)
	cdxWriteAuth(t, home, 2*time.Minute, "rt-old")
	newAccess := cdxJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})

	var refreshed int32
	ref := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&refreshed, 1)
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["grant_type"] != "refresh_token" || in["refresh_token"] != "rt-old" || in["client_id"] == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": newAccess, "refresh_token": "rt-new"})
	}))
	defer ref.Close()
	provider.CodexRefreshURL = ref.URL

	b := &cdxBackend{reply: cdxCompleted("ok")}
	up := httptest.NewServer(b)
	defer up.Close()
	provider.CodexBase = up.URL

	rec := cdxDo(t, `{"model":"codex/gpt-5.5","input":"x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if atomic.LoadInt32(&refreshed) != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshed)
	}
	b.mu.Lock()
	auth := b.head.Get("Authorization")
	b.mu.Unlock()
	if auth != "Bearer "+newAccess {
		t.Errorf("backend got Authorization %q, want the refreshed token", auth)
	}

	p := filepath.Join(home, ".codex", "auth.json")
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("auth.json mode = %o, want 600", mode)
	}
	raw, _ := os.ReadFile(p)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["custom_top"] != "keep-me" {
		t.Errorf("top-level field lost: %s", raw)
	}
	tok, _ := m["tokens"].(map[string]any)
	if tok["access_token"] != newAccess || tok["refresh_token"] != "rt-new" {
		t.Errorf("tokens not updated: %s", raw)
	}
	if tok["extra_field"] != "keep-me-too" || tok["account_id"] != "acct-cdx-1" || tok["id_token"] == nil {
		t.Errorf("token fields lost: %s", raw)
	}
	if _, ok := m["OPENAI_API_KEY"]; !ok {
		t.Errorf("OPENAI_API_KEY field lost: %s", raw)
	}
}

func TestCdxConcurrentRequestsRefreshOnce(t *testing.T) {
	home := cdxEnv(t)
	cdxWriteAuth(t, home, time.Minute, "rt-old")
	newAccess := cdxJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	var refreshed int32
	ref := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&refreshed, 1)
		time.Sleep(50 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]string{"access_token": newAccess, "refresh_token": "rt-new"})
	}))
	defer ref.Close()
	provider.CodexRefreshURL = ref.URL
	b := &cdxBackend{reply: cdxCompleted("ok")}
	up := httptest.NewServer(b)
	defer up.Close()
	provider.CodexBase = up.URL

	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = cdxDo(t, `{"model":"codex/gpt-5.5","input":"x"}`).Code
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("request %d status %d", i, c)
		}
	}
	if n := atomic.LoadInt32(&refreshed); n != 1 {
		t.Errorf("refresh calls = %d, want 1", n)
	}
}

func TestCdxRefreshFailureAsksForLogin(t *testing.T) {
	home := cdxEnv(t)
	cdxWriteAuth(t, home, time.Minute, "rt-revoked")
	ref := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer ref.Close()
	provider.CodexRefreshURL = ref.URL
	b := &cdxBackend{reply: cdxCompleted("never")}
	up := httptest.NewServer(b)
	defer up.Close()
	provider.CodexBase = up.URL

	rec := cdxDo(t, `{"model":"codex/gpt-5.5","input":"x"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "codex login") {
		t.Errorf("error should ask the user to sign in again: %s", rec.Body)
	}
	if n := atomic.LoadInt32(&b.calls); n != 0 {
		t.Errorf("backend should not be called after a failed refresh, got %d calls", n)
	}
}

func TestCdxBackendUnauthorizedAsksForLogin(t *testing.T) {
	home := cdxEnv(t)
	cdxWriteAuth(t, home, time.Hour, "rt-cdx")
	b := &cdxBackend{reply: `{"error":"nope"}`, code: http.StatusForbidden}
	up := httptest.NewServer(b)
	defer up.Close()
	provider.CodexBase = up.URL

	rec := cdxDo(t, `{"model":"codex/gpt-5.5","input":"x"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "codex login") {
		t.Errorf("error should ask the user to sign in again: %s", rec.Body)
	}
}

func TestCdxPrepareBodyShape(t *testing.T) {
	in := []byte(`{"model":"gpt-5.5","input":"hi","top_p":0.9,"max_tokens":3,"max_completion_tokens":4,"reasoning":{"effort":"high"}}`)
	out := (provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex"}}).Prepare(in)
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"top_p", "max_tokens", "max_completion_tokens"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s should be removed", k)
		}
	}
	if m["stream"] != true || m["store"] != false {
		t.Errorf("stream/store wrong: %s", out)
	}
	if _, ok := m["reasoning"]; !ok {
		t.Errorf("reasoning should be kept: %s", out)
	}
}
