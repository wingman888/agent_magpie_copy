package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestViaDialAccounts(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	os.MkdirAll(filepath.Join(h, ".codex"), 0o755)
	os.WriteFile(filepath.Join(h, ".codex", "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"t","id_token":"a.eyJlbWFpbCI6ImFAYi5jIn0.c"}}`), 0o600)
	os.MkdirAll(filepath.Join(h, ".config", "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(h, ".config", "github-copilot", "apps.json"), []byte(`{"github.com:x":{"user":"octo","oauth_token":"o"}}`), 0o600)

	has := func(opts []Option, prefix string) (Option, bool) {
		for _, o := range opts {
			if strings.HasPrefix(o.Value, prefix) {
				return o, true
			}
		}
		return Option{}, false
	}
	all := viaDial("")
	if o, ok := has(all, "codex/"); !ok || o.Note != "a@b.c · via dial" {
		t.Fatalf("codex option: %+v", o)
	}
	if o, ok := has(all, "copilot/"); !ok || o.Note != "octo · via dial" {
		t.Fatalf("copilot option: %+v", o)
	}
	own := viaDialExcept("", "codex/")
	if _, ok := has(own, "codex/"); ok {
		t.Fatal("codex lists itself")
	}
	if _, ok := has(own, "copilot/"); !ok {
		t.Fatal("copilot missing")
	}
}
