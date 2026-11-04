package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

var colourCodes = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// captureOut runs fn and returns what it printed, without colour codes.
func captureOut(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	err = fn()
	os.Stdout = old
	w.Close()
	out := <-done
	if err != nil {
		t.Fatal(err)
	}
	return colourCodes.ReplaceAllString(out, "")
}

func cliJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// cliLogins isolates HOME and XDG dirs, signs in to Codex (ChatGPT mode) and
// Copilot, and optionally to Claude Code.
func cliLogins(t *testing.T, claude bool) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))

	id := cliJWT(map[string]any{
		"email":                       "me@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
	})
	auth, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      id,
			"access_token":  cliJWT(map[string]any{"exp": 4102444800}),
			"refresh_token": "rt",
			"account_id":    "acct-1",
		},
	})
	writeCLIFile(t, filepath.Join(home, ".codex", "auth.json"), string(auth))
	writeCLIFile(t, filepath.Join(home, ".config", "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_test"}}`)
	if claude {
		writeCLIFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"secret"}}`)
	}
	return home
}

func writeCLIFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func lineWith(out, sub string) (string, bool) {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, sub) {
			return l, true
		}
	}
	return "", false
}

// The list shows account rows as signed in, not as custom providers.
func TestProviderListShowsAccountsWithoutCustom(t *testing.T) {
	cliLogins(t, false)
	out := captureOut(t, providers)

	codexRow, ok := lineWith(out, "signed in as me@example.com")
	if !ok {
		t.Fatalf("no codex row in:\n%s", out)
	}
	if strings.Contains(codexRow, "custom") {
		t.Errorf("codex row is marked custom: %q", codexRow)
	}
	if _, ok := lineWith(out, "signed in as octo"); !ok {
		t.Errorf("no copilot row in:\n%s", out)
	}
	if strings.Contains(out, "not offered") {
		t.Errorf("no Claude Code login, yet a not-offered line:\n%s", out)
	}
}

// A Claude Code login is named at the end of the list, with its reason, and
// is never offered as a provider.
func TestProviderListNamesExcludedClaudeLogin(t *testing.T) {
	cliLogins(t, true)
	out := captureOut(t, providers)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(strings.TrimSpace(last), "Claude Code is signed in but not offered: ") {
		t.Fatalf("last line = %q, want the Claude Code not-offered line", last)
	}
	if !strings.Contains(last, "Anthropic's terms") || !strings.Contains(last, "Anthropic API key") {
		t.Errorf("reason does not explain the terms and the alternative: %q", last)
	}
	if _, err := provider.Find("claude"); err == nil {
		t.Error("a claude provider was offered")
	}
	if strings.Contains(out, "secret") {
		t.Error("Claude Code's credential leaked into the output")
	}
}

// The detail page gives the account, its plan, and the agent it comes from.
func TestProviderDetailShowsAccount(t *testing.T) {
	cliLogins(t, false)
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	out := captureOut(t, func() error { return showProvider(*p) })
	line, ok := lineWith(out, "account")
	if !ok {
		t.Fatalf("no account line in:\n%s", out)
	}
	if !strings.Contains(line, "me@example.com · pro · from Codex") {
		t.Errorf("account line = %q, want user, plan and agent", line)
	}
	if strings.Contains(out, "custom") {
		t.Errorf("account detail is marked custom:\n%s", out)
	}
}

// Saving an account keeps only the picked models; deleting clears them and
// the provider stays, since logging out is the agent's business.
func TestAccountSaveStoresOnlyPicks(t *testing.T) {
	cliLogins(t, false)
	if err := provider.Save(provider.Provider{ID: "codex", Name: "Mine", Chat: "https://evil.example/v1",
		Key: "sk-secret", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"evil.example", "sk-secret", "Mine"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("providers.json contains %q:\n%s", bad, b)
		}
	}
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.Account == nil || p.Chat != "" || p.Key != "" {
		t.Errorf("reloaded codex = %+v, want an account with no address or key", p)
	}
	if len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Errorf("reloaded picks = %v, want [gpt-5.5]", p.Models)
	}

	if err := provider.Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p, err = provider.Find("codex")
	if err != nil {
		t.Fatalf("codex vanished after delete: %v", err)
	}
	if len(p.Models) != 0 {
		t.Errorf("picks after delete = %v, want none", p.Models)
	}
}
