package provider

// Independent black-box checks of the agent-account providers through the
// exported API: All, Save, Delete, Notices, Path.

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// zzbbSandbox isolates HOME, dial's config and every agent's sign-in dir.
func zzbbSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

func zzbbJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func zzbbWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// zzbbSignInCodex writes a ChatGPT login as codex login does.
func zzbbSignInCodex(t *testing.T, home string) {
	t.Helper()
	doc := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     zzbbJWT(map[string]any{"email": "dev@example.com"}),
		"access_token": zzbbJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
		"refresh_token": "rt-bb", "account_id": "acct-bb"}}
	b, _ := json.Marshal(doc)
	zzbbWrite(t, filepath.Join(home, ".codex", "auth.json"), string(b))
}

func zzbbFind(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

// Signed-in Codex appears in All() with its email as the user.
func TestZzBBCodexInAll(t *testing.T) {
	home := zzbbSandbox(t)
	zzbbSignInCodex(t, home)
	p := zzbbFind(All(), "codex")
	if p == nil {
		t.Fatalf("codex not in All(): %+v", All())
	}
	if p.User != "dev@example.com" {
		t.Errorf("User = %q, want the email", p.User)
	}
	if p.Account != "codex" {
		t.Errorf("Account = %q", p.Account)
	}
	if strings.Contains(p.Key, "eyJ") {
		t.Errorf("sign-in token exposed as Key")
	}
}

// API-key login is not an account.
func TestZzBBCodexAPIKeyModeIsNotAccount(t *testing.T) {
	home := zzbbSandbox(t)
	zzbbWrite(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-bb"}`)
	if zzbbFind(All(), "codex") != nil {
		t.Errorf("API-key Codex login listed as a provider")
	}
}

// Logging out removes codex from All(); a leftover pick does not revive it.
func TestZzBBCodexLogoutRemovesProvider(t *testing.T) {
	home := zzbbSandbox(t)
	zzbbSignInCodex(t, home)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if zzbbFind(All(), "codex") == nil {
		t.Fatalf("codex missing while signed in")
	}
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if zzbbFind(All(), "codex") != nil {
		t.Errorf("codex still listed after logout, with a leftover pick")
	}
}

// Saving a signed-in account writes only its model picks; Delete clears the
// picks without logging out or touching the sign-in.
func TestZzBBAccountSaveAndDeleteOnlyPicks(t *testing.T) {
	home := zzbbSandbox(t)
	zzbbSignInCodex(t, home)
	authPath := filepath.Join(home, ".codex", "auth.json")
	authBefore, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "codex", Name: "BB-NAME", Key: "BB-KEY-VALUE", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"BB-KEY-VALUE", "BB-NAME", "dev@example.com", "acct-bb", "rt-bb", "eyJ"} {
		if strings.Contains(string(cfg), bad) {
			t.Errorf("config file holds %q: %s", bad, cfg)
		}
	}
	if !strings.Contains(string(cfg), "gpt-5.5") {
		t.Errorf("config file lost the model pick: %s", cfg)
	}
	if err := Delete("codex"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if zzbbFind(All(), "codex") == nil {
		t.Errorf("Delete logged codex out of All()")
	}
	authAfter, err := os.ReadFile(authPath)
	if err != nil || string(authAfter) != string(authBefore) {
		t.Errorf("Delete changed the Codex sign-in file")
	}
}

// Signed-in Copilot appears with the GitHub login; its token is not kept as a Key.
func TestZzBBCopilotInAll(t *testing.T) {
	home := zzbbSandbox(t)
	apps := `{"github.com:octo":{"user":"octo","oauth_token":"gho_BBSECRET"}}`
	zzbbWrite(t, filepath.Join(home, "config", "github-copilot", "apps.json"), apps)
	p := zzbbFind(All(), "copilot")
	if p == nil {
		t.Fatalf("copilot not in All(): %+v", All())
	}
	if p.User != "octo" {
		t.Errorf("User = %q, want octo", p.User)
	}
	if strings.Contains(p.Key, "gho_") {
		t.Errorf("GitHub token exposed as Key")
	}
}

// Claude Code signed in: not a provider, a notice says so, and its token never shows.
func TestZzBBClaudeNotProvidedAndNotice(t *testing.T) {
	home := zzbbSandbox(t)
	zzbbWrite(t, filepath.Join(home, "claude", ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"sk-ant-oat-BBSECRET"}}`)
	for _, p := range All() {
		if p.ID == "claude" {
			t.Errorf("claude appears in All()")
		}
		for _, v := range []string{p.Key, p.Name, p.Website, p.Source, p.User} {
			if strings.Contains(v, "BBSECRET") {
				t.Errorf("Claude token leaked into provider %s: %q", p.ID, v)
			}
		}
	}
	ns := Notices()
	var txt string
	for _, n := range ns {
		if strings.Contains(n.Text, "已登录但不提供") {
			txt = n.Text
		}
	}
	if txt == "" {
		t.Fatalf("no 已登录但不提供 notice: %+v", ns)
	}
	if !strings.Contains(txt, "Claude") {
		t.Errorf("notice does not name Claude Code: %s", txt)
	}
	if strings.Contains(txt, "BBSECRET") {
		t.Errorf("notice leaks the token")
	}
}
