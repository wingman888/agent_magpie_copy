package provider

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// acctEnv points HOME, dial's config and cache, and Claude's config dir at a
// fresh temp directory, so no real sign-in on this machine is seen.
func acctEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

func acctWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// acctJWT builds an unsigned JWT with the given payload claims.
func acctJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// acctWriteCodex writes ~/.codex/auth.json with the given auth_mode.
func acctWriteCodex(t *testing.T, home, mode string) string {
	t.Helper()
	idTok := acctJWT(map[string]any{
		"email":                        "dev@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
	})
	accTok := acctJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	doc := map[string]any{
		"auth_mode": mode,
		"tokens": map[string]any{
			"id_token":      idTok,
			"access_token":  accTok,
			"refresh_token": "rt-secret",
			"account_id":    "acct-1",
		},
	}
	b, _ := json.Marshal(doc)
	path := filepath.Join(home, ".codex", "auth.json")
	acctWriteFile(t, path, string(b))
	return path
}

// acctFind returns the provider with the given id from All(), if listed.
func acctFind(id string) (Provider, bool) {
	for _, p := range All() {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

func TestAcctCodexCredentialsParsed(t *testing.T) {
	home := acctEnv(t)
	acctWriteCodex(t, home, "chatgpt")
	c, ok := readCodex()
	if !ok {
		t.Fatal("chatgpt login not read")
	}
	if c.User != "dev@example.com" || c.Plan != "plus" || c.AccountID != "acct-1" {
		t.Errorf("parsed %+v", c)
	}
	if c.AccessToken == "" || c.RefreshToken != "rt-secret" || c.IDToken == "" {
		t.Errorf("tokens not read: %+v", c)
	}
	if left := time.Until(c.Expires); left <= 0 || left > 2*time.Hour {
		t.Errorf("expiry %v not taken from access token exp", c.Expires)
	}
}

func TestAcctCodexAPIKeyLoginIsNotAccount(t *testing.T) {
	home := acctEnv(t)
	acctWriteCodex(t, home, "apikey")
	if _, ok := readCodex(); ok {
		t.Error("apikey login treated as an account")
	}
	if _, ok := acctFind("codex"); ok {
		t.Error("codex listed as a provider for an apikey login")
	}
}

func TestAcctCodexSignedInIsProvider(t *testing.T) {
	home := acctEnv(t)
	acctWriteCodex(t, home, "chatgpt")
	p, ok := acctFind("codex")
	if !ok {
		t.Fatal("codex not listed while signed in")
	}
	if p.Account == nil || p.Account.User != "dev@example.com" || p.Account.Source != "Codex" {
		t.Errorf("account = %+v", p.Account)
	}
	if p.Key != "" || !p.Ready() {
		t.Errorf("account provider should be ready without a key: key=%q ready=%v", p.Key, p.Ready())
	}
	if !p.Chosen("gpt-5.5") {
		t.Error("built-in codex list should expose gpt-5.5 before any pick")
	}
	found := false
	for _, e := range Catalog() {
		if e.ID == "codex/gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Error("codex/gpt-5.5 missing from the catalog")
	}
}

func TestAcctSaveStoresOnlyModels(t *testing.T) {
	home := acctEnv(t)
	acctWriteCodex(t, home, "chatgpt")
	err := Save(Provider{ID: "codex", Name: "Mine", Key: "sk-secret", Chat: "https://example.com/v1", Models: []string{"gpt-5.5"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"gpt-5.5"`) || !strings.Contains(s, `"codex"`) {
		t.Errorf("picks not stored: %s", s)
	}
	for _, bad := range []string{"sk-secret", "Mine", "example.com", `"key"`, `"name"`, `"chat"`, "rt-secret"} {
		if strings.Contains(s, bad) {
			t.Errorf("config contains %q: %s", bad, s)
		}
	}
}

func TestAcctDeleteClearsPicksKeepsSignIn(t *testing.T) {
	home := acctEnv(t)
	authPath := acctWriteCodex(t, home, "chatgpt")
	before, _ := os.ReadFile(authPath)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	p, ok := acctFind("codex")
	if !ok {
		t.Fatal("still signed in, but codex disappeared after delete")
	}
	if len(p.Models) != 0 {
		t.Errorf("picks not cleared: %v", p.Models)
	}
	after, _ := os.ReadFile(authPath)
	if string(before) != string(after) {
		t.Error("delete changed the agent's credentials file")
	}
}

func TestAcctSignedOutIgnoresStalePicks(t *testing.T) {
	home := acctEnv(t)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := acctFind("codex"); ok {
		t.Fatal("codex listed while signed out")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Errorf("stale pick became a catalog entry: %s", e.ID)
		}
	}
	acctWriteCodex(t, home, "chatgpt")
	p, ok := acctFind("codex")
	if !ok || len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Errorf("picks should return with the sign-in: ok=%v %+v", ok, p)
	}
}

func TestAcctClaudeNeverAProvider(t *testing.T) {
	home := acctEnv(t)
	credPath := filepath.Join(home, "claude", ".credentials.json")
	acctWriteFile(t, credPath, `{"claudeAiOauth":{"accessToken":"claude-token-XYZ"}}`)

	if _, ok := acctFind("claude"); ok {
		t.Error("claude listed as a provider")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "claude/") {
			t.Errorf("claude model in catalog: %s", e.ID)
		}
	}
	notes := Notices()
	if len(notes) != 1 || notes[0].Agent != "Claude Code" || notes[0].Text == "" {
		t.Fatalf("notices = %+v", notes)
	}
	if strings.Contains(notes[0].Text, "XYZ") {
		t.Error("notice leaks the credential contents")
	}
	if err := Save(Provider{ID: "claude", Anthropic: "https://api.anthropic.com", Key: "k"}); err == nil {
		t.Error("saving claude as a provider should fail")
	}
	if !Reserved("claude") {
		t.Error("claude should be a reserved id")
	}

	// A hand-edited file that mentions claude is still ignored.
	acctWriteFile(t, Path(), `{"providers":[{"id":"claude","models":["x"]}]}`)
	if _, ok := acctFind("claude"); ok {
		t.Error("claude listed from a config entry")
	}
}

func TestAcctNoNoticeWithoutClaudeLogin(t *testing.T) {
	acctEnv(t)
	if n := Notices(); len(n) != 0 {
		t.Errorf("notices without a claude login: %+v", n)
	}
}

func TestAcctCodexBaseDefault(t *testing.T) {
	if CodexBase != "https://chatgpt.com/backend-api/codex" {
		t.Errorf("CodexBase = %q", CodexBase)
	}
}
