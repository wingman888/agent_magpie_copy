package provider

import (
	"context"
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

// setVar points a package variable at v for the rest of the test.
func setVar(t *testing.T, p *string, v string) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// expJWT is an unsigned JWT whose exp claim is at.
func expJWT(t *testing.T, at time.Time) string {
	t.Helper()
	return fakeJWT(t, map[string]any{"exp": at.Unix()})
}

// writeCodexTokens writes a ChatGPT-mode auth.json with an extra top-level
// field and an extra token field, so tests can see that both survive a refresh.
func writeCodexTokens(t *testing.T, home, access, refresh string) string {
	t.Helper()
	path := filepath.Join(home, ".codex", "auth.json")
	b, err := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"custom":         "keep-me",
		"tokens": map[string]any{
			"id_token":      codexIDToken(t, "me@example.com", "pro"),
			"access_token":  access,
			"refresh_token": refresh,
			"account_id":    "acct-1",
			"extra":         "keep-tok",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(b))
	return path
}

func newSignReq(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://dial.invalid/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func codexProvider() Provider {
	return Provider{ID: "codex", Name: "Codex", Account: &Account{Agent: "codex", User: "me@example.com"}}
}

func TestOrdinaryProviderSignsWithKey(t *testing.T) {
	p := Provider{ID: "relay", Name: "Relay", Key: "sk-1", Chat: "https://relay.example.com/v1"}
	req := newSignReq(t)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-1" {
		t.Fatalf("Authorization = %q, want the provider key", got)
	}
	if got := req.URL.String(); got != "https://dial.invalid/v1/responses" {
		t.Fatalf("ordinary provider must not rewrite the URL, got %s", got)
	}
}

func TestCodexSignUsesAgentTokenAndBackend(t *testing.T) {
	home := isolate(t)
	access := expJWT(t, time.Now().Add(time.Hour))
	writeCodexTokens(t, home, access, "rt")
	setVar(t, &CodexBase, "https://backend.example.test/codex")

	req := newSignReq(t)
	if err := codexProvider().Sign(context.Background(), req, Responses, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "https://backend.example.test/codex/responses" {
		t.Fatalf("URL = %s", got)
	}
	if req.Host != "" {
		t.Errorf("Host = %q, want it cleared so the new URL is used", req.Host)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+access {
		t.Errorf("Authorization = %q", got)
	}
	if got := req.Header.Get("chatgpt-account-id"); got != "acct-1" {
		t.Errorf("chatgpt-account-id = %q, want acct-1", got)
	}
}

func TestCodexSignWithoutLoginDemandsRelogin(t *testing.T) {
	isolate(t)
	err := codexProvider().Sign(context.Background(), newSignReq(t), Responses, nil)
	if err == nil || !strings.Contains(err.Error(), "请重新登录") {
		t.Fatalf("err = %v, want a relogin error", err)
	}
}

func TestCodexAPIKeyModeDemandsRelogin(t *testing.T) {
	home := isolate(t)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test"}`)
	err := codexProvider().Sign(context.Background(), newSignReq(t), Responses, nil)
	if err == nil || !strings.Contains(err.Error(), "请重新登录") {
		t.Fatalf("err = %v, want a relogin error in API-key mode", err)
	}
}

func TestCodexRefreshWritesBackAndKeepsOtherFields(t *testing.T) {
	home := isolate(t)
	path := writeCodexTokens(t, home, expJWT(t, time.Now().Add(2*time.Minute)), "rt-old")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var before0 map[string]any
	if err := json.Unmarshal(before, &before0); err != nil {
		t.Fatal(err)
	}
	origID := before0["tokens"].(map[string]any)["id_token"]

	newAccess := expJWT(t, time.Now().Add(time.Hour))
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		b, _ := io.ReadAll(r.Body)
		var in map[string]string
		json.Unmarshal(b, &in)
		if r.Method != http.MethodPost || in["grant_type"] != "refresh_token" ||
			in["refresh_token"] != "rt-old" || in["client_id"] != codexClientID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": newAccess, "refresh_token": "rt-new"})
	}))
	t.Cleanup(srv.Close)
	setVar(t, &CodexRefreshURL, srv.URL)
	setVar(t, &CodexBase, "https://backend.example.test/codex")

	req := newSignReq(t)
	if err := codexProvider().Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+newAccess {
		t.Fatalf("Authorization after refresh = %q, want the new token", got)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	tokens, ok := raw["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("tokens missing after refresh: %s", b)
	}
	if tokens["access_token"] != newAccess {
		t.Errorf("access_token not written back")
	}
	if tokens["refresh_token"] != "rt-new" {
		t.Errorf("refresh_token = %v, want rt-new", tokens["refresh_token"])
	}
	if tokens["id_token"] != origID {
		t.Errorf("id_token changed although the refresh did not return one")
	}
	if tokens["account_id"] != "acct-1" || tokens["extra"] != "keep-tok" {
		t.Errorf("token fields not preserved: %v", tokens)
	}
	if raw["custom"] != "keep-me" {
		t.Errorf("top-level field lost: custom = %v", raw["custom"])
	}
	if _, ok := raw["OPENAI_API_KEY"]; !ok {
		t.Errorf("OPENAI_API_KEY field dropped")
	}
	if raw["last_refresh"] == nil || raw["last_refresh"] == "" {
		t.Errorf("last_refresh not set")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("auth.json mode = %o, want 600", perm)
	}
}

