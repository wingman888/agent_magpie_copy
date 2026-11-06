package provider

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tcfgEnv gives the test a private HOME, config and cache dir, so no real
// login or dial configuration is read.
func tcfgEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

func tcfgFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// tcfgCodexLogin writes a ChatGPT-mode Codex login for the given email.
func tcfgCodexLogin(t *testing.T, home, email string) {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{
		"email":                       email,
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
	})
	idTok := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	auth, _ := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":     idTok,
			"access_token": "tok-tcfg-access",
			"account_id":   "acct-tcfg",
		},
	})
	tcfgFile(t, filepath.Join(home, ".codex", "auth.json"), string(auth))
}

func tcfgConfig(t *testing.T, content string) string {
	t.Helper()
	p := Path()
	tcfgFile(t, p, content)
	return p
}

func tcfgByID(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func TestTcfgSaveKeepsOnlyModelChoice(t *testing.T) {
	home := tcfgEnv(t)
	tcfgCodexLogin(t, home, "dev@example.com")

	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	p.Chat = "https://evil.example.com/v1"
	p.Responses = "https://evil.example.com/v1"
	p.Key = "sk-tcfg-secret"
	p.Models = []string{"gpt-5.5"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, bad := range []string{"evil.example.com", "sk-tcfg-secret", `"key"`, `"chat"`, `"responses"`, "https://"} {
		if strings.Contains(s, bad) {
			t.Fatalf("stored config must hold only the model choice, found %q in:\n%s", bad, s)
		}
	}

	got, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.Account == nil || got.Chat != "" || got.Key != "" {
		t.Fatalf("account provider should carry no address or key: %+v", got)
	}
	if len(got.Models) != 1 || got.Models[0] != "gpt-5.5" {
		t.Fatalf("only the chosen models should come back, got %v", got.Models)
	}
	if exp := got.Exposed(); len(exp) != 1 || exp[0].ID != "gpt-5.5" {
		t.Fatalf("exposed should be the chosen model only, got %v", exp)
	}
}

func TestTcfgSaveWithoutAccountFlagStillStoresChoiceOnly(t *testing.T) {
	home := tcfgEnv(t)
	tcfgCodexLogin(t, home, "dev@example.com")

	if err := Save(Provider{ID: "codex", Chat: "https://evil.example.com/v1", Key: "sk-tcfg-x", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "evil.example.com") || strings.Contains(string(b), "sk-tcfg-x") {
		t.Fatalf("address and key must be ignored for a signed-in login:\n%s", b)
	}
	if p := tcfgByID(All(), "codex"); p == nil || p.Account == nil || len(p.Models) != 1 {
		t.Fatalf("codex should be the account provider with its choice, got %+v", p)
	}
}

func TestTcfgDeleteClearsChoiceKeepsProvider(t *testing.T) {
	home := tcfgEnv(t)
	tcfgCodexLogin(t, home, "dev@example.com")
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5", "gpt-5.4"}}); err != nil {
		t.Fatal(err)
	}

	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatalf("the account provider must survive a delete: %v", err)
	}
	if p.Account == nil {
		t.Fatal("codex should still be the account provider")
	}
	if len(p.Models) != 0 {
		t.Fatalf("the model choice should be cleared, got %v", p.Models)
	}
}

func TestTcfgResidualChoiceMakesNoProvider(t *testing.T) {
	tcfgEnv(t)
	tcfgConfig(t, `{"providers":[{"id":"codex","models":["gpt-5.5"]},{"id":"copilot","models":["claude-sonnet-4.5"]}]}`)

	if n := len(Accounts()); n != 0 {
		t.Fatalf("no login is signed in, Accounts should be empty, got %d", n)
	}
	for _, p := range All() {
		if p.ID == "codex" || p.ID == "copilot" {
			t.Fatalf("a residual choice must not become a provider: %+v", p)
		}
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") || strings.HasPrefix(e.ID, "copilot/") {
			t.Fatalf("a residual choice must not reach the catalog: %s", e.ID)
		}
	}
	if _, err := Find("codex"); err == nil {
		t.Fatal("Find should not resolve a residual choice")
	}
}

func TestTcfgLogoutRemovesProviderAndCatalogEntries(t *testing.T) {
	home := tcfgEnv(t)
	tcfgCodexLogin(t, home, "dev@example.com")
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if tcfgByID(All(), "codex") == nil {
		t.Fatal("precondition: codex should be offered while signed in")
	}

	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if tcfgByID(All(), "codex") != nil {
		t.Fatal("logout should remove the provider even though the choice is still stored")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Fatalf("logout should remove codex models from the catalog, got %s", e.ID)
		}
	}
}

func TestTcfgUserProviderWithAddressWins(t *testing.T) {
	home := tcfgEnv(t)
	tcfgCodexLogin(t, home, "dev@example.com")
	tcfgConfig(t, `{"providers":[{"id":"codex","name":"Mine","chat":"https://relay.example.com/v1","key":"sk-own","models":["m1"]}]}`)

	all := All()
	n := 0
	for _, p := range all {
		if p.ID == "codex" {
			n++
			if p.Account != nil {
				t.Fatal("the user's own provider must win over the sign-in")
			}
			if p.Chat != "https://relay.example.com/v1" {
				t.Fatalf("the user's own address must be kept, got %q", p.Chat)
			}
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one codex provider, got %d", n)
	}
}

func TestTcfgCatalogLabelsAndResolve(t *testing.T) {
	home := tcfgEnv(t)
	tcfgCodexLogin(t, home, "dev@example.com")
	tcfgFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json"),
		`{"github.com:octo":{"user":"octo","oauth_token":"gho_tcfg"}}`)

	ids := map[string]Entry{}
	for _, e := range Catalog() {
		ids[e.ID] = e
	}
	if _, ok := ids["codex/gpt-5.5"]; !ok {
		t.Fatalf("catalog should hold codex/gpt-5.5, have %d entries", len(ids))
	}
	if _, ok := ids["copilot/claude-sonnet-4.5"]; !ok {
		t.Fatal("catalog should hold copilot/claude-sonnet-4.5")
	}
	if _, ok := ids["copilot/auto"]; ok {
		t.Fatal("catalog must not hold copilot/auto")
	}
	if e := ids["codex/gpt-5.5"]; e.Provider.Label() != "dev@example.com" {
		t.Fatalf("account models should be labelled with the signed-in user, got %q", e.Provider.Label())
	}

	p, model, ok := Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || model != "gpt-5.5" {
		t.Fatalf("Resolve(codex/gpt-5.5) = %q, %q, %v", p.ID, model, ok)
	}
}
