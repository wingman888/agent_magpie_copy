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

// signIn points every credential location at a temporary HOME and signs
// Codex (ChatGPT) and Copilot in there. With claude set, Claude Code's
// credential file exists too (its contents are never read by dial).
func signIn(t *testing.T, claude bool) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))

	claims := map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}}
	b, _ := json.Marshal(claims)
	idTok := "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	writeTestFile(t, filepath.Join(root, ".codex", "auth.json"),
		`{"auth_mode":"chatgpt","tokens":{"id_token":"`+idTok+`","access_token":"at","account_id":"acc"}}`)
	writeTestFile(t, filepath.Join(root, "config", "github-copilot", "apps.json"),
		`{"github.com:Iv1":{"user":"octo","oauth_token":"gho_test"}}`)
	if claude {
		writeTestFile(t, filepath.Join(root, "claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"secret"}}`)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// capture runs fn and returns what it printed to stdout.
func capture(t *testing.T, fn func() error) string {
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
	runErr := fn()
	os.Stdout = old
	w.Close()
	out := <-done
	if runErr != nil {
		t.Fatal(runErr)
	}
	return out
}

func lineWith(out, sub string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestProvidersListShowsSignedInAccounts(t *testing.T) {
	signIn(t, false)
	out := capture(t, providers)

	for _, want := range []string{"signed in as me@example.com", "signed in as octo"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "custom") {
		t.Fatalf("account providers must not be marked custom:\n%s", out)
	}
	for _, p := range provider.All() {
		if p.ID == "claude" {
			t.Fatal("claude must never be offered as a provider")
		}
	}
}

// The exclusion line is checked with an explicit list: provider.Excluded()
// caches its answer for a minute, which these tests cannot reset.
func TestExclusionLineText(t *testing.T) {
	out := capture(t, func() error {
		printExcluded([]provider.Exclusion{{Agent: "claude", Why: "terms text"}})
		return nil
	})
	want := "  Claude Code is signed in but not offered: terms text\n"
	if out != want {
		t.Fatalf("exclusion line = %q, want exactly %q", out, want)
	}
}

func TestExclusionLineNoneWhenEmpty(t *testing.T) {
	out := capture(t, func() error {
		printExcluded(nil)
		return nil
	})
	if out != "" {
		t.Fatalf("no exclusions should print nothing, got %q", out)
	}
}

func TestClaudeExclusionDoesNotLeakCredentials(t *testing.T) {
	signIn(t, true)
	out := capture(t, providers)
	if strings.Contains(out, "secret") {
		t.Fatal("claude credentials leaked into the list")
	}
	for _, p := range provider.All() {
		if p.ID == "claude" {
			t.Fatal("claude must never be offered as a provider")
		}
	}
}

func TestShowProviderAccountLine(t *testing.T) {
	signIn(t, false)
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	out := capture(t, func() error { return showProvider(*p) })

	if got := lineWith(out, "account"); !strings.Contains(got, "me@example.com (plan: pro) via codex") {
		t.Fatalf("account line = %q", got)
	}
	if strings.Contains(out, "custom") {
		t.Fatalf("account provider detail must not say custom:\n%s", out)
	}
}

func TestModelsNoteShowsLoginUser(t *testing.T) {
	signIn(t, false)
	out := capture(t, models)

	if l := lineWith(out, "codex/gpt-5.5"); !strings.Contains(l, "me@example.com") {
		t.Fatalf("codex model line should show the login user: %q", l)
	}
	if l := lineWith(out, "copilot/claude-sonnet-4.5"); !strings.Contains(l, "octo") {
		t.Fatalf("copilot model line should show the GitHub user: %q", l)
	}
	if strings.Contains(out, "copilot/auto") {
		t.Fatalf("copilot/auto must not be in the catalog:\n%s", out)
	}
}

func TestCatalogHasAccountModelsAndResolves(t *testing.T) {
	signIn(t, false)
	ids := provider.IDs()
	if !contains(ids, "codex/gpt-5.5") || !contains(ids, "copilot/claude-sonnet-4.5") {
		t.Fatalf("catalog ids = %v", ids)
	}
	if contains(ids, "copilot/auto") {
		t.Fatal("copilot/auto leaked into the catalog")
	}
	p, model, ok := provider.Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || model != "gpt-5.5" {
		t.Fatalf("Resolve(codex/gpt-5.5) = %q %q %v", p.ID, model, ok)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
