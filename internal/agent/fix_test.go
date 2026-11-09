package agent

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

// fixEnv points HOME and the XDG dirs at a fresh temp tree.
func fixEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdgconfig"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdgcache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

// fixJWT builds an unsigned JWT carrying the given claims.
func fixJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// TestFix_CopilotCLIOffersAuto: the Copilot CLI picker keeps its own auto
// entry first, although the catalog's built-in list does not carry it.
func TestFix_CopilotCLIOffersAuto(t *testing.T) {
	fixEnv(t)
	opts := copilot(t.TempDir()).Fields[0].Options(nil)
	if len(opts) == 0 || opts[0].Value != "auto" {
		t.Fatalf("first copilot option = %+v, want auto", opts)
	}
	n := 0
	for _, o := range opts {
		if o.Value == "auto" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("auto offered %d times, want once", n)
	}
	for _, m := range catalog.Builtin("copilot") {
		if m.ID == "auto" {
			t.Error("catalog built-in copilot list must not contain auto")
		}
	}
}

// TestFix_CodexRoutesHideOwnLogin: with a ChatGPT login, the catalog holds
// codex/… models, but the list Codex is given leaves them out.
func TestFix_CodexRoutesHideOwnLogin(t *testing.T) {
	home := fixEnv(t)
	id := fixJWT(map[string]any{"email": "dev@example.com"})
	access := fixJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	doc := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": id, "access_token": access, "refresh_token": "rt", "account_id": "acct-1"}}
	b, _ := json.Marshal(doc)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	sawOwn := false
	for _, m := range dialModels() {
		if strings.HasPrefix(m.ID, "codex/") {
			sawOwn = true
		}
	}
	if !sawOwn {
		t.Fatal("setup: expected codex/… models in the dial catalog")
	}
	for _, m := range codexRoutes() {
		if strings.HasPrefix(m.ID, "codex/") {
			t.Errorf("codexRoutes offers its own login model %q", m.ID)
		}
	}
}

// TestFix_ModelFieldPicksModelLabel: the model field is the one labelled
// "model" wherever it sits; otherwise the first field.
func TestFix_ModelFieldPicksModelLabel(t *testing.T) {
	fixEnv(t)
	a := &Agent{Fields: []Field{{Key: "provider", Label: "auth"}, {Key: "model", Label: "model"}}}
	if f := a.ModelField(); f == nil || f.Label != "model" {
		t.Errorf("ModelField = %+v, want the model field", f)
	}
	b := &Agent{Fields: []Field{{Key: "large", Label: "large"}}}
	if f := b.ModelField(); f == nil || f.Key != "large" {
		t.Errorf("ModelField fallback = %+v, want the first field", f)
	}
	if f := (&Agent{}).ModelField(); f != nil {
		t.Errorf("ModelField without fields = %+v, want nil", f)
	}
	if f := gemini(t.TempDir()).ModelField(); f == nil || f.Label != "model" {
		t.Errorf("gemini ModelField = %+v, want the model field", f)
	}
}
