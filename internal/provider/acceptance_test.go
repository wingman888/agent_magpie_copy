package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 6. A signed-in Codex login is a provider named by its e-mail.
func TestAccCodexSignedInIsProvider(t *testing.T) {
	h := home(t)
	codexAuth(t, h, time.Now().Add(time.Hour))
	p := findID(All(), "codex")
	if p == nil {
		t.Fatal("codex is not in All()")
	}
	if p.Account == nil || p.Account.User != "me@example.com" {
		t.Fatalf("account = %+v, want me@example.com", p.Account)
	}
	if !p.Ready() {
		t.Error("account provider must be ready without a key")
	}
}

// 8. A signed-in Copilot is a provider; its built-in list is non-empty and has no auto.
func TestAccCopilotSignedInBuiltinList(t *testing.T) {
	h := home(t)
	writeFile(t, filepath.Join(h, "config", "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_test"}}`)
	p := findID(All(), "copilot")
	if p == nil {
		t.Fatal("copilot is not in All()")
	}
	ids := modelIDs(p.Available())
	if len(ids) == 0 {
		t.Fatal("built-in Copilot list is empty")
	}
	for _, id := range ids {
		if id == "auto" {
			t.Fatal("built-in Copilot list contains auto")
		}
	}
}

// 9. Saving an account keeps only its picks; deleting clears picks only;
// logging out hides the account and leaves the picks inert.
func TestAccAccountSaveDeleteAndLogout(t *testing.T) {
	h := home(t)
	codexAuth(t, h, time.Now().Add(time.Hour))
	const oauth = "gho_secret_token_123"
	writeFile(t, filepath.Join(h, "config", "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"`+oauth+`"}}`)

	if err := Save(Provider{ID: "codex", Key: "sk-leak", Name: "Leaky", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "copilot", Key: "sk-leak", Name: "Leaky", Models: []string{"gpt-4.1"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, bad := range []string{`"key"`, `"name"`, "sk-leak", oauth, "access_token", "refresh_token", "me@example.com"} {
		if strings.Contains(s, bad) {
			t.Errorf("config file contains %q: %s", bad, s)
		}
	}
	if p := findID(All(), "codex"); p == nil || strings.Join(modelIDs(p.Exposed()), ",") != "gpt-5.5" {
		t.Fatalf("codex picks not kept: %+v", p)
	}

	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p := findID(All(), "codex")
	if p == nil {
		t.Fatal("Delete logged codex out: it is no longer in All()")
	}
	if len(p.Models) != 0 {
		t.Errorf("Delete kept picks: %v", p.Models)
	}

	// signing out: the login file goes away; the stale pick must not reappear
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if findID(All(), "codex") != nil {
		t.Error("codex still a provider after logout")
	}
	if findID(All(), "copilot") == nil {
		t.Error("copilot should be unaffected by codex logout")
	}
}

// 10. Claude Code's login is never a provider, and its token is never read into the provider list.
func TestAccClaudeNeverAProvider(t *testing.T) {
	h := home(t)
	const secret = "sk-ant-oat-SECRET-TOKEN-987"
	writeFile(t, filepath.Join(h, ".claude", ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"`+secret+`"}}`)
	f := Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "https://example.invalid/v1", Models: []string{"m1"}}
	if err := Save(f); err != nil {
		t.Fatal(err)
	}
	all := All()
	for _, p := range all {
		if p.ID == "claude" {
			t.Fatal("claude is listed as a provider")
		}
	}
	b, _ := json.Marshal(all)
	if strings.Contains(string(b), secret) {
		t.Fatal("provider list contains the Claude token")
	}
	if notes := strings.Join(Notes(), "\n"); !strings.Contains(notes, "已登录但不提供") {
		t.Errorf("no note about Claude Code: %q", notes)
	}
	if err := Save(Provider{ID: "claude", Chat: "https://example.invalid/v1", Key: "k"}); err == nil {
		t.Error("saving claude as a provider must fail")
	}
}
