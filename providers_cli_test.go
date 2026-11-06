package main

import (
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// acctHome points every sign-in and dial's own files at a temp directory.
func acctHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

func acctPut(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// acctCodex writes a ChatGPT-mode Codex sign-in for dev@example.com on the pro plan.
func acctCodex(t *testing.T, home string) {
	t.Helper()
	payload := `{"email":"dev@example.com","https://api.openai.com/auth":{"chatgpt_plan_type":"pro","chatgpt_account_id":"acct-1"}}`
	id := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
	acctPut(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"chatgpt","tokens":{"id_token":"`+id+`","access_token":"ACCESS","refresh_token":"R","account_id":"acct-1"}}`)
}

// acctCopilot writes a GitHub Copilot sign-in for octocat.
func acctCopilot(t *testing.T, home string) {
	t.Helper()
	acctPut(t, filepath.Join(home, "config", "github-copilot", "hosts.json"),
		`{"github.com":{"user":"octocat","oauth_token":"gho_x"}}`)
}

// acctClaude writes a Claude Code credentials file; its content is never read by dial.
func acctClaude(t *testing.T, home string) {
	t.Helper()
	acctPut(t, filepath.Join(home, "claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"C"}}`)
}

// acctOutput runs f with stdout captured and returns what it printed.
func acctOutput(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	ferr := f()
	os.Stdout = old
	w.Close()
	return <-done, ferr
}

func TestProvidersListAccountsAndClaudeExclusion(t *testing.T) {
	home := acctHome(t)
	acctCodex(t, home)
	acctCopilot(t, home)
	acctClaude(t, home)
	out, err := acctOutput(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "signed in as dev@example.com") || !strings.Contains(out, "signed in as octocat") {
		t.Fatalf("account rows should say who is signed in:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "signed in as") && strings.Contains(line, "custom") {
			t.Fatalf("account row must not be marked custom: %q", line)
		}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "Claude Code is signed in but not offered: ") || !strings.Contains(last, "Anthropic") {
		t.Fatalf("the Claude exclusion line must close the list:\n%s", out)
	}
}

func TestProvidersNoExclusionLineWithoutClaudeLogin(t *testing.T) {
	home := acctHome(t)
	acctCodex(t, home)
	out, err := acctOutput(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "is signed in but not offered") {
		t.Fatalf("no Claude login, no exclusion line:\n%s", out)
	}
}

func TestProviderDetailShowsAccountLine(t *testing.T) {
	home := acctHome(t)
	acctCodex(t, home)
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	out, err := acctOutput(t, func() error { return showProvider(*p) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "account") || !strings.Contains(out, "dev@example.com (plan pro, from codex)") {
		t.Fatalf("detail should carry an account line:\n%s", out)
	}
}

func TestAccountModelsListNamesTheUser(t *testing.T) {
	home := acctHome(t)
	acctCodex(t, home)
	acctCopilot(t, home)
	out, err := acctOutput(t, models)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Copilot · signed in as octocat", "Codex · signed in as dev@example.com", "copilot/claude-sonnet-4.5"} {
		if !strings.Contains(out, want) {
			t.Fatalf("models list missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "copilot/auto") {
		t.Fatalf("the built-in Copilot list must not offer auto:\n%s", out)
	}
}

func TestAccountSaveKeepsOnlyTheModelChoice(t *testing.T) {
	home := acctHome(t)
	acctCopilot(t, home)
	out, err := acctOutput(t, func() error {
		return providerCmd([]string{"dial", "provider", "key", "copilot", "sk-SECRET"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "sk-SECRET") || !strings.Contains(out, "no key is kept") {
		t.Fatalf("key for an account must be ignored and not echoed:\n%s", out)
	}
	if err := provider.Save(provider.Provider{ID: "copilot", Chat: "https://evil.example/v1", Key: "sk-SECRET",
		Models: []string{"claude-sonnet-4.5"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, bad := range []string{"sk-SECRET", "evil.example", `"chat"`, `"key"`} {
		if strings.Contains(s, bad) {
			t.Fatalf("stored account must not hold %q:\n%s", bad, s)
		}
	}
	if !strings.Contains(s, "claude-sonnet-4.5") {
		t.Fatalf("the model choice must be stored:\n%s", s)
	}
	p, err := provider.Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "" || p.Key != "" || len(p.Models) != 1 || p.Models[0] != "claude-sonnet-4.5" {
		t.Fatalf("reloaded account should expose only the choice: %+v", p)
	}
	if ex := p.Exposed(); len(ex) != 1 || ex[0].ID != "claude-sonnet-4.5" {
		t.Fatalf("exposed models should be the choice only: %+v", ex)
	}
}

func TestAccountRemoveClearsChoiceButKeepsProvider(t *testing.T) {
	home := acctHome(t)
	acctCopilot(t, home)
	if err := provider.Save(provider.Provider{ID: "copilot", Models: []string{"claude-sonnet-4.5"}}); err != nil {
		t.Fatal(err)
	}
	out, err := acctOutput(t, func() error {
		return providerCmd([]string{"dial", "provider", "rm", "copilot"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cleared the model choice") {
		t.Fatalf("remove on an account should say it only cleared the choice:\n%s", out)
	}
	p, err := provider.Find("copilot")
	if err != nil {
		t.Fatalf("account provider must remain after remove: %v", err)
	}
	if len(p.Models) != 0 {
		t.Fatalf("choice should be cleared: %+v", p.Models)
	}
}
