package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// signedInHome points HOME and XDG dirs at a temp tree holding a Codex
// ChatGPT login and a Copilot login, so the catalog has both account providers.
func signedInHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	enc := base64.RawURLEncoding.EncodeToString
	claims, _ := json.Marshal(map[string]any{
		"email":                       "me@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
	})
	auth := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":     enc([]byte(`{"alg":"none"}`)) + "." + enc(claims) + ".sig",
			"access_token": "at-test",
			"account_id":   "acct-1",
		},
	}
	write(t, filepath.Join(home, ".codex", "auth.json"), auth)
	write(t, filepath.Join(home, ".config", "github-copilot", "hosts.json"),
		map[string]any{"github.com": map[string]any{"user": "octo", "oauth_token": "gho_x"}})
	return home
}

func write(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func optionValues(opts []Option) []string {
	var out []string
	for _, o := range opts {
		out = append(out, o.Value)
	}
	return out
}

func TestCodexCLIDoesNotOfferItsOwnAccountModels(t *testing.T) {
	home := signedInHome(t)

	for _, m := range dialModelsSkipping(ownAccount("codex")) {
		if strings.HasPrefix(m.ID, "codex/") {
			t.Fatalf("Codex CLI list offers %s back to itself", m.ID)
		}
	}
	var modelField *Field
	for _, f := range codex(home).Fields {
		if f.Key == "model" {
			modelField = &f
		}
	}
	if modelField == nil {
		t.Fatal("codex has no model field")
	}
	vals := optionValues(modelField.Options(map[string]string{}))
	var hasCopilot bool
	for _, v := range vals {
		if strings.HasPrefix(v, "codex/") {
			t.Fatalf("Codex model options include %s", v)
		}
		if strings.HasPrefix(v, "copilot/") {
			hasCopilot = true
		}
	}
	if !hasCopilot {
		t.Fatalf("Codex model options lack copilot/... models: %v", vals)
	}
}

func TestOtherAgentsStillOfferCodexAccountModels(t *testing.T) {
	signedInHome(t)
	var found *Option
	for _, o := range viaDial("") {
		if o.Value == "codex/gpt-5.5" {
			o := o
			found = &o
		}
	}
	if found == nil {
		t.Fatal("codex/gpt-5.5 missing from the catalog picker")
	}
	if found.Note != "me@example.com · via dial" {
		t.Errorf("note = %q, want the signed-in user, not the provider name", found.Note)
	}
}
