package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

// gemrouteSetup points dial's provider and stash files at a temp dir, adds a
// ready catalog provider "fake", and returns the Gemini CLI agent for a fresh
// temp home along with that home's .gemini directory.
func gemrouteSetup(t *testing.T, env, settings string) (*Agent, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Chat: "http://127.0.0.1:9/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if env != "" {
		gemrouteWrite(t, filepath.Join(dir, ".env"), env)
	}
	if settings != "" {
		gemrouteWrite(t, filepath.Join(dir, "settings.json"), settings)
	}
	return gemini(home), dir
}

func gemrouteWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gemrouteState reads the four routed settings from the CLI's files.
type gemrouteState struct {
	base, key, auth, model string
	hasBase, hasKey        bool
	hasAuth, hasModel      bool
}

func gemrouteRead(t *testing.T, dir string) gemrouteState {
	t.Helper()
	envPath := filepath.Join(dir, ".env")
	path := filepath.Join(dir, "settings.json")
	var s gemrouteState
	s.base, s.hasBase = edit.GetEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
	s.key, s.hasKey = edit.GetEnvFile(envPath, "GEMINI_API_KEY")
	s.auth, s.hasAuth = edit.GetJSON(path, "security.auth.selectedType")
	s.model, s.hasModel = edit.GetJSON(path, "model.name")
	return s
}

func gemrouteFieldSet(t *testing.T, a *Agent, key, value string) error {
	t.Helper()
	f := a.Lookup(key)
	if f == nil {
		t.Fatalf("no field %q", key)
	}
	return f.Set(value)
}

