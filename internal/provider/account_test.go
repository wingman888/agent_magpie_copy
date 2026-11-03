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

// isolate points HOME, the XDG dirs and Claude's config dir at a fresh tree.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeJWT builds an unsigned JWT whose middle segment carries the claims.
func fakeJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// codexLoginFile writes ~/.codex/auth.json for a ChatGPT (or API-key) login.
func codexLoginFile(t *testing.T, home, mode, email string) string {
	t.Helper()
	idTok := fakeJWT(map[string]any{
		"email":                       email,
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
	})
	access := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	b, _ := json.Marshal(map[string]any{
		"auth_mode": mode,
		"tokens": map[string]any{
			"id_token":      idTok,
			"access_token":  access,
			"refresh_token": "rt-secret",
			"account_id":    "acct-1",
		},
	})
	p := filepath.Join(home, ".codex", "auth.json")
	put(t, p, string(b))
	return p
}

// copilotLoginFile writes the GitHub Copilot apps.json under XDG_CONFIG_HOME.
func copilotLoginFile(t *testing.T) {
	t.Helper()
	put(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octocat","oauth_token":"gho_test"}}`)
}

func byID(ps []Provider, id string) (Provider, bool) {
	for _, p := range ps {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

func TestCodexAPIKeyModeIsNotAnAccount(t *testing.T) {
	home := isolate(t)
	codexLoginFile(t, home, "apikey", "me@example.com")
	if _, ok := byID(All(), "codex"); ok {
		t.Fatal("codex offered although auth_mode is apikey")
	}
	if _, ok := signedInAs("codex"); ok {
		t.Fatal("signedInAs(codex) is true for apikey mode")
	}
}

func TestCodexLoginIsOffered(t *testing.T) {
	home := isolate(t)
	codexLoginFile(t, home, "chatgpt", "me@example.com")
	p, ok := byID(All(), "codex")
	if !ok {
		t.Fatal("codex missing from All()")
	}
	if p.Account == nil || p.Account.User != "me@example.com" || p.Account.Plan != "plus" || p.Account.Agent != "codex" {
		t.Fatalf("account = %+v", p.Account)
	}
	if p.Key != "" {
		t.Fatalf("Key must stay empty, got %q", p.Key)
	}
	if !p.Ready() {
		t.Fatal("signed-in codex should be ready without a key")
	}
	if p.Base(Responses) != CodexBase {
		t.Fatalf("responses base = %q, want %q", p.Base(Responses), CodexBase)
	}
	if got, model, ok := Resolve("codex/gpt-5.5"); !ok || got.ID != "codex" || model != "gpt-5.5" {
		t.Fatalf("Resolve(codex/gpt-5.5) = %q, %q, %v", got.ID, model, ok)
	}
	found := false
	for _, e := range Catalog() {
		if e.ID == "codex/gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Fatal("codex/gpt-5.5 is not in Catalog()")
	}
}

func TestStaleAccountPicksAreIgnored(t *testing.T) {
	isolate(t)
	if err := Save(Provider{ID: "copilot", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := byID(All(), "copilot"); ok {
		t.Fatal("copilot offered without a login")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "copilot/") {
			t.Fatalf("catalog has %s without a login", e.ID)
		}
	}
	copilotLoginFile(t)
	p, ok := byID(All(), "copilot")
	if !ok {
		t.Fatal("copilot missing after sign-in")
	}
	if len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Fatalf("picks = %v", p.Models)
	}
}

func TestSaveAccountKeepsOnlyModelPicks(t *testing.T) {
	isolate(t)
	copilotLoginFile(t)
	err := Save(Provider{
		ID: "copilot", Name: "mine", Key: "secret-key", Chat: "https://evil.example",
		Models: []string{" gpt-5.5 ", "gpt-5.5", ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, bad := range []string{"secret-key", "evil.example", `"name"`, `"key"`, `"chat"`} {
		if strings.Contains(s, bad) {
			t.Fatalf("providers.json holds %s:\n%s", bad, s)
		}
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Providers) != 1 || f.Providers[0].ID != "copilot" ||
		len(f.Providers[0].Models) != 1 || f.Providers[0].Models[0] != "gpt-5.5" {
		t.Fatalf("stored = %+v", f.Providers)
	}
	p, ok := byID(All(), "copilot")
	if !ok || p.Key != "" || len(p.Models) != 1 {
		t.Fatalf("copilot = %+v, %v", p, ok)
	}
}

func TestDeleteAccountOnlyClearsPicks(t *testing.T) {
	home := isolate(t)
	credPath := codexLoginFile(t, home, "chatgpt", "me@example.com")
	before, _ := os.ReadFile(credPath)

	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p, ok := byID(All(), "codex")
	if !ok {
		t.Fatal("codex disappeared after Delete while still signed in")
	}
	if len(p.Models) != 0 {
		t.Fatalf("picks after Delete = %v", p.Models)
	}
	after, _ := os.ReadFile(credPath)
	if string(before) != string(after) {
		t.Fatal("Delete touched the Codex login file")
	}
	if err := Delete("copilot"); err != nil {
		t.Fatalf("Delete of a signed-out account: %v", err)
	}

	// Signing out makes the provider disappear.
	if err := os.Remove(credPath); err != nil {
		t.Fatal(err)
	}
	if _, ok := byID(All(), "codex"); ok {
		t.Fatal("codex still offered after sign-out")
	}
}

func TestClaudeIsNeverOffered(t *testing.T) {
	home := isolate(t)
	if n := ClaudeNotice(); n != "" {
		t.Fatalf("notice without a Claude login: %q", n)
	}
	secret := "claude-token-do-not-leak"
	put(t, filepath.Join(home, ".claude", ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"`+secret+`"}}`)

	for _, p := range All() {
		if p.ID == claudeID {
			t.Fatal("claude offered as a provider")
		}
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, claudeID+"/") {
			t.Fatalf("catalog has %s", e.ID)
		}
	}
	n := ClaudeNotice()
	if n == "" {
		t.Fatal("no notice for a signed-in Claude Code")
	}
	if strings.Contains(n, secret) {
		t.Fatal("notice leaks the Claude token")
	}
	if err := Save(Provider{ID: claudeID, Name: "claude", Anthropic: "https://api.anthropic.com", Key: "k"}); err == nil {
		t.Fatal("saved a claude provider")
	}
}

func TestStoredClaudeRecordIsIgnored(t *testing.T) {
	isolate(t)
	put(t, Path(), `{"providers":[{"id":"claude","anthropic":"https://api.anthropic.com","key":"k"}]}`)
	if _, ok := byID(All(), claudeID); ok {
		t.Fatal("stored claude record offered")
	}
}

func TestCopilotListHasNoAuto(t *testing.T) {
	isolate(t)
	copilotLoginFile(t)
	p, ok := byID(All(), "copilot")
	if !ok {
		t.Fatal("copilot missing from All()")
	}
	avail := p.Available()
	if len(avail) == 0 {
		t.Fatal("copilot has no built-in models")
	}
	for _, m := range avail {
		if m.ID == "auto" {
			t.Fatal("auto is in the copilot list")
		}
	}
	found := false
	for _, e := range Catalog() {
		if e.ID == "copilot/gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Fatal("copilot/gpt-5.5 is not in Catalog()")
	}
}

func TestCopilotModelFilter(t *testing.T) {
	body := `{"data":[
		{"id":"gpt-5.5","name":"GPT-5.5","model_picker_enabled":true,"capabilities":{"type":"chat"},"policy":{"state":"enabled"}},
		{"id":"auto","name":"Auto","model_picker_enabled":true,"capabilities":{"type":"chat"}},
		{"id":"text-embedding-3-small","capabilities":{"type":"embeddings"}},
		{"id":"nomic-embedding","capabilities":{"type":"chat"}},
		{"id":"internal-router","model_picker_enabled":false,"capabilities":{"type":"chat"}},
		{"id":"gemini-x","capabilities":{"type":"chat"},"policy":{"state":"unconfigured"}},
		{"id":"claude-y","capabilities":{"type":"chat"}}
	]}`
	ms, err := parseCopilotModels([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.ID)
	}
	if strings.Join(got, ",") != "gpt-5.5,claude-y" {
		t.Fatalf("kept %v", got)
	}
	if ms[0].Name != "GPT-5.5" {
		t.Fatalf("name = %q", ms[0].Name)
	}
	if _, err := parseCopilotModels([]byte("<html>")); err == nil {
		t.Fatal("non-JSON body accepted")
	}
}
