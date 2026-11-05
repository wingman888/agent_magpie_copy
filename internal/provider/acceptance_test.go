package provider

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// 6. A signed-in Codex login is a provider named by its e-mail.
func TestZv_AccCodexSignedInIsProvider(t *testing.T) {
	h := zvAccHome(t)
	zvAccCodexAuth(t, h, time.Now().Add(time.Hour))
	p := zvAccFindID(All(), "codex")
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
func TestZv_AccCopilotSignedInBuiltinList(t *testing.T) {
	h := zvAccHome(t)
	zvAccWriteFile(t, filepath.Join(h, "config", "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_test"}}`)
	p := zvAccFindID(All(), "copilot")
	if p == nil {
		t.Fatal("copilot is not in All()")
	}
	ids := zvAccModelIDs(p.Available())
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
func TestZv_AccAccountSaveDeleteAndLogout(t *testing.T) {
	h := zvAccHome(t)
	zvAccCodexAuth(t, h, time.Now().Add(time.Hour))
	const oauth = "gho_secret_token_123"
	zvAccWriteFile(t, filepath.Join(h, "config", "github-copilot", "apps.json"),
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
	if p := zvAccFindID(All(), "codex"); p == nil || strings.Join(zvAccModelIDs(p.Exposed()), ",") != "gpt-5.5" {
		t.Fatalf("codex picks not kept: %+v", p)
	}

	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p := zvAccFindID(All(), "codex")
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
	if zvAccFindID(All(), "codex") != nil {
		t.Error("codex still a provider after logout")
	}
	if zvAccFindID(All(), "copilot") == nil {
		t.Error("copilot should be unaffected by codex logout")
	}
}

// 10. Claude Code's login is never a provider, and its token is never read into the provider list.
func TestZv_AccClaudeNeverAProvider(t *testing.T) {
	h := zvAccHome(t)
	const secret = "sk-ant-oat-SECRET-TOKEN-987"
	zvAccWriteFile(t, filepath.Join(h, ".claude", ".credentials.json"),
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

// Local copies of helpers this file needs (kept here so the file stands alone).

func zvAccJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func zvAccHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(h, ".claude"))
	return h
}

func zvAccWriteFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func zvAccCodexAuth(t *testing.T, h string, exp time.Time) {
	t.Helper()
	access := zvAccJWT(t, map[string]any{"exp": exp.Unix()})
	idt := zvAccJWT(t, map[string]any{
		"email":                        "me@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
	})
	b, _ := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{"id_token": idt, "access_token": access,
			"refresh_token": "rt-old", "account_id": "acct-1"},
		"last_refresh": "2026-01-01T00:00:00Z",
		"custom":       "keep",
	})
	zvAccWriteFile(t, filepath.Join(h, ".codex", "auth.json"), string(b))
}

func zvAccFindID(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func zvAccModelIDs(ms []catalog.Model) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}
