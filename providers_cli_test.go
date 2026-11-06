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

// cliSandbox points HOME, dial's config and Claude Code's config at a fresh tree.
func cliSandbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	return home
}

// cliCodexSignIn writes a ChatGPT-mode Codex login with a plan under home.
func cliCodexSignIn(t *testing.T, home string) {
	t.Helper()
	claims, err := json.Marshal(map[string]any{
		"email":                       "dev@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
	})
	if err != nil {
		t.Fatal(err)
	}
	idToken := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	body, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens":    map[string]any{"id_token": idToken, "access_token": "tok", "account_id": "acct"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// cliClaudeSignIn writes a Claude Code credentials file, which dial only detects.
func cliClaudeSignIn(t *testing.T) {
	t.Helper()
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// cliStdout runs a command and returns what it printed.
func cliStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = old
	w.Close()
	b, _ := io.ReadAll(r)
	r.Close()
	return string(b), runErr
}

func TestProvidersListShowsAccountAndExclusion(t *testing.T) {
	home := cliSandbox(t)
	cliCodexSignIn(t, home)
	cliClaudeSignIn(t)

	out, err := cliStdout(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "signed in as dev@example.com") {
		t.Errorf("list lacks the account line:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "signed in as") && strings.Contains(l, "custom") {
			t.Errorf("account row is marked custom: %q", l)
		}
	}
	if !strings.Contains(out, "\n  Claude Code is signed in but not offered: ") {
		t.Errorf("list lacks the Claude Code exclusion line:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "signed in as") && strings.HasPrefix(strings.TrimSpace(l), "claude") {
			t.Errorf("Claude Code offered as a provider: %q", l)
		}
	}
}

func TestProvidersListWithoutClaudeHasNoExclusionLine(t *testing.T) {
	home := cliSandbox(t)
	cliCodexSignIn(t, home)

	out, err := cliStdout(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "is signed in but not offered") {
		t.Errorf("exclusion line without a Claude Code sign-in:\n%s", out)
	}
}

func TestProviderDetailShowsAccount(t *testing.T) {
	home := cliSandbox(t)
	cliCodexSignIn(t, home)

	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	out, err := cliStdout(t, func() error { return showProvider(*p) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"account", "dev@example.com", "pro", "from codex"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "custom") {
		t.Errorf("account detail is marked custom:\n%s", out)
	}
}
