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

func testJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return enc(`{"alg":"none"}`) + "." + enc(string(b)) + ".sig"
}

// providersSandbox signs in to Codex (ChatGPT login) and Claude Code, and
// points every config directory at a temp dir.
func providersSandbox(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	auth := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      testJWT(map[string]any{"email": "ana@example.com"}),
			"access_token":  testJWT(map[string]any{"exp": 4102444800, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
			"refresh_token": "rt-1",
			"account_id":    "acct-1",
		},
	}
	b, _ := json.Marshal(auth)
	writeFileForTest(t, filepath.Join(home, ".codex", "auth.json"), string(b))
	writeFileForTest(t, filepath.Join(home, "claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"secret-token"}}`)
}

func writeFileForTest(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// captureOut runs fn and returns what it printed to stdout.
func captureOut(t *testing.T, fn func() error) string {
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

func TestProvidersShowsAccountAndClaudeNotice(t *testing.T) {
	providersSandbox(t)
	out := captureOut(t, providers)

	if !strings.Contains(out, "signed in as ana@example.com") {
		t.Errorf("providers lacks codex account line:\n%s", out)
	}
	if strings.Contains(out, "custom") {
		t.Errorf("account provider marked custom:\n%s", out)
	}
	if !strings.Contains(out, "已登录但不提供") {
		t.Errorf("providers lacks Claude Code notice:\n%s", out)
	}
	if strings.Contains(out, "secret-token") {
		t.Errorf("claude token leaked into output")
	}
}

func TestProviderShowsAccountItem(t *testing.T) {
	providersSandbox(t)
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	out := captureOut(t, func() error { return showProvider(*p) })
	for _, want := range []string{"account", "ana@example.com", "plus plan", "from Codex"} {
		if !strings.Contains(out, want) {
			t.Errorf("show codex lacks %q:\n%s", want, out)
		}
	}
}
