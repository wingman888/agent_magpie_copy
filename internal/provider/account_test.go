package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// acctEnv points HOME, XDG and CLAUDE_CONFIG_DIR at a fresh temp tree.
func acctEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdgconfig"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdgcache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

// acctJWT builds an unsigned JWT with the given payload claims.
func acctJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// acctPut writes a file, creating its directory.
func acctPut(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// acctCodexLogin writes a ChatGPT login for Codex with the given access exp.
func acctCodexLogin(t *testing.T, home string, accessExp time.Time) {
	t.Helper()
	id := acctJWT(map[string]any{
		"email": "dev@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
	})
	access := acctJWT(map[string]any{"exp": accessExp.Unix()})
	doc := map[string]any{
		"auth_mode": "chatgpt",
		"other":     "keep-me",
		"tokens": map[string]any{
			"id_token":      id,
			"access_token":  access,
			"refresh_token": "rt-old",
			"account_id":    "acct-123",
		},
	}
	b, _ := json.Marshal(doc)
	acctPut(t, filepath.Join(home, ".codex", "auth.json"), string(b))
}

func acctFind(ps []Provider, id string) (Provider, bool) {
	for _, p := range ps {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

func TestAcct_CodexSignedInListed(t *testing.T) {
	home := acctEnv(t)
	acctCodexLogin(t, home, time.Now().Add(time.Hour))

	p, ok := acctFind(All(), "codex")
	if !ok {
		t.Fatal("codex not listed with a ChatGPT login")
	}
	if !p.Ready() || p.Key != "" {
		t.Errorf("codex should be ready without a key, got ready=%v key=%q", p.Ready(), p.Key)
	}
	if p.Account == nil || p.Account.User != "dev@example.com" || p.Account.Plan != "plus" || p.Account.Source != "Codex" {
		t.Errorf("unexpected account: %+v", p.Account)
	}
	if p.Responses != CodexBase {
		t.Errorf("codex responses base = %q, want CodexBase", p.Responses)
	}
	found := false
	for _, e := range Catalog() {
		if e.ID == "codex/gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Error("codex/gpt-5.5 missing from the catalog")
	}
}

func TestAcct_CodexOverrideBase(t *testing.T) {
	home := acctEnv(t)
	acctCodexLogin(t, home, time.Now().Add(time.Hour))
	old := CodexBase
	CodexBase = "http://127.0.0.1:9/fake"
	t.Cleanup(func() { CodexBase = old })
	p, ok := acctFind(All(), "codex")
	if !ok || p.Responses != "http://127.0.0.1:9/fake" {
		t.Fatalf("CodexBase override not used: %+v", p)
	}
}

func TestAcct_CodexAPIKeyIsNotAccount(t *testing.T) {
	home := acctEnv(t)
	acctPut(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-x"}`)
	if _, ok := acctFind(All(), "codex"); ok {
		t.Error("apikey login must not be an account provider")
	}
}

func TestAcct_LoggedOutIgnoresLeftoverPicks(t *testing.T) {
	home := acctEnv(t)
	acctCodexLogin(t, home, time.Now().Add(time.Hour))
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if _, ok := acctFind(All(), "codex"); ok {
		t.Error("logged-out codex still listed")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Errorf("leftover codex pick became a catalog entry: %s", e.ID)
		}
	}
}

func TestAcct_SaveAndDeleteOnlyTouchPicks(t *testing.T) {
	home := acctEnv(t)
	acctCodexLogin(t, home, time.Now().Add(time.Hour))
	if err := Save(Provider{ID: "codex", Name: "Codex", Key: "", Models: []string{"gpt-5.5", "gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "dev@example.com") || strings.Contains(string(raw), "acct-123") || strings.Contains(string(raw), "rt-old") {
		t.Errorf("credentials leaked into dial config: %s", raw)
	}
	p, ok := acctFind(All(), "codex")
	if !ok || len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Fatalf("picks not saved: %+v", p)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p, ok = acctFind(All(), "codex")
	if !ok {
		t.Fatal("Delete must not remove a signed-in account provider")
	}
	if len(p.Models) != 0 {
		t.Errorf("Delete should clear picks, got %v", p.Models)
	}
	// the login file is untouched
	b, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if !strings.Contains(string(b), "rt-old") {
		t.Error("Delete touched the Codex login file")
	}
}

func TestAcct_CopilotListAndFilter(t *testing.T) {
	home := acctEnv(t)
	acctPut(t, filepath.Join(home, "xdgconfig", "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_secret"}}`)
	p, ok := acctFind(All(), "copilot")
	if !ok {
		t.Fatal("copilot not listed with a GitHub login")
	}
	if p.Account == nil || p.Account.User != "octo" || p.Account.Source != "GitHub Copilot" {
		t.Errorf("unexpected account: %+v", p.Account)
	}
	ms := p.Available()
	if len(ms) == 0 {
		t.Fatal("copilot built-in list is empty")
	}
	for _, m := range ms {
		if m.ID == "auto" {
			t.Error("auto must not be offered")
		}
	}
	for _, e := range Catalog() {
		if e.ID == "copilot/auto" {
			t.Error("copilot/auto in catalog")
		}
	}
}

func TestAcct_CopilotSessionMessage(t *testing.T) {
	acctEnv(t)
	if _, err := CopilotGitHubToken(); err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Errorf("missing Copilot login should ask to sign in again, got %v", err)
	}
}

func TestAcct_ClaudeNeverAProvider(t *testing.T) {
	home := acctEnv(t)
	acctPut(t, filepath.Join(home, "claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"sk-ant-secret"}}`)
	acctPut(t, Path(), `{"providers":[{"id":"claude","name":"Claude","key":"sk-ant-secret","anthropic":"https://api.anthropic.com"}]}`)

	if _, ok := acctFind(All(), "claude"); ok {
		t.Error("claude listed as a provider")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "claude/") {
			t.Errorf("claude in catalog: %s", e.ID)
		}
	}
	if _, err := Find("claude"); err == nil {
		t.Error("Find(claude) should fail")
	}
	if err := Save(Provider{ID: "claude", Models: []string{"x"}}); err == nil {
		t.Error("Save(claude) should be refused")
	}
	ns := Notices()
	if len(ns) != 1 || ns[0].Agent != "Claude Code" || !ns[0].Signed || !strings.Contains(ns[0].Text, "已登录但不提供") {
		t.Errorf("expected a Claude notice, got %+v", ns)
	}
	// the credentials file content must never be read into a provider
	for _, p := range All() {
		if strings.Contains(p.Key+p.Name, "sk-ant-secret") {
			t.Error("claude token surfaced in All()")
		}
	}
}

func TestAcct_ClaudeNoticeAbsentWhenLoggedOut(t *testing.T) {
	acctEnv(t)
	if ns := Notices(); len(ns) != 0 {
		t.Errorf("no Claude login, but notices: %+v", ns)
	}
}

func TestAcct_CodexTokenFreshNoRefresh(t *testing.T) {
	home := acctEnv(t)
	acctCodexLogin(t, home, time.Now().Add(time.Hour))
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	old := codexRefreshURL
	codexRefreshURL = srv.URL
	t.Cleanup(func() { codexRefreshURL = old })

	tok, acct, err := CodexToken(context.Background())
	if err != nil || tok == "" || acct != "acct-123" {
		t.Fatalf("CodexToken = %q %q %v", tok, acct, err)
	}
	if calls.Load() != 0 {
		t.Error("a fresh token must not be refreshed")
	}
}

func TestAcct_CodexTokenRefreshWritesBack(t *testing.T) {
	home := acctEnv(t)
	acctCodexLogin(t, home, time.Now().Add(time.Minute)) // inside the 5 minute window

	newAccess := acctJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "refresh_token" || body["refresh_token"] != "rt-old" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": newAccess, "refresh_token": "rt-new"})
	}))
	defer srv.Close()
	old := codexRefreshURL
	codexRefreshURL = srv.URL
	t.Cleanup(func() { codexRefreshURL = old })

	tok, _, err := CodexToken(context.Background())
	if err != nil || tok != newAccess {
		t.Fatalf("refreshed token not returned: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["other"] != "keep-me" {
		t.Error("unrelated field lost on write-back")
	}
	tokens := doc["tokens"].(map[string]any)
	if tokens["access_token"] != newAccess || tokens["refresh_token"] != "rt-new" || tokens["account_id"] != "acct-123" {
		t.Errorf("tokens not written back correctly: %v", tokens)
	}
}

func TestAcct_CodexRefreshFailureSaysLoggedOut(t *testing.T) {
	home := acctEnv(t)
	acctCodexLogin(t, home, time.Now().Add(-time.Minute))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	old := codexRefreshURL
	codexRefreshURL = srv.URL
	t.Cleanup(func() { codexRefreshURL = old })

	_, _, err := CodexToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "已登出，请运行 `codex login`") {
		t.Errorf("want logged-out message, got %v", err)
	}
}
