package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

// cli5Setup points HOME and the dial config dir at a fresh temp dir, writes
// the Gemini CLI files given (empty means absent), and adds one dial
// provider whose model is cli5fake/m1.
func cli5Setup(t *testing.T, settings, env string) (home string, a *Agent) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "no-claude"))
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if settings != "" {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if env != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.Save(provider.Provider{Name: "cli5fake", Chat: "https://fake.example/v1", Key: "sk-cli5", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	return home, gemini(home)
}

// cli5Gemini reads the Gemini CLI state back the way a user would see it.
type cli5Gemini struct {
	selected, model, base, key, foo, other string
	hasSelected, hasModel, hasBase, hasKey bool
}

func cli5Read(t *testing.T, home string) cli5Gemini {
	t.Helper()
	settings := filepath.Join(home, ".gemini", "settings.json")
	env := filepath.Join(home, ".gemini", ".env")
	var s cli5Gemini
	s.selected, s.hasSelected = edit.GetJSON(settings, "security.auth.selectedType")
	s.model, s.hasModel = edit.GetJSON(settings, "model.name")
	s.other, _ = edit.GetJSON(settings, "other")
	s.base, s.hasBase = edit.GetEnvFile(env, "GOOGLE_GEMINI_BASE_URL")
	s.key, s.hasKey = edit.GetEnvFile(env, "GEMINI_API_KEY")
	s.foo, _ = edit.GetEnvFile(env, "FOO")
	return s
}

func TestCli5GeminiCatalogModelRoutesAndNativeRestores(t *testing.T) {
	const settings = `{"other":"keep","security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}` + "\n"
	const env = "FOO=bar\nGEMINI_API_KEY=old-key\nGOOGLE_GEMINI_BASE_URL=https://orig.example\n"
	home, a := cli5Setup(t, settings, env)

	if err := a.Field("model").Set("cli5fake/m1"); err != nil {
		t.Fatal(err)
	}
	// route a second time: the original values must still be the ones stashed
	if err := a.Field("model").Set("cli5fake/m1"); err != nil {
		t.Fatal(err)
	}
	s := cli5Read(t, home)
	if s.base != gateway.URL() || s.key != gateway.Token || s.selected != "gemini-api-key" || s.model != "cli5fake/m1" {
		t.Fatalf("routed state wrong: %+v", s)
	}
	if s.foo != "bar" || s.other != "keep" {
		t.Fatalf("unrelated entries lost: FOO=%q other=%q", s.foo, s.other)
	}
	if got := a.Field("provider").Get(); got != "dial" {
		t.Fatalf("auth while routed = %q, want dial", got)
	}
	if got := a.Field("auth"); got != a.Field("provider") {
		t.Fatal("auth label should find the provider field")
	}
	if err := a.Field("provider").Set("dial"); err != nil {
		t.Fatalf("Set(dial) while routed: %v", err)
	}
	if got := a.Field("provider").Get(); got != "dial" {
		t.Fatalf("Set(dial) changed the state: auth = %q", got)
	}

	// a native model restores the values from before routing
	if err := a.Field("model").Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	s = cli5Read(t, home)
	if s.selected != "oauth-personal" || s.key != "old-key" || s.base != "https://orig.example" {
		t.Fatalf("restore after native model: %+v", s)
	}
	if s.model != "gemini-2.5-flash" {
		t.Fatalf("model.name = %q, want the native model", s.model)
	}
	if s.foo != "bar" || s.other != "keep" {
		t.Fatalf("unrelated entries lost after restore: FOO=%q other=%q", s.foo, s.other)
	}
	if got := a.Field("provider").Get(); got != "custom" {
		t.Fatalf("auth after restore = %q, want custom (the original base URL)", got)
	}
}

func TestCli5GeminiDefaultRemovesWhatWasAbsent(t *testing.T) {
	home, a := cli5Setup(t, `{"other":"keep"}`+"\n", "FOO=bar\n")

	if err := a.Field("model").Set("cli5fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	s := cli5Read(t, home)
	if s.hasSelected || s.hasKey || s.hasBase || s.hasModel {
		t.Fatalf("Default should remove what was absent before routing: %+v", s)
	}
	if s.foo != "bar" || s.other != "keep" {
		t.Fatalf("unrelated entries lost: FOO=%q other=%q", s.foo, s.other)
	}
	if got := a.Field("provider").Get(); got != "google" {
		t.Fatalf("auth after Default = %q, want google", got)
	}
}

func TestCli5GeminiDefaultKeepsPreviousKey(t *testing.T) {
	home, a := cli5Setup(t, `{"security":{"auth":{"selectedType":"gemini-api-key"}},"model":{"name":"gemini-2.5-pro"}}`+"\n",
		"GEMINI_API_KEY=mine\n")

	if err := a.Field("model").Set("cli5fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	s := cli5Read(t, home)
	if s.key != "mine" || s.selected != "gemini-api-key" || s.hasBase || s.hasModel {
		t.Fatalf("Default restored wrong values: %+v", s)
	}
}

func TestCli5GeminiAuthGetAndSet(t *testing.T) {
	home, a := cli5Setup(t, `{}`+"\n", "")
	auth := a.Field("provider")

	if err := auth.Set("dial"); err == nil {
		t.Fatal("Set(dial) on an unrouted CLI should fail")
	}
	if got := auth.Get(); got != "google" {
		t.Fatalf("unrouted auth = %q, want google", got)
	}

	if err := a.Field("model").Set("cli5fake/m1"); err != nil {
		t.Fatal(err)
	}
	if got := auth.Get(); got != "dial" {
		t.Fatalf("routed auth = %q, want dial", got)
	}

	// leaving dial by picking another auth undoes the routing
	if err := auth.Set("google"); err != nil {
		t.Fatal(err)
	}
	s := cli5Read(t, home)
	if s.base != "" || s.hasKey || s.model != "cli5fake/m1" {
		t.Fatalf("leaving dial: %+v", s)
	}
	if got := auth.Get(); got != "google" {
		t.Fatalf("auth after leaving dial = %q, want google", got)
	}
}

func TestCli5GeminiUnknownCatalogRefFails(t *testing.T) {
	_, a := cli5Setup(t, `{}`+"\n", "")
	if err := a.Field("model").Set("cli5nope/m9"); err == nil {
		t.Fatal("unknown catalog reference should be an error")
	}
	if strings.Contains(a.Field("model").Get(), "cli5nope") {
		t.Fatal("a rejected model must not be written")
	}
}

func TestCli5GeminiModelOptionsListCatalog(t *testing.T) {
	_, a := cli5Setup(t, `{}`+"\n", "")
	var found bool
	for _, o := range a.Field("model").Options(nil) {
		if o.Value == "cli5fake/m1" {
			found = true
		}
	}
	if !found {
		t.Fatal("Gemini model picker should offer dial's catalog models")
	}
}

// cli5Codex signs the home directory in to Codex with a ChatGPT login.
func cli5Codex(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := cli5JWT(map[string]any{"email": "cli5@example.test",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
	acc := cli5JWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": id, "access_token": acc, "refresh_token": "r", "account_id": "acct"}})
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func cli5JWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func TestCli5AccountModelsInOtherPickers(t *testing.T) {
	home, _ := cli5Setup(t, `{}`+"\n", "")
	cli5Codex(t, home)
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}

	// Gemini's picker: the account model is reached via dial, noted by the user
	var note string
	for _, o := range gemini(home).Field("model").Options(nil) {
		if o.Value == "codex/gpt-5.5" {
			note = o.Note
		}
	}
	if note != "cli5@example.test · via dial" {
		t.Fatalf("Gemini note for codex model = %q", note)
	}

	// Codex's own picker does not list its account models
	for _, o := range codex(home).Field("model").Options(nil) {
		if strings.HasPrefix(o.Value, "codex/") {
			t.Fatalf("Codex picker lists its own account model %q", o.Value)
		}
	}
}
