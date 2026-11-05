package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// accHome points every dial and agent location at a fresh temp dir.
func accHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(h, ".claude"))
	return h
}

func accJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func accWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// accCodexLogin writes a ChatGPT login for me@example.com.
func accCodexLogin(t *testing.T, h string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     accJWT(t, map[string]any{"email": "me@example.com"}),
		"access_token": accJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
		"refresh_token": "rt", "account_id": "acct-1"}})
	accWrite(t, filepath.Join(h, ".codex", "auth.json"), string(b))
}

// accStdout runs f and returns what it printed to stdout.
func accStdout(t *testing.T, f func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	ferr := f()
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	if ferr != nil {
		t.Fatal(ferr)
	}
	return string(out)
}

// 6 (CLI). dial providers shows the Codex login by e-mail.
func TestAccProvidersShowsCodexSignedIn(t *testing.T) {
	h := accHome(t)
	accCodexLogin(t, h)
	out := accStdout(t, providers)
	if !strings.Contains(out, "codex") || !strings.Contains(out, "signed in as me@example.com") {
		t.Fatalf("providers output:\n%s", out)
	}
	if strings.Contains(out, "custom") && strings.Contains(out, "codex") {
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "signed in as") && strings.Contains(line, "custom") {
				t.Errorf("codex row marked custom: %s", line)
			}
		}
	}
}

// 10 (CLI). A signed-in Claude Code is noted last, with the reason.
func TestAccProvidersNotesClaudeLast(t *testing.T) {
	h := accHome(t)
	accWrite(t, filepath.Join(h, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"sk-ant-oat-SECRET-TOKEN-987"}}`)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "https://example.invalid/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	out := accStdout(t, providers)
	if strings.Contains(out, "SECRET-TOKEN-987") {
		t.Fatal("providers output leaks the Claude token")
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "已登录但不提供") {
		t.Fatalf("last line should note Claude Code; output:\n%s", out)
	}
}

// 10 (CLI, no other provider). The note still appears when Claude is the only login.
func TestAccProvidersNotesClaudeWhenAlone(t *testing.T) {
	h := accHome(t)
	accWrite(t, filepath.Join(h, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"sk-ant-oat-SECRET-TOKEN-987"}}`)
	out := accStdout(t, providers)
	if !strings.Contains(out, "已登录但不提供") {
		t.Fatalf("no Claude note when no provider exists; output:\n%s", out)
	}
}
