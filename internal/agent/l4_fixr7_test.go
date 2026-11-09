package agent

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// l4FixR7JWT builds an unsigned JWT with the given payload.
func l4FixR7JWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(b) + ".sig"
}

// The Codex picker must not list Codex's own account models, while other
// agents' catalogs still do.
func TestL4FixR7CodexPickerSkipsItsOwnAccountModels(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	l4GmTWrite(t, filepath.Join(root, "config", "dial", "providers.json"),
		`{"providers":[{"id":"fake","name":"Fake","key":"sk-fake","chat":"http://127.0.0.1:1/v1","models":["m1"]}]}`)
	idTok := l4FixR7JWT(t, map[string]any{"email": "dev@example.com"})
	accTok := l4FixR7JWT(t, map[string]any{"exp": 4102444800}) // year 2100
	l4GmTWrite(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"chatgpt","tokens":{"id_token":"`+idTok+`","access_token":"`+accTok+
			`","refresh_token":"rt","account_id":"acct-1"}}`)

	own, fake := 0, 0
	for _, m := range l4DialModelsExcept(provider.CodexID) {
		if strings.HasPrefix(m.ID, "codex/") {
			own++
		}
		if m.ID == "fake/m1" {
			fake++
		}
	}
	if own != 0 || fake != 1 {
		t.Fatalf("codex picker: own codex models = %d, fake/m1 = %d", own, fake)
	}
	all := 0
	for _, m := range dialModels() {
		if strings.HasPrefix(m.ID, "codex/") {
			all++
		}
	}
	if all == 0 {
		t.Fatal("other agents should still see codex account models")
	}
}

// Labels are accepted as field names for Gemini only; other agents keep
// key-only lookup.
func TestL4FixR7LabelLookupIsGeminiOnly(t *testing.T) {
	home := t.TempDir()
	g := gemini(home)
	if g.Field("auth") == nil {
		t.Fatal("gemini should find its auth field by label")
	}
	c := codex(home)
	for _, f := range c.Fields {
		if f.Label != "" && f.Label != f.Key && c.Field(f.Label) != nil {
			t.Fatalf("codex resolved label %q by label lookup", f.Label)
		}
	}
}
