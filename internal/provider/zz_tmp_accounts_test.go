package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sacctEnv points HOME and XDG_CONFIG_HOME at a fresh temp dir so no real
// login or dial config is read.
func sacctEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

func sacctWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// sacctJWT builds an unsigned JWT whose payload is the given claims.
func sacctJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

const sacctAccessToken = "tok-sacct-access-7f3a"

func sacctCodexLogin(t *testing.T, home string) {
	t.Helper()
	idTok := sacctJWT(t, map[string]any{
		"email": "dev@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type": "pro",
		},
	})
	auth := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      idTok,
			"access_token":  sacctAccessToken,
			"refresh_token": "rt-sacct",
			"account_id":    "acct-sacct-1",
		},
	}
	b, _ := json.Marshal(auth)
	sacctWrite(t, filepath.Join(home, ".codex", "auth.json"), string(b))
}

func sacctFind(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func TestSacctCodexChatGPTLoginIsAccount(t *testing.T) {
	home := sacctEnv(t)
	sacctCodexLogin(t, home)

	p := sacctFind(Accounts(), "codex")
	if p == nil {
		t.Fatal("codex ChatGPT login should be offered as a provider")
	}
	if p.Account == nil || p.Account.Agent != "codex" || p.Account.User != "dev@example.com" || p.Account.Plan != "pro" {
		t.Fatalf("unexpected account info: %+v", p.Account)
	}
	if !p.Ready() {
		t.Fatal("account provider should be ready without a key")
	}
	if p.Key != "" {
		t.Fatal("account provider must not carry a key")
	}
}

func TestSacctCodexAPIKeyModeIsNotAccount(t *testing.T) {
	home := sacctEnv(t)
	sacctWrite(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-sacct-test","tokens":{"access_token":"x"}}`)

	if sacctFind(Accounts(), "codex") != nil {
		t.Fatal("API-key mode Codex must not be treated as an account")
	}
}

func TestSacctCodexLogoutRemovesProvider(t *testing.T) {
	home := sacctEnv(t)
	sacctCodexLogin(t, home)
	if sacctFind(Accounts(), "codex") == nil {
		t.Fatal("precondition: codex should be signed in")
	}
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if n := len(Accounts()); n != 0 {
		t.Fatalf("after logout Accounts should be empty, got %d", n)
	}
}

func TestSacctCodexDefaultsUserWhenNoEmail(t *testing.T) {
	home := sacctEnv(t)
	sacctWrite(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"chatgpt","tokens":{"id_token":"a.b.c","access_token":"`+sacctAccessToken+`"}}`)

	p := sacctFind(Accounts(), "codex")
	if p == nil || p.Account.User != "ChatGPT" {
		t.Fatalf("expected user ChatGPT, got %+v", p)
	}
}

func TestSacctCopilotDefaultsUserToGitHub(t *testing.T) {
	sacctEnv(t)
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot")
	sacctWrite(t, filepath.Join(cfg, "hosts.json"),
		`{"github.com":{"oauth_token":"gho_sacct"}}`)

	p := sacctFind(Accounts(), "copilot")
	if p == nil {
		t.Fatal("copilot login should be offered")
	}
	if p.Account.Agent != "copilot" || p.Account.User != "GitHub" {
		t.Fatalf("unexpected copilot account: %+v", p.Account)
	}
}

func TestSacctCopilotUsesUserField(t *testing.T) {
	sacctEnv(t)
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot")
	sacctWrite(t, filepath.Join(cfg, "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo-sacct","oauth_token":"gho_sacct"}}`)

	p := sacctFind(Accounts(), "copilot")
	if p == nil || p.Account.User != "octo-sacct" {
		t.Fatalf("expected user octo-sacct, got %+v", p)
	}
}

func TestSacctClaudeIsExcludedNotProvider(t *testing.T) {
	home := sacctEnv(t)
	sacctWrite(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"secret-sacct"}}`)

	if sacctFind(Accounts(), "claude") != nil {
		t.Fatal("claude must never be an account provider")
	}
	var found *Exclusion
	for _, e := range Excluded() {
		if e.Agent == "claude" {
			e := e
			found = &e
		}
	}
	if found == nil {
		t.Fatal("claude login should be reported as excluded")
	}
	if strings.TrimSpace(found.Why) == "" {
		t.Fatal("exclusion must explain why")
	}
	if strings.Contains(found.Why, "secret-sacct") {
		t.Fatal("exclusion text must not contain credentials")
	}
}

func TestSacctNoClaudeLoginNoExclusion(t *testing.T) {
	sacctEnv(t)
	if n := len(Excluded()); n != 0 {
		t.Fatalf("without a Claude login there should be no exclusion, got %d", n)
	}
}

func TestSacctUserProviderWinsOverAccount(t *testing.T) {
	home := sacctEnv(t)
	sacctCodexLogin(t, home)
	sacctWrite(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "dial", "providers.json"),
		`{"providers":[{"id":"codex","name":"Mine","chat":"https://relay.example.com/v1","key":"k"}]}`)

	if sacctFind(Accounts(), "codex") != nil {
		t.Fatal("a user-built provider with the same id and an address must win")
	}
}

func TestSacctStoredSelectionDoesNotBlockAccount(t *testing.T) {
	home := sacctEnv(t)
	sacctCodexLogin(t, home)
	sacctWrite(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "dial", "providers.json"),
		`{"providers":[{"id":"codex","name":"Codex","models":["gpt-5.5"]}]}`)

	if sacctFind(Accounts(), "codex") == nil {
		t.Fatal("a stored model selection without an address must not block the account")
	}
}

func TestSacctAccountJSONHasNoToken(t *testing.T) {
	home := sacctEnv(t)
	sacctCodexLogin(t, home)

	b, err := json.Marshal(Accounts())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, secret := range []string{sacctAccessToken, "rt-sacct", "acct-sacct-1"} {
		if strings.Contains(s, secret) {
			t.Fatalf("JSON leaks credential %q: %s", secret, s)
		}
	}
	if !strings.Contains(s, `"account"`) {
		t.Fatalf("JSON should carry the account field: %s", s)
	}
}

func TestSacctSignCodexSetsTokenAndAccount(t *testing.T) {
	home := sacctEnv(t)
	sacctCodexLogin(t, home)
	orig := CodexBase
	CodexBase = "https://backend.example.test/codex"
	t.Cleanup(func() { CodexBase = orig })

	p := sacctFind(Accounts(), "codex")
	if p == nil {
		t.Fatal("precondition: codex signed in")
	}
	req, _ := http.NewRequest(http.MethodPost, "https://unused.example.test/x", nil)
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "https://backend.example.test/codex/responses" {
		t.Fatalf("URL = %s", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+sacctAccessToken {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("chatgpt-account-id"); got != "acct-sacct-1" {
		t.Fatalf("chatgpt-account-id = %q", got)
	}
}

func TestSacctSignPlainProviderUsesKey(t *testing.T) {
	p := Provider{ID: "plain", Name: "Plain", Chat: "https://x.example.test/v1", Key: "key-sacct"}
	req, _ := http.NewRequest(http.MethodPost, "https://x.example.test/v1/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer key-sacct" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestSacctPrepareKeepsBody(t *testing.T) {
	body := []byte(`{"model":"m"}`)
	if got := (Provider{ID: "p"}).Prepare(body); string(got) != string(body) {
		t.Fatalf("Prepare changed body: %s", got)
	}
}
