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

// s5TEnv points HOME, XDG dirs and CLAUDE_CONFIG_DIR at a fresh temp tree.
func s5TEnv(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
}

func s5TWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// s5TJWT builds an unsigned JWT with the given payload.
func s5TJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(b) + ".sig"
}

// s5TCodexLogin signs Codex in with a ChatGPT account.
func s5TCodexLogin(t *testing.T) {
	t.Helper()
	idTok := s5TJWT(map[string]any{
		"email":                       "dev@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
	})
	body := `{"auth_mode":"chatgpt","tokens":{"id_token":"` + idTok +
		`","access_token":"at-secret","refresh_token":"rt-secret","account_id":"acct-1"}}`
	s5TWrite(t, filepath.Join(os.Getenv("HOME"), ".codex", "auth.json"), body)
}

// s5TCopilotLogin signs Copilot in with a GitHub account.
func s5TCopilotLogin(t *testing.T) {
	t.Helper()
	s5TWrite(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_secret"}}`)
}

// s5TClaudeLogin makes Claude Code look signed in, with a recognisable secret.
func s5TClaudeLogin(t *testing.T) {
	t.Helper()
	s5TWrite(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"sk-ant-secret-token"}}`)
}

func s5TFindLine(lines []string, sub string) string {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestS5ListShowsSignedInAccountWithoutCustom(t *testing.T) {
	s5TEnv(t)
	s5TCodexLogin(t)
	s5TCopilotLogin(t)
	lines := providerLines(provider.All(), nil, "")
	codex := s5TFindLine(lines, "signed in as dev@example.com")
	if codex == "" || !strings.Contains(codex, "codex") {
		t.Fatalf("codex row missing 'signed in as': %q", lines)
	}
	if strings.Contains(codex, "custom") {
		t.Errorf("account row must not be tagged custom: %q", codex)
	}
	copilot := s5TFindLine(lines, "signed in as octo")
	if copilot == "" || !strings.Contains(copilot, "copilot") || strings.Contains(copilot, "custom") {
		t.Errorf("copilot row wrong: %q", copilot)
	}
}

func TestS5ListEndsWithClaudeNoticeAndNoSecrets(t *testing.T) {
	s5TEnv(t)
	s5TCodexLogin(t)
	s5TClaudeLogin(t)
	note, ok := provider.ClaudeNotice()
	if !ok {
		t.Fatal("claude login should produce a notice")
	}
	lines := providerLines(provider.All(), nil, note)
	last := lines[len(lines)-1]
	if !strings.Contains(last, "已登录但不作为 Provider 提供") {
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

func TestS5ListWithoutClaudeHasNoNotice(t *testing.T) {
	s5TEnv(t)
	s5TCodexLogin(t)
	note, ok := provider.ClaudeNotice()
	if ok || note != "" {
		t.Fatalf("no claude login, but got notice %q", note)
	}
	lines := providerLines(provider.All(), nil, note)
	for _, l := range lines {
		if strings.Contains(l, "Provider 提供") {
			t.Errorf("unexpected claude note: %q", l)
		}
	}
}

func TestS5DetailShowsAccountLine(t *testing.T) {
	s5TEnv(t)
	s5TCodexLogin(t)
	s5TCopilotLogin(t)

	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	lines := providerDetail(*p, nil)
	acct := s5TFindLine(lines, "account")
	for _, want := range []string{"dev@example.com", "plus", "from Codex"} {
		if !strings.Contains(acct, want) {
			t.Errorf("account line %q lacks %q", acct, want)
		}
	}
	if s5TFindLine(lines, "custom") != "" {
		t.Errorf("account detail must not say custom: %v", lines)
	}

	cp, err := provider.Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	cl := providerDetail(*cp, nil)
	acct = s5TFindLine(cl, "account")
	if !strings.Contains(acct, "octo") || !strings.Contains(acct, "from Copilot") {
		t.Errorf("copilot account line wrong: %q", acct)
	}
}

func TestS5ClaudeNeverInCatalogOrProviders(t *testing.T) {
	s5TEnv(t)
	s5TCodexLogin(t)
	s5TCopilotLogin(t)
	s5TClaudeLogin(t)
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
