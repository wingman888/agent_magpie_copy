package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Acceptance checks V6 and V10 at the command: `dial providers` lists a
// signed-in Codex as "signed in as <email>" and ends with Claude Code's notice.

func accJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(b) + ".sig"
}

func accWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// accStdout runs fn and returns what it printed to stdout.
func accStdout(t *testing.T, fn func() error) string {
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
	b, _ := io.ReadAll(r)
	if ferr != nil {
		t.Fatal(ferr)
	}
	return string(b)
}

func TestAcceptV6V10ProvidersCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))

	idTok := accJWT(map[string]any{"email": "dev@example.com"})
	accTok := accJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	accWrite(t, filepath.Join(home, ".codex", "auth.json"),
		fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":"rt","account_id":"acct-1"}}`, idTok, accTok))
	accWrite(t, filepath.Join(home, "claude", ".credentials.json"), `{"accessToken":"SECRET-CLAUDE-TOKEN"}`)

	out := accStdout(t, providers)
	if !strings.Contains(out, "signed in as dev@example.com") {
		t.Errorf("codex row lacks 'signed in as': %s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "codex") && strings.Contains(line, "custom") {
			t.Errorf("codex row marked custom: %q", line)
		}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "Claude Code") || !strings.Contains(last, "已登录但不提供") {
		t.Errorf("last line is not the Claude Code notice: %q", last)
	}
	if strings.Contains(out, "SECRET") {
		t.Errorf("providers output shows Claude credentials")
	}
}
