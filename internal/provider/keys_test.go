package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// keysEnv isolates the config in a temp HOME with no signed-in accounts.
func keysEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

// writeLegacy writes a pre-multi-key config: one provider with one key.
func writeLegacy(t *testing.T, key string) {
	t.Helper()
	cfg := map[string]any{"providers": []map[string]any{{
		"id": "relay", "name": "Relay", "chat": "https://relay.example/v1", "key": key,
	}}}
	b, _ := json.Marshal(cfg)
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func rawConfig(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustFind(t *testing.T) Provider {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

// order returns the names of the keys in try order, with "*" marking the
// front one and "-" the disabled ones, e.g. "*A,B-".
func order(p Provider) string {
	var parts []string
	for _, k := range p.KeyList() {
		s := k.Name
		if k.Active {
			s = "*" + s
		}
		if !k.On {
			s += "-"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ",")
}

func TestLegacyConfigSingleKey(t *testing.T) {
	keysEnv(t)
	writeLegacy(t, "sk-legacy-1234567890")
	p := mustFind(t)
	kl := p.KeyList()
	if len(kl) != 1 || !kl[0].Active || !kl[0].On || kl[0].Name != "" {
		t.Fatalf("legacy KeyList = %+v", kl)
	}
	if got := p.KeysOn(); len(got) != 1 || got[0].Key != "sk-legacy-1234567890" {
		t.Fatalf("legacy KeysOn = %+v", got)
	}
	if strings.Contains(rawConfig(t), "keys") || strings.Contains(rawConfig(t), "keyName") {
		t.Fatalf("untouched legacy config gained new fields: %s", rawConfig(t))
	}
}

func TestAddKeyToLegacyProvider(t *testing.T) {
	keysEnv(t)
	writeLegacy(t, "sk-legacy-1234567890")
	if err := AddKey("relay", "团队", "  sk-team-abcdefghij  "); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t)
	if got := order(p); got != "*,团队" {
		// the legacy key has no name, so only the second is named
		t.Fatalf("order = %q", got)
	}
	kl := p.KeyList()
	if !kl[0].Active || !kl[0].On || !kl[1].On || kl[1].Active || kl[1].Name != "团队" {
		t.Fatalf("KeyList = %+v", kl)
	}
	if kl[1].ID == kl[0].ID || kl[0].ID == "" {
		t.Fatalf("ids not distinct: %q %q", kl[0].ID, kl[1].ID)
	}
	// trimmed key is what is stored
	if got := p.KeysOn()[1].Key; got != "sk-team-abcdefghij" {
		t.Fatalf("stored key = %q", got)
	}
	// duplicate and empty are rejected, and nothing changes
	before := rawConfig(t)
	if err := AddKey("relay", "again", "sk-team-abcdefghij"); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if err := AddKey("relay", "again", "sk-legacy-1234567890"); err == nil {
		t.Fatal("duplicate of front key accepted")
	}
	if err := AddKey("relay", "x", "   "); err == nil {
		t.Fatal("empty key accepted")
	}
	if rawConfig(t) != before {
		t.Fatal("rejected AddKey changed the config")
	}
}

func TestKeyListNeverLeaksFullKey(t *testing.T) {
	keysEnv(t)
	const full = "sk-very-secret-key-value-9876"
	writeLegacy(t, full)
	if err := AddKey("relay", "团队", "sk-another-secret-key-1111"); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t)
	for _, k := range p.KeyList() {
		if strings.Contains(k.Masked, "very-secret") || k.Masked == full {
			t.Fatalf("masked value leaks key: %q", k.Masked)
		}
		if k.Masked == "" {
			t.Fatal("empty masked value")
		}
	}
	ref := p.KeyList()[0].ID
	if err := UseKey("relay", "nope"); err == nil || strings.Contains(err.Error(), "very-secret") {
		t.Fatalf("unknown ref error = %v", err)
	}
	// error from a rejected operation must not carry the key either
	must(t, SetKeyOn("relay", ref, false))
	if err := SetKeyOn("relay", p.KeyList()[1].ID, false); err == nil {
		t.Fatal("disabled the last enabled key")
	}
	if err := RemoveKey("relay", mustFind(t).KeyList()[0].ID); err == nil || strings.Contains(err.Error(), "sk-") {
		t.Fatalf("error leaks or absent: %v", err)
	}
}

func TestUseKey(t *testing.T) {
	keysEnv(t)
	writeLegacy(t, "sk-a-1111111111")
	must(t, AddKey("relay", "B", "sk-b-2222222222"))
	must(t, AddKey("relay", "C", "sk-c-3333333333"))
	p := mustFind(t)
	must(t, RenameKey("relay", p.KeyList()[0].ID, "A"))
	p = mustFind(t)
	if got := order(p); got != "*A,B,C" {
		t.Fatalf("start order = %q", got)
	}
	cRef := p.KeyList()[2].ID
	must(t, UseKey("relay", cRef))
	// old front stays enabled, right after the new front
	if got := order(mustFind(t)); got != "*C,A,B" {
		t.Fatalf("after UseKey = %q", got)
	}
	before := rawConfig(t)
	must(t, UseKey("relay", cRef)) // already front: no change
	if rawConfig(t) != before {
		t.Fatal("UseKey on the front key changed the config")
	}
	if err := UseKey("relay", "deadbeef00"); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestSetKeyOnBoundaries(t *testing.T) {
	keysEnv(t)
	writeLegacy(t, "sk-a-1111111111")
	must(t, AddKey("relay", "B", "sk-b-2222222222"))
	p := mustFind(t)
	aRef, bRef := p.KeyList()[0].ID, p.KeyList()[1].ID
	must(t, RenameKey("relay", aRef, "A"))

	// disable the front: B takes over, A is kept off right after it
	must(t, SetKeyOn("relay", aRef, false))
	p = mustFind(t)
	if got := order(p); got != "*B,A-" {
		t.Fatalf("after disabling front = %q", got)
	}
	if kl := p.KeyList(); !kl[0].Active || kl[0].Name != "B" || kl[1].On {
		t.Fatalf("KeyList after disabling front = %+v", kl)
	}
	if got := p.KeysOn(); len(got) != 1 || got[0].Name != "B" {
		t.Fatalf("KeysOn = %+v", got)
	}

	// B is now the only enabled key: neither disabling nor removing it is allowed
	before := rawConfig(t)
	if err := SetKeyOn("relay", bRef, false); err == nil {
		t.Fatal("disabled the last enabled key")
	}
	if err := RemoveKey("relay", bRef); err == nil {
		t.Fatal("removed the last enabled key")
	}
	if rawConfig(t) != before {
		t.Fatal("rejected operation changed the config")
	}

	// re-enable A, then the old front can be removed and the rest keeps its name
	must(t, SetKeyOn("relay", aRef, true))
	must(t, RemoveKey("relay", aRef))
	p = mustFind(t)
	if kl := p.KeyList(); len(kl) != 1 || kl[0].ID != bRef || kl[0].Name != "B" || !kl[0].Active {
		t.Fatalf("after removing old front = %+v", kl)
	}
}

func TestSetKeyOnNonFrontAndReenable(t *testing.T) {
	keysEnv(t)
	writeLegacy(t, "sk-a-1111111111")
	must(t, AddKey("relay", "B", "sk-b-2222222222"))
	must(t, AddKey("relay", "C", "sk-c-3333333333"))
	p := mustFind(t)
	bRef := p.KeyList()[1].ID
	must(t, SetKeyOn("relay", bRef, false))
	if got := len(mustFind(t).KeysOn()); got != 2 {
		t.Fatalf("KeysOn after disabling B = %d, want 2", got)
	}
	must(t, SetKeyOn("relay", bRef, true))
	if got := len(mustFind(t).KeysOn()); got != 3 {
		t.Fatalf("KeysOn after re-enabling B = %d, want 3", got)
	}
	if err := SetKeyOn("relay", "nope", true); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestRemoveKeyFrontHandsOver(t *testing.T) {
	keysEnv(t)
	writeLegacy(t, "sk-a-1111111111")
	must(t, AddKey("relay", "B", "sk-b-2222222222"))
	must(t, AddKey("relay", "C", "sk-c-3333333333"))
	p := mustFind(t)
	must(t, SetKeyOn("relay", p.KeyList()[1].ID, false)) // B off: order A, B-, C
	p = mustFind(t)
	aRef := p.KeyList()[0].ID
	must(t, RemoveKey("relay", aRef))
	p = mustFind(t)
	// next enabled key (C) is the front; disabled B is kept
	if kl := p.KeyList(); len(kl) != 2 || kl[0].Name != "C" || !kl[0].Active || kl[1].Name != "B" || kl[1].On {
		t.Fatalf("after removing front = %+v", kl)
	}
	if err := RemoveKey("relay", "nope"); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestRenameKey(t *testing.T) {
	keysEnv(t)
	writeLegacy(t, "sk-a-1111111111")
	must(t, AddKey("relay", "B", "sk-b-2222222222"))
	p := mustFind(t)
	aRef, bRef := p.KeyList()[0].ID, p.KeyList()[1].ID
	must(t, RenameKey("relay", aRef, "  个人  "))
	must(t, RenameKey("relay", bRef, ""))
	p = mustFind(t)
	if kl := p.KeyList(); kl[0].Name != "个人" || kl[1].Name != "" {
		t.Fatalf("after rename = %+v", kl)
	}
	must(t, RenameKey("relay", aRef, ""))
	if kl := mustFind(t).KeyList(); kl[0].Name != "" {
		t.Fatalf("clearing name failed: %+v", kl)
	}
	if err := RenameKey("relay", "nope", "x"); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestSignedInAccountRejectsAddKey(t *testing.T) {
	signIn(t)
	accs := Accounts()
	if len(accs) == 0 {
		t.Fatal("no signed-in account to test against")
	}
	if err := AddKey(accs[0].ID, "x", "sk-whatever-123456"); err == nil {
		t.Fatal("AddKey accepted a signed-in account")
	}
}

func TestNoKeyProviderErrors(t *testing.T) {
	keysEnv(t)
	if err := AddKey("missing", "x", "sk-1234567890"); err == nil {
		t.Fatal("AddKey accepted an unknown provider")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
