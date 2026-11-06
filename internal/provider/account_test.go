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
	"testing"
)

// isolate points HOME, XDG_* and the Claude config dir at a temp directory.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	return home
}

func writeTemp(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeJWT builds an unsigned token with the given payload claims.
func fakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func writeCodexLogin(t *testing.T, home, mode string) {
	t.Helper()
	idTok := fakeJWT(map[string]any{
		"email":                        "dev@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
	})
	accTok := fakeJWT(map[string]any{"exp": 4102444800}) // year 2100
	body, _ := json.Marshal(map[string]any{
		"auth_mode": mode,
		"tokens": map[string]any{
			"id_token":      idTok,
			"access_token":  accTok,
			"refresh_token": "rt-secret",
			"account_id":    "acct-1",
		},
	})
	writeTemp(t, filepath.Join(home, ".codex", "auth.json"), string(body))
}

func writeCopilotLogin(t *testing.T, home string) {
	t.Helper()
	writeTemp(t, filepath.Join(home, "cfg", "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octocat","oauth_token":"gho_secret"}}`)
}

func find(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func TestCodexDefaultBase(t *testing.T) {
	if CodexBase != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("CodexBase = %q", CodexBase)
	}
}

func TestCodexLoginListed(t *testing.T) {
	home := isolate(t)
	writeCodexLogin(t, home, "chatgpt")

	p := find(All(), Codex)
	if p == nil {
		t.Fatal("codex not in All() with a ChatGPT login")
	}
	if p.Account == nil || p.Account.User != "dev@example.com" || p.Account.Plan != "plus" {
		t.Fatalf("account = %+v", p.Account)
	}
	if !p.Ready() {
		t.Fatal("account provider should be ready without a key")
	}
	found := false
	for _, e := range Catalog() {
		if e.ID == "codex/gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Fatal("codex/gpt-5.5 missing from the catalog")
	}
}

func TestCodexAPIKeyIsNotAccount(t *testing.T) {
	home := isolate(t)
	writeCodexLogin(t, home, "apikey")
	if find(All(), Codex) != nil {
		t.Fatal("apikey-mode Codex must not be an account provider")
	}
}

func TestCopilotLoginListed(t *testing.T) {
	home := isolate(t)
	writeCopilotLogin(t, home)
	p := find(All(), Copilot)
	if p == nil || p.Account == nil || p.Account.User != "octocat" {
		t.Fatalf("copilot = %+v", p)
	}
	for _, m := range p.Exposed() {
		if m.ID == "auto" {
			t.Fatal("built-in Copilot list must not contain auto")
		}
	}
	if len(p.Exposed()) == 0 {
		t.Fatal("built-in Copilot list is empty")
	}
}

func TestClaudeNeverOffered(t *testing.T) {
	home := isolate(t)
	writeTemp(t, filepath.Join(home, ".claude", ".credentials.json"), "not json at all")
	writeTemp(t, filepath.Join(home, "cfg", "dial", "providers.json"), `{"providers":[{"id":"claude","models":["x"]}]}`)

	if find(All(), Claude) != nil {
		t.Fatal("claude must never be in All()")
	}
	if !ClaudeSignedIn() {
		t.Fatal("ClaudeSignedIn should see the credentials file")
	}
	if len(Notes()) != 1 {
		t.Fatalf("Notes() = %v, want one Claude note", Notes())
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "claude/") {
			t.Fatalf("claude model in catalog: %s", e.ID)
		}
	}
	if err := Save(Provider{ID: Claude, Models: []string{"x"}}); err == nil {
		t.Fatal("saving claude should fail")
	}
}

func TestSaveAccountWritesOnlyPicks(t *testing.T) {
	home := isolate(t)
	writeCodexLogin(t, home, "chatgpt")

	if err := Save(Provider{ID: Codex, Name: "Mine", Key: "secret-key", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "secret-key") || strings.Contains(s, "Mine") || strings.Contains(s, `"name"`) || strings.Contains(s, `"key"`) {
		t.Fatalf("config leaked fields: %s", s)
	}
	if strings.Contains(string(b), "rt-secret") {
		t.Fatal("login token written to dial config")
	}
	p := find(All(), Codex)
	if p == nil || len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Fatalf("picks not kept: %+v", p)
	}
	if len(p.Exposed()) != 1 {
		t.Fatalf("exposed = %d, want 1", len(p.Exposed()))
	}
}

func TestDeleteAccountClearsPicksOnly(t *testing.T) {
	home := isolate(t)
	writeCodexLogin(t, home, "chatgpt")
	if err := Save(Provider{ID: Codex, Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete(Codex); err != nil {
		t.Fatal(err)
	}
	p := find(All(), Codex)
	if p == nil {
		t.Fatal("deleting an account must not sign it out or hide it")
	}
	if len(p.Models) != 0 {
		t.Fatalf("picks not cleared: %v", p.Models)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal("Codex login file was touched")
	}
}

func TestSignedOutAccountDisappears(t *testing.T) {
	home := isolate(t)
	writeCodexLogin(t, home, "chatgpt")
	if err := Save(Provider{ID: Codex, Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if find(All(), Codex) != nil {
		t.Fatal("signed-out codex still listed")
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("stale codex picks must not resolve")
	}
}

func TestRefreshCopilotFiltersModels(t *testing.T) {
	home := isolate(t)
	writeCopilotLogin(t, home)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	defer func() { CopilotTokenURL = old }()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"token":     "sess-1",
			"endpoints": map[string]any{"api": srv.URL},
		})
	})
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "gpt-4.1", "name": "GPT-4.1", "model_picker_enabled": true, "capabilities": map[string]any{"type": "chat"}, "policy": map[string]any{"state": "enabled"}},
			{"id": "auto", "name": "Auto", "model_picker_enabled": true, "capabilities": map[string]any{"type": "chat"}},
			{"id": "text-embedding-3", "model_picker_enabled": true, "capabilities": map[string]any{"type": "embeddings"}},
			{"id": "internal-router", "model_picker_enabled": false, "capabilities": map[string]any{"type": "chat"}},
			{"id": "gpt-off", "model_picker_enabled": true, "capabilities": map[string]any{"type": "chat"}, "policy": map[string]any{"state": "disabled"}},
			{"id": "claude-sonnet-4.5", "name": "Claude Sonnet 4.5", "model_picker_enabled": true, "capabilities": map[string]any{"type": "chat"}, "policy": map[string]any{"state": "enabled"}},
		}})
	})

	ms, err := RefreshCopilot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "gpt-4.1,claude-sonnet-4.5" {
		t.Fatalf("refreshed ids = %v", ids)
	}

	p := find(All(), Copilot)
	if p == nil {
		t.Fatal("copilot missing")
	}
	live := p.Available()
	if len(live) != 2 || live[0].ID != "gpt-4.1" {
		t.Fatalf("saved live list not used: %v", live)
	}
	if _, ok := p.Fetched(); !ok {
		t.Fatal("refresh should be remembered")
	}
}

func TestCopilotRejectedAsksForSignIn(t *testing.T) {
	home := isolate(t)
	writeCopilotLogin(t, home)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL
	defer func() { CopilotTokenURL = old }()

	_, err := RefreshCopilot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Fatalf("err = %v, want a sign-in prompt", err)
	}
}
