package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/catalog"
	"github.com/yetone/dial/internal/provider"
)

// cliSandbox points HOME, XDG and the agents' config dirs at a temp dir.
func cliSandbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	return root
}

func cliWrite(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// cliJWT builds an unsigned token whose payload is the given claims.
func cliJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func signInCodex(t *testing.T, home, email string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      cliJWT(map[string]any{"email": email}),
			"access_token":  cliJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
			"refresh_token": "rt-secret",
			"account_id":    "acct-1",
		},
	})
	cliWrite(t, filepath.Join(home, ".codex", "auth.json"), string(b))
}

func signInCopilot(t *testing.T) {
	t.Helper()
	cliWrite(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc": {"user": "octo", "oauth_token": "gho_test"}}`)
}

func listing(t *testing.T) []string {
	t.Helper()
	var buf bytes.Buffer
	if err := writeProviders(&buf); err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
}

func TestProvidersListShowsSignedInAccounts(t *testing.T) {
	root := cliSandbox(t)
	signInCodex(t, filepath.Join(root, "home"), "dev@example.com")
	signInCopilot(t)

	out := strings.Join(listing(t), "\n")
	if !strings.Contains(out, "signed in as dev@example.com") {
		t.Errorf("codex row lacks its email:\n%s", out)
	}
	if !strings.Contains(out, "signed in as octo") {
		t.Errorf("copilot row lacks its login:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if (strings.Contains(l, "codex") || strings.Contains(l, "copilot")) && strings.Contains(l, "custom") {
			t.Errorf("sign-in row marked custom: %q", l)
		}
	}
}

func TestProvidersListCustomStillMarkedCustom(t *testing.T) {
	root := cliSandbox(t)
	signInCodex(t, filepath.Join(root, "home"), "dev@example.com")
	if err := provider.Save(provider.Provider{Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-relay-1234567890"}); err != nil {
		t.Fatal(err)
	}
	var relay string
	for _, l := range listing(t) {
		if strings.Contains(l, "relay") {
			relay = l
		}
	}
	if !strings.Contains(relay, "custom") {
		t.Errorf("custom provider not marked: %q", relay)
	}
}

func TestProvidersListEndsWithClaudeNotice(t *testing.T) {
	root := cliSandbox(t)
	const secret = "SECRET-CLAUDE-TOKEN"
	signInCodex(t, filepath.Join(root, "home"), "dev@example.com")
	cliWrite(t, filepath.Join(root, "claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"`+secret+`"}}`)

	lines := listing(t)
	last := lines[len(lines)-1]
	if !strings.Contains(last, "Claude Code") || !strings.Contains(last, "已登录但不提供") || !strings.Contains(last, "凭据") {
		t.Errorf("last line is not the Claude notice: %q", last)
	}
	out := strings.Join(lines, "\n")
	if strings.Contains(out, secret) {
		t.Fatal("providers output leaked Claude's token")
	}
	for _, l := range lines {
		if strings.Contains(l, "claude") && !strings.Contains(l, "Claude Code") {
			t.Errorf("claude listed as a provider row: %q", l)
		}
	}
}

func TestProvidersListNoticeWithoutProviders(t *testing.T) {
	root := cliSandbox(t)
	cliWrite(t, filepath.Join(root, "claude", ".credentials.json"), `{}`)
	out := strings.Join(listing(t), "\n")
	if !strings.Contains(out, "已登录但不提供") {
		t.Errorf("no notice when only Claude is signed in:\n%s", out)
	}
}

func TestProvidersListNoClaudeNoticeWhenSignedOut(t *testing.T) {
	cliSandbox(t)
	out := strings.Join(listing(t), "\n")
	if strings.Contains(out, "已登录但不提供") {
		t.Errorf("notice shown while Claude is signed out:\n%s", out)
	}
}
