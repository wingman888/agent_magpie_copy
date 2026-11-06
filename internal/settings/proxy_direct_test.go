package settings

import "testing"

// "direct" is accepted in any letter case and stored in lower case.
func TestProxyDirectCaseInsensitive(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, in := range []string{"Direct", "DIRECT", " direct "} {
		if err := Save(Settings{Proxy: in}); err != nil {
			t.Fatalf("Save(%q): %v", in, err)
		}
		if got := Load().Proxy; got != "direct" {
			t.Fatalf("Save(%q) stored %q, want \"direct\"", in, got)
		}
	}
}
