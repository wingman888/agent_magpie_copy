package provider

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
	"sync/atomic"
	"testing"
	"time"
)

// s2Isolate points HOME and friends at empty temp dirs, restores the package
// variables the tests replace, and clears the Copilot session cache.
func s2Isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	oldBase, oldRefresh, oldToken := CodexBase, CodexRefreshURL, CopilotTokenURL
	t.Cleanup(func() {
		CodexBase, CodexRefreshURL, CopilotTokenURL = oldBase, oldRefresh, oldToken
	})
	copilotSessions.mu.Lock()
	copilotSessions.oauth = ""
	copilotSessions.sess = copilotSession{}
	copilotSessions.mu.Unlock()
	return home
}

func s2Write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// s2JWT builds an unsigned JWT with the given claims.
func s2JWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// s2Exp is an access token whose exp claim is d from now.
func s2Exp(d time.Duration) string {
	return s2JWT(map[string]any{"exp": time.Now().Add(d).Unix()})
}

// s2CodexFile writes a ChatGPT-mode auth.json with an extra field and returns its path.
func s2CodexFile(t *testing.T, home, access string) string {
	t.Helper()
	path := filepath.Join(home, ".codex", "auth.json")
	doc := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      s2JWT(map[string]any{"email": "dev@example.com"}),
			"access_token":  access,
			"refresh_token": "old-refresh",
			"account_id":    "acct-1",
		},
		"custom":       map[string]any{"keep": true},
		"last_refresh": "2020-01-01T00:00:00Z",
	}
	b, _ := json.Marshal(doc)
	s2Write(t, path, string(b))
	return path
}

func s2ReadDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestS2CodexSignRefreshesAndWritesBack(t *testing.T) {
	home := s2Isolate(t)
	path := s2CodexFile(t, home, s2Exp(time.Minute))
	newAccess := s2Exp(time.Hour)

	var refreshes int32
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&refreshes, 1)
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["grant_type"] != "refresh_token" || in["refresh_token"] != "old-refresh" || in["client_id"] == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": newAccess, "refresh_token": "new-refresh"})
	}))
	defer refresh.Close()

	var gotAuth, gotAcct, gotPath string
	var gotBody []byte
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAcct = r.Header.Get("chatgpt-account-id")
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	CodexRefreshURL = refresh.URL
	CodexBase = backend.URL + "/codex"

	p := Provider{ID: "codex", Name: "Codex", Account: &Account{Agent: "codex"}}
	body := p.Prepare([]byte(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":16,"temperature":0.2}`))
	req, _ := http.NewRequest(http.MethodPost, "http://dial.invalid/v1/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, body); err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if n := atomic.LoadInt32(&refreshes); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
	if gotAuth != "Bearer "+newAccess {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAcct != "acct-1" {
		t.Errorf("chatgpt-account-id = %q", gotAcct)
	}
	if gotPath != "/codex/responses" {
		t.Errorf("path = %q, want /codex/responses", gotPath)
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("backend body not JSON: %v", err)
	}
	if sent["model"] != "gpt-5.5" || sent["stream"] != true || sent["store"] != false {
		t.Errorf("backend body = %s", gotBody)
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := sent[k]; ok {
			t.Errorf("%s was sent to the backend", k)
		}
	}
	if in, ok := sent["input"].([]any); !ok || len(in) != 1 {
		t.Errorf("input = %v, want one message item", sent["input"])
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("auth.json mode = %v, want 0600", info.Mode().Perm())
	}
	doc := s2ReadDoc(t, path)
	tokens, ok := doc["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("tokens missing from auth.json: %v", doc)
	}
	if tokens["access_token"] != newAccess || tokens["refresh_token"] != "new-refresh" {
		t.Errorf("tokens not updated: %v", tokens)
	}
	if tokens["id_token"] == nil || tokens["account_id"] != "acct-1" {
		t.Errorf("tokens lost fields: %v", tokens)
	}
	if custom, _ := doc["custom"].(map[string]any); custom["keep"] != true {
		t.Errorf("unknown field dropped: %v", doc["custom"])
	}
	if _, ok := doc["OPENAI_API_KEY"]; !ok {
		t.Errorf("OPENAI_API_KEY dropped")
	}
	if doc["auth_mode"] != "chatgpt" {
		t.Errorf("auth_mode = %v", doc["auth_mode"])
	}
	if lr, _ := doc["last_refresh"].(string); lr == "" || lr == "2020-01-01T00:00:00Z" {
		t.Errorf("last_refresh not updated: %v", doc["last_refresh"])
	}
}

func TestS2CodexFreshTokenIsNotRefreshed(t *testing.T) {
	home := s2Isolate(t)
	access := s2Exp(time.Hour)
	s2CodexFile(t, home, access)
	var refreshes int32
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&refreshes, 1)
	}))
	defer refresh.Close()
	CodexRefreshURL = refresh.URL

	got, acct, err := codexToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != access || acct != "acct-1" {
		t.Errorf("token = %q, account = %q", got, acct)
	}
	if n := atomic.LoadInt32(&refreshes); n != 0 {
		t.Errorf("refresh calls = %d, want 0", n)
	}
}

