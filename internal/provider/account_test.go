package provider

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points HOME, XDG_CONFIG_HOME and CLAUDE_CONFIG_DIR at fresh temp
// directories so no real login on the machine leaks into the test.
func isolate(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	resetExcluded()
	return home
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeJWT builds an unsigned JWT with the given payload claims.
func fakeJWT(t *testing.T, claims any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(b) + ".sig"
}

func codexIDToken(t *testing.T, email, plan string) string {
	claims := map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": plan},
	}
	if email != "" {
		claims["email"] = email
	}
	return fakeJWT(t, claims)
}

func codexAuthJSON(t *testing.T, mode, idToken string) string {
	b, err := json.Marshal(map[string]any{
		"auth_mode":      mode,
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      idToken,
			"access_token":  "at-secret-123",
			"refresh_token": "rt-secret-456",
			"account_id":    "acct-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func findAccount(ps []Provider, id string) (Provider, bool) {
	for _, p := range ps {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

func TestCodexLoginAndLogout(t *testing.T) {
	home := isolate(t)
	authPath := filepath.Join(home, ".codex", "auth.json")
	writeFile(t, authPath, codexAuthJSON(t, "chatgpt", codexIDToken(t, "me@example.com", "pro")))

	p, ok := findAccount(Accounts(), "codex")
	if !ok {
		t.Fatal("codex account not offered while signed in")
	}
	if p.Account == nil || p.Account.Agent != "codex" || p.Account.User != "me@example.com" || p.Account.Plan != "pro" {
		t.Fatalf("unexpected account: %+v", p.Account)
	}
	if p.Key != "" || !p.Ready() {
		t.Fatalf("account provider should be ready without a key: key=%q ready=%v", p.Key, p.Ready())
	}
	if p.Name != "Codex" {
		t.Errorf("name = %q, want Codex", p.Name)
	}
	if len(p.Models) == 0 {
		t.Error("expected built-in Codex models")
	}

	// Logging out in Codex removes the provider at once.
	if err := os.Remove(authPath); err != nil {
		t.Fatal(err)
	}
	if _, ok := findAccount(Accounts(), "codex"); ok {
		t.Fatal("codex still offered after its auth file was removed")
	}
}

func TestCodexAPIKeyModeIsNotAccount(t *testing.T) {
	home := isolate(t)
	authPath := filepath.Join(home, ".codex", "auth.json")

	writeFile(t, authPath, `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test"}`)
	if _, ok := findAccount(Accounts(), "codex"); ok {
		t.Error("API-key mode counted as an account")
	}

	// Tokens alongside an API key, with no auth_mode, is still API-key mode.
	writeFile(t, authPath, `{"OPENAI_API_KEY":"sk-test","tokens":{"access_token":"at","id_token":""}}`)
	if _, ok := findAccount(Accounts(), "codex"); ok {
		t.Error("API key present without auth_mode counted as an account")
	}

	// ChatGPT mode without an access token is not signed in.
	writeFile(t, authPath, `{"auth_mode":"chatgpt","tokens":{"access_token":""}}`)
	if _, ok := findAccount(Accounts(), "codex"); ok {
		t.Error("empty access token counted as an account")
	}
}

func TestCodexUserFallbackWithoutEmail(t *testing.T) {
	home := isolate(t)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"),
		codexAuthJSON(t, "chatgpt", codexIDToken(t, "", "plus")))
	p, ok := findAccount(Accounts(), "codex")
	if !ok {
		t.Fatal("codex account missing")
	}
	if p.Account.User != "ChatGPT" {
		t.Errorf("user = %q, want ChatGPT", p.Account.User)
	}
	if p.Account.Plan != "plus" {
		t.Errorf("plan = %q, want plus", p.Account.Plan)
	}
}

func TestParseJWTClaims(t *testing.T) {
	tok := codexIDToken(t, "a@b.c", "pro")
	c := parseJWTClaims(tok)
	if c.Email != "a@b.c" || c.Auth.Plan != "pro" {
		t.Fatalf("claims = %+v", c)
	}
	if c := parseJWTClaims("not-a-jwt"); c.Email != "" || c.Auth.Plan != "" {
		t.Errorf("malformed token gave claims: %+v", c)
	}
	if c := parseJWTClaims(""); c.Email != "" {
		t.Errorf("empty token gave claims: %+v", c)
	}
}

func TestCopilotLoginLogoutAndNoTokenInConfig(t *testing.T) {
	isolate(t)
	xdg := os.Getenv("XDG_CONFIG_HOME")
	hosts := filepath.Join(xdg, "github-copilot", "hosts.json")
	writeFile(t, hosts, `{"github.com":{"user":"octo","oauth_token":"gho_secret_999"}}`)

	p, ok := findAccount(Accounts(), "copilot")
	if !ok {
		t.Fatal("copilot account not offered while signed in")
	}
	if p.Account.User != "octo" || p.Account.Agent != "copilot" {
		t.Fatalf("unexpected account: %+v", p.Account)
	}
	if p.Name != "Copilot" || !p.Ready() {
		t.Errorf("copilot should be a ready account named Copilot: %+v", p)
	}
	for _, m := range p.Models {
		if m == "auto" {
			t.Error("built-in Copilot list must not contain auto")
		}
	}

	// The provider's JSON form must not carry the token.
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "gho_secret_999") {
		t.Fatalf("token leaked into provider JSON: %s", b)
	}

	if err := os.Remove(hosts); err != nil {
		t.Fatal(err)
	}
	if _, ok := findAccount(Accounts(), "copilot"); ok {
		t.Fatal("copilot still offered after logout")
	}
}

func TestCopilotAppsJSONAndDefaultName(t *testing.T) {
	isolate(t)
	apps := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json")
	writeFile(t, apps, `{"other.com:x":{"user":"nope","oauth_token":"t"},"github.com:Iv1.abc":{"oauth_token":"gho_x"}}`)

	p, ok := findAccount(Accounts(), "copilot")
	if !ok {
		t.Fatal("copilot account missing from apps.json")
	}
	if p.Account.User != "GitHub" {
		t.Errorf("user = %q, want GitHub when name is absent", p.Account.User)
	}
}

func TestCopilotEntryWithoutTokenIsNotLogin(t *testing.T) {
	isolate(t)
	hosts := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "hosts.json")
	writeFile(t, hosts, `{"github.com":{"user":"octo"}}`)
	if _, ok := findAccount(Accounts(), "copilot"); ok {
		t.Error("entry with no oauth_token counted as signed in")
	}
}

func TestClaudeLoginIsExcludedNotOffered(t *testing.T) {
	home := isolate(t)
	if len(Excluded()) != 0 {
		t.Fatal("no Claude login, but Excluded is not empty")
	}

	cred := filepath.Join(home, ".claude", ".credentials.json")
	writeFile(t, cred, `{"claudeAiOauth":{"accessToken":"sk-ant-secret"}}`)
	resetExcluded()

	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "claude" || ex[0].Why == "" {
		t.Fatalf("Excluded = %+v, want one claude exclusion with a reason", ex)
	}
	if strings.Contains(ex[0].Why, "sk-ant") {
		t.Error("exclusion reason leaks credentials")
	}
	if _, ok := findAccount(Accounts(), "claude"); ok {
		t.Error("claude offered as an account provider")
	}
}

func TestClaudeConfigDirOverride(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeFile(t, filepath.Join(dir, ".credentials.json"), `{}`)
	resetExcluded()
	if ex := Excluded(); len(ex) != 1 || ex[0].Agent != "claude" {
		t.Fatalf("CLAUDE_CONFIG_DIR login not excluded: %+v", ex)
	}
}

func TestExcludedCachesWithinTTL(t *testing.T) {
	home := isolate(t)
	if len(Excluded()) != 0 {
		t.Fatal("expected no exclusions")
	}
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{}`)
	// Still inside the cache window: the stale answer is returned.
	if len(Excluded()) != 0 {
		t.Error("Excluded did not reuse its cached result within the TTL")
	}
}

func TestUserAddressedProviderOverridesAccount(t *testing.T) {
	home := isolate(t)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"),
		codexAuthJSON(t, "chatgpt", codexIDToken(t, "me@example.com", "pro")))
	if err := Save(Provider{ID: "codex", Name: "My Codex", Chat: "https://relay.example.com/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := findAccount(Accounts(), "codex"); ok {
		t.Fatal("a self-built provider with the same id should win over the account")
	}
}

func TestStoredSelectionOnlyShapesAccountModels(t *testing.T) {
	home := isolate(t)
	writeFile(t, filepath.Join(home, ".config", "github-copilot", "hosts.json"),
		`{"github.com":{"user":"octo","oauth_token":"gho_x"}}`)
	writeFile(t, Path(), `{"providers":[{"id":"copilot","name":"Copilot","models":["gpt-5.5"," gpt-5.5 ","claude-sonnet-4.5"]}]}`)

	p, ok := findAccount(Accounts(), "copilot")
	if !ok {
		t.Fatal("copilot missing")
	}
	if len(p.Models) != 2 || p.Models[0] != "gpt-5.5" || p.Models[1] != "claude-sonnet-4.5" {
		t.Fatalf("models = %v, want the stored selection", p.Models)
	}
}

func TestSelectionWithoutLoginIsNotAProvider(t *testing.T) {
	isolate(t)
	writeFile(t, Path(), `{"providers":[{"id":"codex","name":"Codex","models":["gpt-5.5"]}]}`)
	if _, ok := findAccount(Accounts(), "codex"); ok {
		t.Fatal("a stored selection with no login must not produce a provider")
	}
}
