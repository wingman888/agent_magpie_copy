package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// accountSandbox signs Codex (ChatGPT) and Copilot in, inside a temporary
// HOME, so no real credential is read or changed.
func accountSandbox(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))

	claims := map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}}
	b, _ := json.Marshal(claims)
	idTok := "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	auth := `{"auth_mode":"chatgpt","tokens":{"id_token":"` + idTok + `","access_token":"at","account_id":"acc"}}`
	writeFile(t, filepath.Join(root, "codex", "auth.json"), auth)
	writeFile(t, filepath.Join(root, "config", "github-copilot", "apps.json"),
		`{"github.com:Iv1":{"user":"octo","oauth_token":"gho_test"}}`)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
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

func findOption(t *testing.T, opts []Option, value string) Option {
	t.Helper()
	for _, o := range opts {
		if o.Value == value {
			return o
		}
	}
	t.Fatalf("no option %q in %v", value, optionValues(opts))
	return Option{}
}

func TestCodexDoesNotOfferItsOwnLoginThroughDial(t *testing.T) {
	accountSandbox(t)

	// Codex's own picker: nothing routed back through its own ChatGPT login.
	for _, v := range optionValues(viaDialFor("", "codex")) {
		if strings.HasPrefix(v, "codex/") {
			t.Fatalf("codex's own list still offers %q", v)
		}
	}
	found := false
	for _, v := range optionValues(viaDialFor("", "codex")) {
		if v == "copilot/claude-sonnet-4.5" {
			found = true
		}
	}
	if !found {
		t.Fatal("other agents' login models should still be offered to codex")
	}

	// Other agents still get the full list, codex included.
	all := optionValues(viaDial(""))
	if !contains(all, "codex/gpt-5.5") || !contains(all, "copilot/claude-sonnet-4.5") {
		t.Fatalf("catalog missing account models: %v", all)
	}

	// The model-field options of the Codex agent itself.
	a := codex(t.TempDir())
	var modelOpts []Option
	for _, f := range a.Fields {
		if f.Key == "model" {
			modelOpts = f.Options(nil)
		}
	}
	for _, v := range optionValues(modelOpts) {
		if strings.HasPrefix(v, "codex/") {
			t.Fatalf("codex model picker offers its own login: %q", v)
		}
	}

	// Its dial catalog file, as written for routed use, skips it too.
	for _, m := range dialModelsFor("codex") {
		if strings.HasPrefix(m.ID, "codex/") {
			t.Fatalf("codex catalog still lists %q", m.ID)
		}
	}
	if len(dialModels()) <= len(dialModelsFor("codex")) {
		t.Fatal("unfiltered dial models should include the codex login")
	}
}

func TestAccountNotesShowLoginUser(t *testing.T) {
	accountSandbox(t)

	gpt := findOption(t, viaDial(""), "codex/gpt-5.5")
	if gpt.Note != "me@example.com · via dial" {
		t.Fatalf("codex note = %q, want the signed-in user", gpt.Note)
	}
	if gpt.Group != "Codex" {
		t.Fatalf("codex group = %q", gpt.Group)
	}
	cp := findOption(t, viaDial(""), "copilot/claude-sonnet-4.5")
	if cp.Note != "octo · via dial" {
		t.Fatalf("copilot note = %q, want the GitHub user", cp.Note)
	}

	for _, m := range dialModels() {
		if m.ID == "codex/gpt-5.5" && !strings.HasSuffix(m.Name, " · me@example.com") {
			t.Fatalf("catalog model name = %q, want login user as note", m.Name)
		}
	}
}
