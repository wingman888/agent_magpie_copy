package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeJWT builds an unsigned token; dial only reads the payload.
func fakeJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// loginFixture signs the test HOME in to both Codex (ChatGPT mode) and Copilot.
func loginFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "no-claude"))

	id := fakeJWT(map[string]any{
		"email":                       "me@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
	})
	auth, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      id,
			"access_token":  fakeJWT(map[string]any{"exp": 4102444800}),
			"refresh_token": "rt",
			"account_id":    "acct-1",
		},
	})
	writeFixture(t, filepath.Join(home, ".codex", "auth.json"), string(auth))
	writeFixture(t, filepath.Join(home, ".config", "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_test"}}`)
	return home
}

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func findOption(opts []Option, value string) (Option, bool) {
	for _, o := range opts {
		if o.Value == value {
			return o, true
		}
	}
	return Option{}, false
}

// Account models are offered to other agents, and their note names the
// signed-in user rather than the provider.
func TestAccountModelsInOtherAgentsPickers(t *testing.T) {
	loginFixture(t)
	opts := viaDial("")

	c, ok := findOption(opts, "codex/gpt-5.5")
	if !ok {
		t.Fatalf("codex/gpt-5.5 missing from the dial picker: %+v", opts)
	}
	if c.Note != "me@example.com · via dial" {
		t.Errorf("codex note = %q, want the signed-in user", c.Note)
	}
	cp, ok := findOption(opts, "copilot/claude-sonnet-4.5")
	if !ok {
		t.Fatalf("copilot/claude-sonnet-4.5 missing from the dial picker")
	}
	if cp.Note != "octo · via dial" {
		t.Errorf("copilot note = %q, want the GitHub user", cp.Note)
	}
	if _, ok := findOption(opts, "copilot/auto"); ok {
		t.Error("copilot/auto must not be offered before a refresh")
	}

	// the catalog handed to file-based agents uses the same note
	found := false
	for _, m := range dialModels() {
		if m.ID == "codex/gpt-5.5" {
			found = true
			if !strings.HasSuffix(m.Name, " · me@example.com") {
				t.Errorf("catalog name = %q, want the signed-in user as its note", m.Name)
			}
		}
	}
	if !found {
		t.Error("codex/gpt-5.5 missing from dialModels()")
	}
}

// Codex, when routed through dial, does not get its own codex/… models back.
func TestCodexCLIOmitsItsOwnAccountModels(t *testing.T) {
	home := loginFixture(t)
	a := codex(home)
	opts := a.Fields[0].Options(nil)

	for _, o := range opts {
		if strings.HasPrefix(o.Value, "codex/") {
			t.Errorf("Codex's own picker offers %q", o.Value)
		}
	}
	if _, ok := findOption(opts, "copilot/claude-sonnet-4.5"); !ok {
		t.Error("Codex's picker dropped the copilot models it should keep")
	}

	// the catalog file Codex reads when routed through dial
	var doc struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if err := json.Unmarshal(codexCatalog(dialModelsExcept("codex")), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Models) == 0 {
		t.Fatal("codex catalog is empty; expected the copilot models")
	}
	for _, m := range doc.Models {
		if strings.HasPrefix(m.Slug, "codex/") {
			t.Errorf("codex catalog lists %q", m.Slug)
		}
	}

	// other agents keep the codex models
	if _, ok := findOption(viaDial(""), "codex/gpt-5.5"); !ok {
		t.Error("another agent's picker lost codex/gpt-5.5")
	}
}
