package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// UseKey on a provider with no current key makes the chosen key current and
// leaves no empty entry behind in Keys.
func TestAccUseKeyWithoutCurrentKeyLeavesNoEmptyEntry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1",
		Keys: []KeyEntry{{Name: "a", Key: "sk-a"}, {Name: "b", Key: "sk-b", Protocol: Chat}}}); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", KeyRef("sk-b")); err != nil {
		t.Fatal(err)
	}
	p := accTestStoredRelay(t)
	if p.Key != "sk-b" || p.KeyName != "b" || p.KeyProtocol != Chat {
		t.Fatalf("current key after UseKey: %+v", p)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-a" || p.Keys[0].Name != "a" {
		t.Fatalf("keys after UseKey: %+v, want only sk-a", p.Keys)
	}
	for _, k := range p.Keys {
		if k.Key == "" {
			t.Fatalf("an empty key entry was kept: %+v", p.Keys)
		}
	}
}

// IconDir falls back to the temp directory when there is no home directory,
// and never yields a relative path.
func TestAccIconDirFallsBackWithoutHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	dir := IconDir()
	if !filepath.IsAbs(dir) {
		t.Fatalf("IconDir without home is relative: %q", dir)
	}
	want := filepath.Join(os.TempDir(), "magpie", "icons")
	if dir != want {
		t.Fatalf("IconDir without home = %q, want %q", dir, want)
	}
	if !strings.HasPrefix(dir, os.TempDir()) {
		t.Fatalf("IconDir %q is not under the temp dir", dir)
	}
}

// LoginUsage accepts a nil context without panicking.
func TestAccLoginUsageAcceptsNilContext(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	out := LoginUsage(nil, "gemini") //nolint:staticcheck // nil on purpose: checks the guard
	if out == nil || len(out) != 0 {
		t.Fatalf("unsupported agent with nil ctx: %v", out)
	}
}