func TestCodexRefreshFailureDemandsRelogin(t *testing.T) {
	home := isolate(t)
	path := writeCodexTokens(t, home, expJWT(t, time.Now().Add(time.Minute)), "rt-bad")
	before, _ := os.ReadFile(path)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token was already used"}`))
	}))
	t.Cleanup(srv.Close)
	setVar(t, &CodexRefreshURL, srv.URL)

	err := codexProvider().Sign(context.Background(), newSignReq(t), Responses, nil)
	if err == nil || !strings.Contains(err.Error(), "请重新登录") {
		t.Fatalf("err = %v, want a relogin error", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Errorf("auth.json changed although the refresh failed")
	}
}

func TestCodexConcurrentSignRefreshesOnce(t *testing.T) {
	home := isolate(t)
	writeCodexTokens(t, home, expJWT(t, time.Now().Add(time.Minute)), "rt")
	newAccess := expJWT(t, time.Now().Add(time.Hour))
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(50 * time.Millisecond) // widen the window a race would need
		json.NewEncoder(w).Encode(map[string]string{"access_token": newAccess, "refresh_token": "rt2"})
	}))
	t.Cleanup(srv.Close)
	setVar(t, &CodexRefreshURL, srv.URL)
	setVar(t, &CodexBase, "https://backend.example.test/codex")

	p := codexProvider()
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, "https://dial.invalid/v1/responses", nil)
			if err != nil {
				errs <- err
				return
			}
			errs <- p.Sign(context.Background(), req, Responses, nil)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("refresh calls = %d under %d concurrent signs, want 1", got, n)
	}
}

func TestPrepareCodexBodyForBackend(t *testing.T) {
	in := []byte(`{"model":"codex/gpt-5.5","input":"hi","stream":false,"store":true,` +
		`"max_output_tokens":16,"temperature":0.2,"top_p":0.9,"reasoning":{"effort":"high"}}`)
	out := codexProvider().Prepare(in)

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("prepared body is not JSON: %v: %s", err, out)
	}
	if m["stream"] != true {
		t.Errorf("stream = %v, want true", m["stream"])
	}
	if m["store"] != false {
		t.Errorf("store = %v, want false", m["store"])
	}
	if m["model"] != "gpt-5.5" {
		t.Errorf("model = %v, want the prefix removed", m["model"])
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s should have been removed", k)
		}
	}
	reasoning, _ := m["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Errorf("reasoning not kept: %v", m["reasoning"])
	}
	items, ok := m["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input = %v, want a one-item list", m["input"])
	}
	item, _ := items[0].(map[string]any)
	if item["type"] != "message" || item["role"] != "user" {
		t.Errorf("input item = %v, want a user message", item)
	}
	parts, _ := item["content"].([]any)
	if len(parts) != 1 {
		t.Fatalf("content = %v", item["content"])
	}
	part, _ := parts[0].(map[string]any)
	if part["type"] != "input_text" || part["text"] != "hi" {
		t.Errorf("content part = %v, want input_text 'hi'", part)
	}
}

