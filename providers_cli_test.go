package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// cliSandbox points HOME, the XDG dirs and the Claude config dir at fresh temp
// dirs, so no test sees the machine's own sign-ins or dial config.
func cliSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
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

// signInCodex writes a ChatGPT sign-in the way `codex login` does. The access
// token is a marker the tests look for, to prove it never reaches the output
// or dial's own files.
func signInCodex(t *testing.T, home, email, plan string) {
	t.Helper()
	claims := map[string]any{
		"email":                       email,
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": plan},
	}
	b, _ := json.Marshal(claims)
	jwt := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
	auth := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": jwt, "access_token": "tok-secret", "account_id": "acct-1",
	}}
	ab, _ := json.Marshal(auth)
	writeCLIFile(t, filepath.Join(home, ".codex", "auth.json"), string(ab))
}

// signInCopilot writes a GitHub sign-in the way the Copilot editor plugin does.
func signInCopilot(t *testing.T, home, user string) {
	t.Helper()
	writeCLIFile(t, filepath.Join(home, ".config", "github-copilot", "hosts.json"),
		`{"github.com:`+user+`":{"user":"`+user+`","oauth_token":"gho_secret"}}`)
}

// capture runs fn with stdout redirected and returns what it printed.
func capture(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	ferr := fn()
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	r.Close()
	if ferr != nil {
		t.Fatal(ferr)
	}
	return string(out)
}

func lines(s string) []string {
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func TestProvidersListsAccountAsSignedInWithoutCustom(t *testing.T) {
	home := cliSandbox(t)
	signInCodex(t, home, "dev@example.com", "pro")
	out := capture(t, providers)

	var row string
	for _, l := range lines(out) {
		if strings.Contains(l, "signed in as dev@example.com") {
			row = l
		}
	}
	if row == "" {
		t.Fatalf("no signed-in row for codex in:\n%s", out)
	}
	if strings.Contains(row, "custom") {
		t.Errorf("account row carries a custom tag: %q", row)
	}
	if !strings.Contains(row, "Codex") {
		t.Errorf("account row does not name the provider: %q", row)
	}
	if strings.Contains(out, "tok-secret") {
		t.Errorf("token leaked into the provider list:\n%s", out)
	}
}

func TestProvidersNotesClaudeLoginAtEnd(t *testing.T) {
	home := cliSandbox(t)
	signInCodex(t, home, "dev@example.com", "pro")
	writeCLIFile(t, filepath.Join(home, ".claude", ".credentials.json"), "{}")
	out := capture(t, providers)

	ls := lines(out)
	last := ls[len(ls)-1]
	const prefix = "Claude Code is signed in but not offered: "
	if !strings.HasPrefix(last, prefix) {
		t.Fatalf("last line = %q, want prefix %q\nfull output:\n%s", last, prefix, out)
	}
	if !strings.Contains(last, "Claude Code itself") || !strings.Contains(last, "Anthropic API key") {
		t.Errorf("reason does not explain the terms and the alternative: %q", last)
	}
	for _, l := range ls {
		if strings.Contains(l, "custom") {
			t.Errorf("no provider should be custom here: %q", l)
		}
	}
}

func TestProvidersNotesClaudeLoginWithNoProviders(t *testing.T) {
	home := cliSandbox(t)
	writeCLIFile(t, filepath.Join(home, ".claude", ".credentials.json"), "{}")
	out := capture(t, providers)
	if !strings.Contains(out, "Claude Code is signed in but not offered: ") {
		t.Errorf("exclusion missing when there are no providers:\n%s", out)
	}
}

func TestProvidersHasNoExclusionLineWithoutClaudeLogin(t *testing.T) {
	home := cliSandbox(t)
	signInCodex(t, home, "dev@example.com", "pro")
	out := capture(t, providers)
	if strings.Contains(out, "is signed in but not offered") {
		t.Errorf("unexpected exclusion line:\n%s", out)
	}
}

func TestShowAccountProviderHasAccountLineAndNoKey(t *testing.T) {
	home := cliSandbox(t)
	signInCodex(t, home, "dev@example.com", "pro")
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	out := capture(t, func() error { return showProvider(*p) })

	if !strings.Contains(out, "account: dev@example.com (plan: pro) via codex") {
		t.Errorf("missing account line:\n%s", out)
	}
	if strings.Contains(out, "custom") {
		t.Errorf("account details carry a custom tag:\n%s", out)
	}
	for _, l := range lines(out) {
		if strings.HasPrefix(strings.TrimSpace(l), "key ") {
			t.Errorf("account details show a key row: %q", l)
		}
	}
	if strings.Contains(out, "tok-secret") {
		t.Errorf("token leaked into the details:\n%s", out)
	}
}

func TestSavedAccountChoiceStoresOnlyModels(t *testing.T) {
	home := cliSandbox(t)
	signInCodex(t, home, "dev@example.com", "pro")
	capture(t, func() error {
		return providerCmd([]string{"provider", "models", "codex", "gpt-5.5"})
	})
	b, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "tok-secret") || strings.Contains(s, "account") || strings.Contains(s, "acct-1") {
		t.Errorf("providers.json holds more than the model choice:\n%s", s)
	}
	if !strings.Contains(s, "gpt-5.5") {
		t.Errorf("model choice not stored:\n%s", s)
	}
}

func TestKeyCommandRefusesAccountProvider(t *testing.T) {
	home := cliSandbox(t)
	signInCodex(t, home, "dev@example.com", "pro")
	err := providerCmd([]string{"provider", "key", "codex", "sk-should-not-store"})
	if err == nil {
		t.Fatal("setting a key on an account provider should fail")
	}
	if _, statErr := os.Stat(provider.Path()); statErr == nil {
		b, _ := os.ReadFile(provider.Path())
		if strings.Contains(string(b), "sk-should-not-store") {
			t.Errorf("key was written to providers.json")
		}
	}
}
