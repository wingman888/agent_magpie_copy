package main

import (
	"bytes"
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

// s6Env isolates every config and cache dir under home.
func s6Env(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

func s6JWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// s6SignInCodex writes a ChatGPT-mode Codex sign-in for amy@example.com on the pro plan.
func s6SignInCodex(t *testing.T, home string) {
	t.Helper()
	doc := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": s6JWT(map[string]any{
			"email":                       "amy@example.com",
			"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
		}),
		"access_token":  s6JWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
		"refresh_token": "ref-s6",
		"account_id":    "acct-s6",
	}}
	raw, _ := json.Marshal(doc)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// s6CaptureStdout returns what fn prints to standard output.
func s6CaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		done <- buf.String()
	}()
	func() {
		defer func() {
			os.Stdout = old
			w.Close()
		}()
		fn()
	}()
	return <-done
}

// A8 (CLI): the provider list ends with the Claude Code exclusion line, and the
// Codex row is a signed-in account, not a custom provider.
func TestS6A8ProviderListEndsWithClaudeExclusion(t *testing.T) {
	home := s6Env(t)
	cred := filepath.Join(home, "claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(cred), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cred, []byte(`{"claudeAiOauth":{"accessToken":"SECRET-s6"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s6SignInCodex(t, home)

	var err error
	out := s6CaptureStdout(t, func() { err = providers() })
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	if strings.Contains(out, "SECRET-s6") {
		t.Errorf("list leaks credential contents")
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "Claude Code is signed in but not offered: ") {
		t.Errorf("last line = %q; want the Claude Code exclusion", last)
	}
	if !strings.Contains(out, "signed in as amy@example.com") {
		t.Errorf("codex row should say signed in as the user:\n%s", out)
	}
	if strings.Contains(out, "custom") {
		t.Errorf("account rows must not be marked custom:\n%s", out)
	}
}

// A8 (CLI): with no Claude Code sign-in, there is no exclusion line.
func TestS6A8NoClaudeLineWithoutSignIn(t *testing.T) {
	home := s6Env(t)
	s6SignInCodex(t, home)
	var err error
	out := s6CaptureStdout(t, func() { err = providers() })
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	if strings.Contains(out, "is signed in but not offered") {
		t.Errorf("unexpected exclusion line:\n%s", out)
	}
}

// A9 (CLI): the detail view has an account line with user, plan and agent.
func TestS6A9ProviderDetailAccountLine(t *testing.T) {
	home := s6Env(t)
	s6SignInCodex(t, home)
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	out := s6CaptureStdout(t, func() { err = showProvider(*p) })
	if err != nil {
		t.Fatalf("showProvider: %v", err)
	}
	if !strings.Contains(out, "account") {
		t.Fatalf("detail lacks an account line:\n%s", out)
	}
	for _, want := range []string{"amy@example.com", "pro", "codex"} {
		if !strings.Contains(out, want) {
			t.Errorf("account line lacks %q:\n%s", want, out)
		}
	}
}
