package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// l4S5TEnv points HOME, XDG dirs and CLAUDE_CONFIG_DIR at a fresh temp tree.
func l4S5TEnv(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
}

func l4S5TWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// l4S5TJWT builds an unsigned JWT with the given payload.
func l4S5TJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(b) + ".sig"
}

// l4S5TCodexLogin signs Codex in with a ChatGPT account.
func l4S5TCodexLogin(t *testing.T) {
	t.Helper()
	idTok := l4S5TJWT(map[string]any{
		"email":                       "dev@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
	})
	body := `{"auth_mode":"chatgpt","tokens":{"id_token":"` + idTok +
		`","access_token":"at-secret","refresh_token":"rt-secret","account_id":"acct-1"}}`
	l4S5TWrite(t, filepath.Join(os.Getenv("HOME"), ".codex", "auth.json"), body)
}

// l4S5TCopilotLogin signs Copilot in with a GitHub account.
func l4S5TCopilotLogin(t *testing.T) {
	t.Helper()
	l4S5TWrite(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_secret"}}`)
}

// l4S5TClaudeLogin makes Claude Code look signed in, with a recognisable secret.
func l4S5TClaudeLogin(t *testing.T) {
	t.Helper()
	l4S5TWrite(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"sk-ant-secret-token"}}`)
}

func l4S5TFindLine(lines []string, sub string) string {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestL4S5ListShowsSignedInAccountWithoutCustom(t *testing.T) {
	l4S5TEnv(t)
	l4S5TCodexLogin(t)
	l4S5TCopilotLogin(t)
	lines := l4ProviderLines(provider.All(), nil, "")
	codex := l4S5TFindLine(lines, "signed in as dev@example.com")
	if codex == "" || !strings.Contains(codex, "codex") {
		t.Fatalf("codex row missing 'signed in as': %q", lines)
	}
	if strings.Contains(codex, "custom") {
		t.Errorf("account row must not be tagged custom: %q", codex)
	}
	copilot := l4S5TFindLine(lines, "signed in as octo")
	if copilot == "" || !strings.Contains(copilot, "copilot") || strings.Contains(copilot, "custom") {
		t.Errorf("copilot row wrong: %q", copilot)
	}
}

func TestL4S5ListEndsWithClaudeNoticeAndNoSecrets(t *testing.T) {
	l4S5TEnv(t)
	l4S5TCodexLogin(t)
	l4S5TClaudeLogin(t)
	note, ok := provider.ClaudeNotice()
	if !ok {
		t.Fatal("claude login should produce a notice")
	}
	lines := l4ProviderLines(provider.All(), nil, note)
	last := lines[len(lines)-1]
	if !strings.Contains(last, "但不作为 Provider 提供") {
		t.Errorf("last line should explain claude, got %q", last)
	}
	for _, l := range lines[:len(lines)-1] {
		if strings.Contains(l, "claude") || strings.Contains(l, "Claude") {
			t.Errorf("claude must not appear as a provider row: %q", l)
		}
	}
	for _, l := range lines {
		if strings.Contains(l, "sk-ant-secret") || strings.Contains(l, "at-secret") || strings.Contains(l, "rt-secret") {
			t.Errorf("token leaked into listing: %q", l)
		}
	}
}

func TestL4S5ListWithoutClaudeHasNoNotice(t *testing.T) {
	l4S5TEnv(t)
	l4S5TCodexLogin(t)
	note, ok := provider.ClaudeNotice()
	if ok || note != "" {
		t.Fatalf("no claude login, but got notice %q", note)
	}
	lines := l4ProviderLines(provider.All(), nil, note)
	for _, l := range lines {
		if strings.Contains(l, "Provider 提供") {
			t.Errorf("unexpected claude note: %q", l)
		}
	}
}

func TestL4S5DetailShowsAccountLine(t *testing.T) {
	l4S5TEnv(t)
	l4S5TCodexLogin(t)
	l4S5TCopilotLogin(t)

	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	lines := l4ProviderDetail(*p, nil)
	acct := l4S5TFindLine(lines, "account")
	for _, want := range []string{"dev@example.com", "plus", "from Codex"} {
		if !strings.Contains(acct, want) {
			t.Errorf("account line %q lacks %q", acct, want)
		}
	}
	if l4S5TFindLine(lines, "custom") != "" {
		t.Errorf("account detail must not say custom: %v", lines)
	}

	cp, err := provider.Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	cl := l4ProviderDetail(*cp, nil)
	acct = l4S5TFindLine(cl, "account")
	if !strings.Contains(acct, "octo") || !strings.Contains(acct, "from Copilot") {
		t.Errorf("copilot account line wrong: %q", acct)
	}
}

func TestL4S5ClaudeNeverInCatalogOrProviders(t *testing.T) {
	l4S5TEnv(t)
	l4S5TCodexLogin(t)
	l4S5TCopilotLogin(t)
	l4S5TClaudeLogin(t)
	for _, p := range provider.All() {
		if p.ID == "claude" {
			t.Fatal("claude listed in provider.All()")
		}
	}
	var codexSeen bool
	for _, e := range provider.Catalog() {
		if strings.HasPrefix(e.ID, "claude") {
			t.Errorf("claude model in catalog: %s", e.ID)
		}
		if strings.HasPrefix(e.ID, "codex/") {
			codexSeen = true
		}
	}
	if !codexSeen {
		t.Error("codex/ models missing from the catalog")
	}
}