func TestS2CodexConcurrentRequestsRefreshOnce(t *testing.T) {
	home := s2Isolate(t)
	path := s2CodexFile(t, home, s2Exp(-time.Minute))
	newAccess := s2Exp(time.Hour)
	var refreshes int32
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&refreshes, 1)
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": newAccess})
	}))
	defer refresh.Close()
	CodexRefreshURL = refresh.URL

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := codexToken(context.Background())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if n := atomic.LoadInt32(&refreshes); n != 1 {
		t.Errorf("refresh calls = %d, want 1", n)
	}
	doc := s2ReadDoc(t, path)
	tokens, _ := doc["tokens"].(map[string]any)
	if tokens["access_token"] != newAccess || tokens["refresh_token"] != "old-refresh" {
		t.Errorf("tokens = %v", tokens)
	}
}

func TestS2CodexRefreshFailureAsksToSignIn(t *testing.T) {
	home := s2Isolate(t)
	path := s2CodexFile(t, home, s2Exp(-time.Minute))
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusUnauthorized)
	}))
	defer refresh.Close()
	CodexRefreshURL = refresh.URL

	p := Provider{ID: "codex", Name: "Codex", Account: &Account{Agent: "codex"}}
	req, _ := http.NewRequest(http.MethodPost, "http://dial.invalid/v1/responses", nil)
	err := p.Sign(context.Background(), req, Responses, nil)
	if err == nil {
		t.Fatal("Sign succeeded with a rejected refresh token")
	}
	for _, want := range []string{"已登出", "请重新登录"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "old-refresh") {
		t.Errorf("auth.json changed after a failed refresh")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest(http.MethodPost, "http://dial.invalid/v1/responses", nil)
	err = p.Sign(context.Background(), req, Responses, nil)
	if err == nil || !strings.Contains(err.Error(), "请重新登录") {
		t.Errorf("signed out Sign error = %v, want a sign-in hint", err)
	}
}

func TestS2CodexPrepareShapesBody(t *testing.T) {
	p := Provider{ID: "codex", Account: &Account{Agent: "codex"}}
	in := `{"model":"codex/gpt-5.5","input":"hello","stream":false,"store":true,` +
		`"temperature":0.2,"top_p":1,"max_completion_tokens":5,"reasoning":{"effort":"high"}}`
	var got map[string]any
	if err := json.Unmarshal(p.Prepare([]byte(in)), &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gpt-5.5" {
		t.Errorf("model = %v", got["model"])
	}
	if got["stream"] != true || got["store"] != false {
		t.Errorf("stream/store = %v/%v", got["stream"], got["store"])
	}
	for _, k := range []string{"temperature", "top_p", "max_completion_tokens"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s kept", k)
		}
	}
	items, ok := got["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input = %v", got["input"])
	}
	item, _ := items[0].(map[string]any)
	content, _ := item["content"].([]any)
	part, _ := content[0].(map[string]any)
	if item["type"] != "message" || item["role"] != "user" || part["type"] != "input_text" || part["text"] != "hello" {
		t.Errorf("input item = %v", item)
	}
	reasoning, _ := got["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Errorf("reasoning dropped: %v", got["reasoning"])
	}

	plain := Provider{ID: "deepseek", Key: "sk-abc"}
	if string(plain.Prepare([]byte(in))) != in {
		t.Errorf("ordinary provider changed the body")
	}
}

