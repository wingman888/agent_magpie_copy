package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// routeSandbox points HOME and the XDG dirs at a fresh temp dir.
func routeSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

func writeRouteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setupAccounts signs in to Codex (ChatGPT) and Copilot, and exposes one model
// from each, so both providers have catalog entries to show or hide.
func setupAccounts(t *testing.T, home string) {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{"email": "dev@example.com"})
	jwt := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": jwt, "access_token": "tok-secret", "account_id": "acct-1",
	}})
	writeRouteFile(t, filepath.Join(home, ".codex", "auth.json"), string(auth))
	writeRouteFile(t, filepath.Join(home, ".config", "github-copilot", "hosts.json"),
		`{"github.com:octocat":{"user":"octocat","oauth_token":"gho_secret"}}`)
	writeRouteFile(t, filepath.Join(home, ".config", "dial", "providers.json"),
		`{"providers":[{"id":"codex","models":["gpt-5.5"]},{"id":"copilot","models":["claude-sonnet-4.5"]}]}`)
}

func optionValues(opts []Option) []string {
	var out []string
	for _, o := range opts {
		out = append(out, o.Value)
	}
	return out
}

func hasPrefixIn(vals []string, prefix string) bool {
	for _, v := range vals {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
}

// The Codex CLI's own model list must not offer codex/… account models: it
// would only loop back through dial. Other agents still see them.
func TestCodexAgentOffersNoOwnAccountModels(t *testing.T) {
	home := routeSandbox(t)
	setupAccounts(t, home)

	cx := codex(home)
	var model Field
	for _, f := range cx.Fields {
		if f.Key == "model" {
			model = f
		}
	}
	if model.Options == nil {
		t.Fatal("codex has no model field")
	}
	codexVals := optionValues(model.Options(nil))
	if hasPrefixIn(codexVals, "codex/") {
		t.Errorf("codex's own list offers codex/… models: %v", codexVals)
	}
	if !contains(codexVals, "copilot/claude-sonnet-4.5") {
		t.Errorf("codex's list lost other providers' models: %v", codexVals)
	}

	other := optionValues(viaDial(""))
	if !contains(other, "codex/gpt-5.5") {
		t.Errorf("another agent's list lacks codex/gpt-5.5: %v", other)
	}
}

func TestCodexCatalogFileSkipsOwnAccountModels(t *testing.T) {
	home := routeSandbox(t)
	setupAccounts(t, home)

	for _, m := range dialModelsFor("codex") {
		if strings.HasPrefix(m.ID, "codex/") {
			t.Errorf("codex's model catalog includes its own account model %q", m.ID)
		}
	}
	var sawCopilot bool
	for _, m := range dialModelsFor("codex") {
		if m.ID == "copilot/claude-sonnet-4.5" {
			sawCopilot = true
		}
	}
	if !sawCopilot {
		t.Error("codex's model catalog lost copilot/claude-sonnet-4.5")
	}
	var sawCodex bool
	for _, m := range dialModels() {
		if m.ID == "codex/gpt-5.5" {
			sawCodex = true
		}
	}
	if !sawCodex {
		t.Error("the general catalog should include codex/gpt-5.5")
	}
}

// An account model's note names the signed-in user, not the provider.
func TestAccountModelNoteShowsSignedInUser(t *testing.T) {
	home := routeSandbox(t)
	setupAccounts(t, home)

	var note string
	for _, o := range viaDial("") {
		if o.Value == "codex/gpt-5.5" {
			note = o.Note
		}
	}
	if note != "dev@example.com · via dial" {
		t.Errorf("codex note = %q, want the signed-in user", note)
	}
}