func TestGemrouteRoutesToGateway(t *testing.T) {
	a, dir := gemrouteSetup(t,
		"GEMINI_API_KEY=AIza-own\n",
		`{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`)

	if err := gemrouteFieldSet(t, a, "model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	s := gemrouteRead(t, dir)
	if s.base != gateway.URL() {
		t.Errorf("GOOGLE_GEMINI_BASE_URL = %q, want %q", s.base, gateway.URL())
	}
	if strings.HasSuffix(s.base, "/v1") {
		t.Errorf("base URL %q must not carry /v1", s.base)
	}
	if s.key != gateway.Token {
		t.Errorf("GEMINI_API_KEY = %q, want gateway token", s.key)
	}
	if s.auth != "gemini-api-key" {
		t.Errorf("selectedType = %q, want gemini-api-key", s.auth)
	}
	if s.model != "fake/m1" {
		t.Errorf("model.name = %q, want fake/m1", s.model)
	}
	if got := a.Field("provider").Get(); got != "dial" {
		t.Errorf("auth shows %q while routed, want dial", got)
	}
	found := false
	for _, o := range a.Field("provider").Options(nil) {
		if o.Value == "dial" && o.Label == "dial" && o.Note != "" {
			found = true
		}
	}
	if !found {
		t.Error("auth options do not offer dial while routed")
	}
}

func TestGemrouteKeepsOriginalsAcrossSwitches(t *testing.T) {
	a, dir := gemrouteSetup(t,
		"GEMINI_API_KEY=AIza-own\n",
		`{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`)

	if err := gemrouteFieldSet(t, a, "model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	// second catalog model while already routed must not overwrite the originals
	if err := gemrouteFieldSet(t, a, "model", "fake/m2"); err != nil {
		t.Fatal(err)
	}
	if got := gemrouteRead(t, dir).model; got != "fake/m2" {
		t.Fatalf("model.name = %q, want fake/m2", got)
	}
	if v := stashLoad()["gemini.auth"]; v != "oauth-personal" {
		t.Errorf("stashed auth = %q, want oauth-personal", v)
	}
	if v := stashLoad()["gemini.api_key"]; v != "AIza-own" {
		t.Errorf("stashed key = %q, want AIza-own", v)
	}
	if v := stashLoad()["gemini.model"]; v != "gemini-2.5-pro" {
		t.Errorf("stashed model = %q, want gemini-2.5-pro", v)
	}

	// back to a Google model: originals come back exactly
	if err := gemrouteFieldSet(t, a, "model", "gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	s := gemrouteRead(t, dir)
	if s.hasBase {
		t.Errorf("GOOGLE_GEMINI_BASE_URL left behind: %q", s.base)
	}
	if s.key != "AIza-own" {
		t.Errorf("GEMINI_API_KEY = %q, want AIza-own", s.key)
	}
	if s.auth != "oauth-personal" {
		t.Errorf("selectedType = %q, want oauth-personal", s.auth)
	}
	if s.model != "gemini-2.5-flash" {
		t.Errorf("model.name = %q, want gemini-2.5-flash", s.model)
	}
	if len(stashLoad()) != 0 {
		t.Errorf("stash not drained after restore: %v", stashLoad())
	}
}

func TestGemrouteRestoresExactly(t *testing.T) {
	a, dir := gemrouteSetup(t,
		"GOOGLE_GEMINI_BASE_URL=https://proxy.example/x\nGEMINI_API_KEY=AIza-own\n",
		`{"security":{"auth":{"selectedType":"gemini-api-key"}},"model":{"name":"gemini-2.5-pro"}}`)

	if err := gemrouteFieldSet(t, a, "model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	// "default" while routed puts everything back
	if err := gemrouteFieldSet(t, a, "model", ""); err != nil {
		t.Fatal(err)
	}
	s := gemrouteRead(t, dir)
	if s.base != "https://proxy.example/x" {
		t.Errorf("base = %q, want original", s.base)
	}
	if s.key != "AIza-own" {
		t.Errorf("key = %q, want original", s.key)
	}
	if s.auth != "gemini-api-key" {
		t.Errorf("selectedType = %q, want gemini-api-key", s.auth)
	}
	if s.model != "gemini-2.5-pro" {
		t.Errorf("model.name = %q, want gemini-2.5-pro", s.model)
	}
	if got := a.Field("provider").Get(); got != "custom" {
		t.Errorf("auth shows %q after restore, want custom (the user's own base URL)", got)
	}
}

func TestGemrouteRemovesWhatWasAbsent(t *testing.T) {
	a, dir := gemrouteSetup(t, "", "{}")

	if err := gemrouteFieldSet(t, a, "model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := gemrouteFieldSet(t, a, "model", "default"); err != nil {
		t.Fatal(err)
	}
	s := gemrouteRead(t, dir)
	if s.hasBase || s.hasKey || s.hasAuth || s.hasModel {
		t.Errorf("settings left behind after undo: %+v", s)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if strings.Contains(string(raw), "GOOGLE_GEMINI_BASE_URL") {
		t.Errorf(".env still mentions GOOGLE_GEMINI_BASE_URL: %q", raw)
	}
	if got := a.Field("provider").Get(); got != "google" {
		t.Errorf("auth shows %q after undo, want google", got)
	}
}

func TestGemrouteAuthChoiceUndoesRoute(t *testing.T) {
	a, dir := gemrouteSetup(t,
		"GEMINI_API_KEY=AIza-own\n",
		`{"security":{"auth":{"selectedType":"oauth-personal"}}}`)

	if err := gemrouteFieldSet(t, a, "model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := gemrouteFieldSet(t, a, "auth", "api-key"); err != nil {
		t.Fatal(err)
	}
	s := gemrouteRead(t, dir)
	if s.hasBase {
		t.Errorf("route not undone: base = %q", s.base)
	}
	if s.key != "AIza-own" {
		t.Errorf("key = %q, want original AIza-own", s.key)
	}
	if s.auth != "gemini-api-key" {
		t.Errorf("selectedType = %q, want gemini-api-key", s.auth)
	}
	if v := stashLoad()["gemini.auth"]; v != "" {
		t.Errorf("stash still holds auth %q after undo", v)
	}
}

func TestGemrouteManualDialNeedsRoute(t *testing.T) {
	a, dir := gemrouteSetup(t, "", "{}")

	err := gemrouteFieldSet(t, a, "auth", "dial")
	if err == nil {
		t.Fatal("setting dial without a route should fail")
	}
	if !strings.Contains(err.Error(), "dial gemini model") {
		t.Errorf("error should point at choosing a dial model, got %q", err)
	}
	if s := gemrouteRead(t, dir); s.hasBase || s.hasAuth {
		t.Errorf("failed dial choice still changed config: %+v", s)
	}

	if err := gemrouteFieldSet(t, a, "model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := gemrouteFieldSet(t, a, "auth", "dial"); err != nil {
		t.Errorf("setting dial while routed should be a no-op, got %v", err)
	}
	if got := gemrouteRead(t, dir).model; got != "fake/m1" {
		t.Errorf("model.name = %q after dial no-op, want fake/m1", got)
	}
}

func TestGemrouteLabelsMatchKeys(t *testing.T) {
	a, dir := gemrouteSetup(t, "", "{}")

	if a.Lookup("auth") == nil || a.Lookup("auth") != a.Lookup("provider") {
		t.Fatal(`Lookup("auth") should be the field keyed "provider"`)
	}
	if a.Lookup("model") != a.Field("model") || a.Lookup("provider") != a.Field("provider") {
		t.Fatal("keys should resolve to the same fields")
	}
	if a.Lookup("nope") != nil {
		t.Fatal("unknown name should not resolve")
	}

	// setting by label and by key land in the same place
	if err := gemrouteFieldSet(t, a, "auth", "google"); err != nil {
		t.Fatal(err)
	}
	byLabel := gemrouteRead(t, dir).auth
	if err := gemrouteFieldSet(t, a, "provider", "vertex"); err != nil {
		t.Fatal(err)
	}
	byKey := gemrouteRead(t, dir).auth
	if byLabel != "oauth-personal" || byKey != "vertex-ai" {
		t.Errorf("label/key set gave %q / %q", byLabel, byKey)
	}
}

func TestGemrouteModelOptionsAreGrouped(t *testing.T) {
	a, _ := gemrouteSetup(t, "", "{}")
	sawDial := false
	for _, o := range a.Field("model").Options(nil) {
		if o.Value == "fake/m1" {
			sawDial = true
			if o.Group != "Fake" {
				t.Errorf("catalog option grouped as %q, want Fake", o.Group)
			}
		}
		if !strings.Contains(o.Value, "/") && o.Group != "Gemini CLI" {
			t.Errorf("native option %q grouped as %q, want Gemini CLI", o.Value, o.Group)
		}
	}
	if !sawDial {
		t.Error("catalog model fake/m1 missing from model options")
	}
}