func TestS2OrdinaryProviderSignsWithKey(t *testing.T) {
	p := Provider{ID: "relay", Key: "sk-abc", Chat: "https://api.example.com/v1"}
	req, _ := http.NewRequest(http.MethodPost, "https://api.example.com/v1/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-abc" {
		t.Errorf("Authorization = %q", got)
	}
}

// s2CopilotSeen records what the stand-in Copilot API was sent.
type s2CopilotSeen struct {
	mu        sync.Mutex
	exchanges int
	chats     int
	initiator string
	vision    string
	integ     string
	path      string
}

// s2CopilotServer stands in for the Copilot token exchange and chat API. The
// session token it issues expires after expiresIn.
func s2CopilotServer(t *testing.T, expiresIn time.Duration) (*httptest.Server, *s2CopilotSeen) {
	t.Helper()
	seen := &s2CopilotSeen{}
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_test" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		seen.mu.Lock()
		seen.exchanges++
		seen.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "sess-1",
			"expires_at": time.Now().Add(expiresIn).Unix(),
			"endpoints":  map[string]string{"api": base + "/api"},
		})
	})
	mux.HandleFunc("/api/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess-1" {
			http.Error(w, "bad session", http.StatusUnauthorized)
			return
		}
		seen.mu.Lock()
		seen.chats++
		seen.initiator = r.Header.Get("X-Initiator")
		seen.vision = r.Header.Get("Copilot-Vision-Request")
		seen.integ = r.Header.Get("Copilot-Integration-Id")
		seen.path = r.URL.Path
		seen.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL
	return srv, seen
}

func s2CopilotSend(t *testing.T, p Provider, body string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://dial.invalid/v1/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, []byte(body)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat status = %d", resp.StatusCode)
	}
}

func TestS2CopilotSignHeadersAndCache(t *testing.T) {
	home := s2Isolate(t)
	s2Write(t, filepath.Join(home, "xdg", "github-copilot", "apps.json"),
		`{"github.com:Iv1":{"user":"octo","oauth_token":"gho_test"}}`)
	srv, seen := s2CopilotServer(t, time.Hour)
	CopilotTokenURL = srv.URL + "/token"
	p := Provider{ID: "copilot", Name: "Copilot", Account: &Account{Agent: "copilot", User: "octo"}}

	s2CopilotSend(t, p, `{"model":"claude-sonnet-4.5","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`)
	seen.mu.Lock()
	if seen.initiator != "user" || seen.vision != "" || seen.integ == "" || seen.path != "/api/chat/completions" {
		t.Errorf("user turn: initiator=%q vision=%q integ=%q path=%q", seen.initiator, seen.vision, seen.integ, seen.path)
	}
	seen.mu.Unlock()

	s2CopilotSend(t, p, `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`)
	seen.mu.Lock()
	if seen.initiator != "user" || seen.vision != "true" {
		t.Errorf("vision turn: initiator=%q vision=%q", seen.initiator, seen.vision)
	}
	seen.mu.Unlock()

	s2CopilotSend(t, p, `{"model":"m","messages":[{"role":"user","content":"go"},{"role":"assistant","content":"ok"},{"role":"tool","content":"done","tool_call_id":"1"}]}`)
	seen.mu.Lock()
	if seen.initiator != "agent" || seen.vision != "" {
		t.Errorf("tool turn: initiator=%q vision=%q", seen.initiator, seen.vision)
	}
	if seen.exchanges != 1 {
		t.Errorf("token exchanges = %d, want 1 (cached)", seen.exchanges)
	}
	seen.mu.Unlock()
}

func TestS2CopilotReexchangesNearExpiry(t *testing.T) {
	home := s2Isolate(t)
	s2Write(t, filepath.Join(home, "xdg", "github-copilot", "hosts.json"),
		`{"github.com":{"user":"octo","oauth_token":"gho_test"}}`)
	srv, seen := s2CopilotServer(t, time.Minute) // inside the 5 minute margin
	CopilotTokenURL = srv.URL + "/token"
	p := Provider{ID: "copilot", Name: "Copilot", Account: &Account{Agent: "copilot"}}

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	s2CopilotSend(t, p, body)
	s2CopilotSend(t, p, body)
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if seen.exchanges != 2 {
		t.Errorf("token exchanges = %d, want 2", seen.exchanges)
	}
}
