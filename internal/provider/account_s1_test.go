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

	"github.com/yetone/dial/internal/catalog"
)

// s1Isolate points HOME, XDG_CONFIG_HOME and CLAUDE_CONFIG_DIR at empty
// temporary directories so no real sign-in on the machine leaks in.
func s1Isolate(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

func s1Write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// s1JWT builds an unsigned JWT carrying the given claims.
func s1JWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func s1FindAccount(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func s1ResetCopilot() {
	copilotState.mu.Lock()
	copilotState.models = nil
	copilotState.mu.Unlock()
}

func TestS1CodexChatGPTSignIn(t *testing.T) {
	home := s1Isolate(t)
	idTok := s1JWT(map[string]any{
		"email":                       "amy@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
	})
	s1Write(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,
	  "tokens":{"id_token":"`+idTok+`","access_token":"acc-1","refresh_token":"ref","account_id":"acct-9"}}`)

	ps := Accounts()
	p := s1FindAccount(ps, "codex")
	if p == nil {
		t.Fatalf("codex account missing: %+v", ps)
	}
	if p.Account == nil || p.Account.Agent != "codex" || p.Account.User != "amy@example.com" || p.Account.Plan != "pro" {
		t.Fatalf("bad account: %+v", p.Account)
	}
	if !p.Ready() {
		t.Fatal("codex account should be ready without a key")
	}
	ms := p.Available()
	if !s1HasModel(ms, "gpt-5.5") {
		t.Fatalf("codex models lack gpt-5.5: %+v", ms)
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "acc-1") || strings.Contains(string(b), "ref") {
		t.Fatalf("token leaked into JSON: %s", b)
	}
}

func TestS1CodexNoEmailFallsBackToChatGPT(t *testing.T) {
	home := s1Isolate(t)
	s1Write(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"chatgpt","tokens":{"id_token":"","access_token":"acc"}}`)
	p := s1FindAccount(Accounts(), "codex")
	if p == nil || p.Account.User != "ChatGPT" || p.Account.Plan != "" {
		t.Fatalf("want ChatGPT fallback, got %+v", p)
	}
}

func TestS1CodexAPIKeyModeIsNotAccount(t *testing.T) {
	home := s1Isolate(t)
	s1Write(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-x","tokens":{"access_token":"acc"}}`)
	if p := s1FindAccount(Accounts(), "codex"); p != nil {
		t.Fatalf("API-key mode must not be an account: %+v", p)
	}
}

func TestS1CodexNoAccessTokenIsNotAccount(t *testing.T) {
	home := s1Isolate(t)
	s1Write(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"chatgpt","tokens":{"access_token":""}}`)
	if p := s1FindAccount(Accounts(), "codex"); p != nil {
		t.Fatalf("empty access token must not be an account: %+v", p)
	}
}

func TestS1LoggedOutCodexDisappears(t *testing.T) {
	home := s1Isolate(t)
	path := filepath.Join(home, ".codex", "auth.json")
	s1Write(t, path, `{"auth_mode":"chatgpt","tokens":{"access_token":"acc"}}`)
	if s1FindAccount(Accounts(), "codex") == nil {
		t.Fatal("codex should be signed in")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if s1FindAccount(Accounts(), "codex") != nil {
		t.Fatal("codex should vanish after logout")
	}
}

func TestS1CopilotSignInAndFallbackName(t *testing.T) {
	s1Isolate(t)
	xdg := os.Getenv("XDG_CONFIG_HOME")
	s1Write(t, filepath.Join(xdg, "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octocat","oauth_token":"gho_1"}}`)
	p := s1FindAccount(Accounts(), "copilot")
	if p == nil || p.Account.User != "octocat" || p.Account.Agent != "copilot" || !p.Ready() {
		t.Fatalf("bad copilot account: %+v", p)
	}

	s1Write(t, filepath.Join(xdg, "github-copilot", "apps.json"), `{}`)
	s1Write(t, filepath.Join(xdg, "github-copilot", "hosts.json"),
		`{"github.com":{"oauth_token":"gho_2"}}`)
	p = s1FindAccount(Accounts(), "copilot")
	if p == nil || p.Account.User != "GitHub" {
		t.Fatalf("want GitHub fallback from hosts.json, got %+v", p)
	}
}

func TestS1CopilotIgnoresNonGitHubEntries(t *testing.T) {
	s1Isolate(t)
	xdg := os.Getenv("XDG_CONFIG_HOME")
	s1Write(t, filepath.Join(xdg, "github-copilot", "hosts.json"),
		`{"gitlab.com":{"user":"x","oauth_token":"t"}}`)
	if s1FindAccount(Accounts(), "copilot") != nil {
		t.Fatal("non-github.com entry must not count")
	}
}

func TestS1ClaudeIsExcludedNotOffered(t *testing.T) {
	s1Isolate(t)
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if got := Excluded(); len(got) != 0 {
		t.Fatalf("no claude login, want no exclusions: %+v", got)
	}
	s1Write(t, filepath.Join(dir, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"secret"}}`)
	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "claude" || strings.TrimSpace(ex[0].Why) == "" {
		t.Fatalf("want one claude exclusion, got %+v", ex)
	}
	if strings.Contains(ex[0].Why, "secret") {
		t.Fatal("exclusion text must not contain credentials")
	}
	if s1FindAccount(Accounts(), "claude") != nil {
		t.Fatal("claude must never be an account provider")
	}
}

func TestS1CodexModelsBuiltin(t *testing.T) {
	s1Isolate(t)
	ms := accountModels(Provider{Account: &Account{Agent: "codex"}})
	if !s1HasModel(ms, "gpt-5.5") {
		t.Fatalf("builtin codex list lacks gpt-5.5: %+v", ms)
	}
}

func TestS1CopilotBuiltinHasNoAuto(t *testing.T) {
	s1Isolate(t)
	s1ResetCopilot()
	ms := accountModels(Provider{Account: &Account{Agent: "copilot"}})
	if s1HasModel(ms, "auto") {
		t.Fatal("builtin copilot list must not contain auto")
	}
	if !s1HasModel(ms, "claude-sonnet-4.5") {
		t.Fatal("builtin copilot list lacks claude-sonnet-4.5")
	}
}

func TestS1CopilotRefreshFiltersAndSignsRequests(t *testing.T) {
	s1Isolate(t)
	s1ResetCopilot()
	t.Cleanup(s1ResetCopilot)
	xdg := os.Getenv("XDG_CONFIG_HOME")
	s1Write(t, filepath.Join(xdg, "github-copilot", "apps.json"),
		`{"github.com:Iv1":{"user":"octo","oauth_token":"gho_tok"}}`)

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_tok" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"sess-1","expires_at":4102444800,"endpoints":{"api":"` + "http://" + r.Host + `/api"}}`))
	})
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess-1" || r.Header.Get("Copilot-Integration-Id") == "" {
			http.Error(w, "bad headers", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
		 {"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","model_picker_enabled":true,
		  "capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}}},
		 {"id":"copilot-search-x","model_picker_enabled":true,"capabilities":{"type":"chat"}},
		 {"id":"embed-1","model_picker_enabled":true,"capabilities":{"type":"embeddings"}},
		 {"id":"gpt-5.5-secondary","model_picker_enabled":true,"capabilities":{"type":"chat"}},
		 {"id":"disabled-one","model_picker_enabled":false,"policy":{"state":"disabled"},"capabilities":{"type":"chat"}},
		 {"id":"policy-on","model_picker_enabled":false,"policy":{"state":"enabled"},"capabilities":{"type":"chat"}},
		 {"id":"gpt-5.4","name":"GPT-5.4","model_picker_enabled":true,"capabilities":{"type":"chat"}}
		]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	t.Cleanup(func() { CopilotTokenURL = old })

	ms, err := RefreshCopilot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "claude-sonnet-4.5,policy-on,gpt-5.4" {
		t.Fatalf("filtered ids = %v", ids)
	}
	if got := ms[0].Efforts; len(got) != 2 || got[0] != "low" || got[1] != "high" {
		t.Fatalf("efforts not kept: %v", got)
	}

	// The refreshed list is what the account now offers.
	p := Provider{ID: "copilot", Account: &Account{Agent: "copilot", User: "octo"}}
	if got := p.Available(); len(got) != 3 || got[0].ID != "claude-sonnet-4.5" {
		t.Fatalf("available after refresh = %+v", got)
	}
}

func TestS1CopilotRefreshNoChatModelsErrors(t *testing.T) {
	s1Isolate(t)
	s1ResetCopilot()
	t.Cleanup(s1ResetCopilot)
	xdg := os.Getenv("XDG_CONFIG_HOME")
	s1Write(t, filepath.Join(xdg, "github-copilot", "apps.json"),
		`{"github.com":{"user":"octo","oauth_token":"gho_tok"}}`)

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			_, _ = w.Write([]byte(`{"token":"s","expires_at":1,"endpoints":{"api":"` + srvURL + `"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"x","capabilities":{"type":"embeddings"}}]}`))
	}))
	defer srv.Close()
	srvURL = srv.URL
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	t.Cleanup(func() { CopilotTokenURL = old })

	if _, err := RefreshCopilot(context.Background()); err == nil {
		t.Fatal("expected an error when no chat models remain")
	}
}

func s1HasModel(ms []catalog.Model, id string) bool {
	for _, m := range ms {
		if m.ID == id {
			return true
		}
	}
	return false
}