func TestPrepareLeavesOrdinaryProviderAlone(t *testing.T) {
	in := []byte(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":16}`)
	p := Provider{ID: "relay", Key: "k", Chat: "https://relay.example.com/v1"}
	if got := string(p.Prepare(in)); got != string(in) {
		t.Fatalf("ordinary provider changed the body: %s", got)
	}
}

func TestCopilotSignCachesSessionAndSetsHeaders(t *testing.T) {
	isolate(t)
	resetCopilotCache()
	t.Cleanup(resetCopilotCache)
	hosts := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "hosts.json")
	writeFile(t, hosts, `{"github.com":{"user":"octo","oauth_token":"gho_x"}}`)

	var exchanges int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" || r.Header.Get("Authorization") != "token gho_x" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(&exchanges, 1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "sess-1",
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			"endpoints":  map[string]string{"api": "http://" + r.Host + "/api"},
		})
	}))
	t.Cleanup(srv.Close)
	setVar(t, &CopilotTokenURL, srv.URL+"/token")

	p, ok := findAccount(Accounts(), "copilot")
	if !ok {
		t.Fatal("copilot account missing")
	}
	sign := func(body string) *http.Request {
		t.Helper()
		req := newSignReq(t)
		req.URL.Path = "/v1/chat/completions"
		if err := p.Sign(context.Background(), req, Chat, []byte(body)); err != nil {
			t.Fatal(err)
		}
		return req
	}

	userBody := `{"model":"gpt-5","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`
	req := sign(userBody)
	if got := req.URL.String(); got != srv.URL+"/api/chat/completions" {
		t.Fatalf("URL = %s, want the session's endpoint", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sess-1" {
		t.Errorf("Authorization = %q", got)
	}
	if got := req.Header.Get("X-Initiator"); got != "user" {
		t.Errorf("X-Initiator = %q, want user", got)
	}
	if req.Header.Get("Copilot-Integration-Id") == "" {
		t.Error("Copilot-Integration-Id header missing")
	}
	if req.Header.Get("Copilot-Vision-Request") != "" {
		t.Error("text-only request marked as vision")
	}

	sign(userBody) // second call reuses the cached session
	if n := atomic.LoadInt32(&exchanges); n != 1 {
		t.Fatalf("token exchanges = %d, want 1 (cached)", n)
	}

	toolBody := `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"x"},{"role":"tool","content":"r"}]}`
	if got := sign(toolBody).Header.Get("X-Initiator"); got != "agent" {
		t.Errorf("X-Initiator after a tool reply = %q, want agent", got)
	}

	visionBody := `{"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`
	vr := sign(visionBody)
	if got := vr.Header.Get("Copilot-Vision-Request"); got != "true" {
		t.Errorf("Copilot-Vision-Request = %q, want true", got)
	}
	if got := vr.Header.Get("X-Initiator"); got != "user" {
		t.Errorf("X-Initiator for vision = %q, want user", got)
	}

	// A session nearly out of time is exchanged again.
	copilotCache.Lock()
	copilotCache.exp = time.Now().Add(time.Minute)
	copilotCache.Unlock()
	sign(userBody)
	if n := atomic.LoadInt32(&exchanges); n != 2 {
		t.Fatalf("token exchanges = %d after expiry, want 2", n)
	}

	// Logging out in Copilot stops signing at once, even with a cached session.
	if err := os.Remove(hosts); err != nil {
		t.Fatal(err)
	}
	err := p.Sign(context.Background(), newSignReq(t), Chat, []byte(userBody))
	if err == nil || !strings.Contains(err.Error(), "请重新登录") {
		t.Fatalf("err = %v after logout, want a relogin error", err)
	}
}

func resetCopilotCache() {
	copilotCache.Lock()
	defer copilotCache.Unlock()
	copilotCache.url = ""
	copilotCache.oauth = ""
	copilotCache.token = ""
	copilotCache.api = ""
	copilotCache.exp = time.Time{}
}
