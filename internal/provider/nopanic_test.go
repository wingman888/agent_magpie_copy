package provider

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Empty, unknown or odd input must not panic in the group, catalog, affinity
// and version code paths.

func TestNoPanicEmptyConfig(t *testing.T) {
	accIsolate(t)
	_ = Catalog()
	_ = accIDs()
	for _, id := range []string{"", "  ", "group/", "group/nope", "nope", "GROUP/"} {
		if _, ms, ok := FindGroup(id); ok || len(ms) != 0 {
			t.Fatalf("FindGroup(%q) found something: ok=%v members=%d", id, ok, len(ms))
		}
	}
	if err := SaveGroup(Group{}); err == nil {
		t.Fatal("SaveGroup(Group{}) should refuse an empty group")
	}
	if err := DeleteGroup(""); err == nil {
		t.Fatal("DeleteGroup(\"\") should report no group")
	}
	if err := DeleteGroup("nope"); err == nil {
		t.Fatal("DeleteGroup of an unknown group should report it")
	}
	_ = ShowGroup("nope") // no group to show: no panic
	_ = ShowGroup("")
	if err := SetAffinity("nope", "session"); err == nil {
		t.Fatal("SetAffinity on an unknown provider should fail")
	}
	if got := CanonicalModel(""); got != "" {
		t.Fatalf("CanonicalModel(\"\") = %q", got)
	}
}

// A hand-edited file with an empty group, a group with no members and a
// provider without any account or key must not panic the catalog.
func TestNoPanicHandEditedGroups(t *testing.T) {
	accIsolate(t)
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	src := `{"providers":[{"id":"bare","name":"Bare"}],"groups":[{"id":"","members":[]},{"id":"empty","members":[]},{"id":"odd","members":["/","group/x","bare/"]}]}`
	if err := os.WriteFile(Path(), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = Catalog()
	for _, id := range []string{"group/", "group/empty", "group/odd"} {
		if _, _, ok := FindGroup(id); ok {
			t.Fatalf("FindGroup(%q) should not find a group with no ready member", id)
		}
	}
	if err := SetAffinity("bare", "bogus"); err != nil {
		t.Fatalf("SetAffinity with an unknown affinity value: %v", err)
	}
}

// The Codex version lookup is safe with no CLI and no cache, and asking
// again within the TTL does not run the CLI again.
func TestNoPanicCodexVersionNoCLI(t *testing.T) {
	accIsolate(t)
	codexVersionMu.Lock()
	codexVersion, codexVersionAt = "", time.Time{}
	codexVersionMu.Unlock()
	calls := 0
	old := codexVersionFromCLI
	codexVersionFromCLI = func() (string, error) { calls++; return "", os.ErrNotExist }
	t.Cleanup(func() { codexVersionFromCLI = old })
	first := codexClientVersionNow()
	second := codexClientVersionNow()
	if first != codexClientVersion || second != first {
		t.Fatalf("version without a CLI = %q then %q, want %q", first, second, codexClientVersion)
	}
	if calls != 1 {
		t.Fatalf("CLI asked %d times within the TTL, want 1", calls)
	}
}
