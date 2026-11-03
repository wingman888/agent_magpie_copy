package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// isolateConfig points HOME and the config dir at a fresh temp dir, so no
// real sign-in or providers.json leaks into the test.
func isolateConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

const (
	keyOld  = "sk-old-aaaaaaaaaaaaaaaa"
	keyTeam = "sk-team-bbbbbbbbbbbbbbbb"
	keyFree = "sk-free-cccccccccccccccc"
)

// legacyRelay writes a pre-multi-key providers.json with one key.
func legacyRelay(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"providers":[{"id":"relay","name":"Relay","key":"` + keyOld + `","chat":"https://relay.example/v1"}]}`
	if err := os.WriteFile(Path(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustFind(t *testing.T, id string) Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

// names returns the key names in attempt order, with "*" marking the front
// and "-" marking an off key, for compact assertions.
func names(p Provider) []string {
	var out []string
	for _, k := range p.KeyList() {
		s := k.Name
		if k.Active {
			s = "*" + s
		}
		if !k.On {
			s = "-" + s
		}
		out = append(out, s)
	}
	return out
}

func TestLegacySingleKeyUnchanged(t *testing.T) {
	isolateConfig(t)
	legacyRelay(t)
	p := mustFind(t, "relay")
	kl := p.KeyList()
	if len(kl) != 1 || !kl[0].Active || !kl[0].On || kl[0].Masked != Mask(keyOld) {
		t.Fatalf("legacy key list: %+v", kl)
	}
	if p.Key != keyOld || p.KeyName != "" || len(p.Keys) != 0 {
		t.Fatalf("legacy fields changed: %+v", p)
	}
}

// Acceptance 1: adding a named second key to a legacy provider.
func TestAddKeyToLegacyProvider(t *testing.T) {
	isolateConfig(t)
	legacyRelay(t)
	if err := AddKey("relay", "  团队  ", "  "+keyTeam+"  "); err != nil {
		t.Fatal(err)
	}
	kl := mustFind(t, "relay").KeyList()
	if len(kl) != 2 {
		t.Fatalf("want 2 keys, got %+v", kl)
	}
	if !kl[0].Active || !kl[0].On || kl[0].Name != "" {
		t.Fatalf("front key: %+v", kl[0])
	}
	if kl[1].Active || !kl[1].On || kl[1].Name != "团队" {
		t.Fatalf("added key: %+v", kl[1])
	}
	if kl[1].ID != keyID(keyTeam) {
		t.Fatalf("id: %q", kl[1].ID)
	}
}

func TestAddKeyRejects(t *testing.T) {
	isolateConfig(t)
	legacyRelay(t)
	if err := AddKey("relay", "x", "   "); err == nil {
		t.Fatal("empty key accepted")
	}
	if err := AddKey("relay", "x", keyOld); err == nil {
		t.Fatal("duplicate of front key accepted")
	}
	if err := AddKey("relay", "x", " "+keyOld+" "); err == nil {
		t.Fatal("duplicate with spaces accepted")
	}
	if err := AddKey("nope", "x", keyTeam); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if err := AddKey("relay", "team", keyTeam); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "again", keyTeam); err == nil {
		t.Fatal("duplicate of added key accepted")
	}
	if n := len(mustFind(t, "relay").KeyList()); n != 2 {
		t.Fatalf("rejected adds changed the list: %d keys", n)
	}
}

// Acceptance 2: no full key in listings or errors.
func TestFullKeysNeverShown(t *testing.T) {
	isolateConfig(t)
	legacyRelay(t)
	if err := AddKey("relay", "team", keyTeam); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t, "relay")
	b, err := json.Marshal(p.KeyList())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{keyOld, keyTeam} {
		if strings.Contains(string(b), k) {
			t.Fatalf("KeyList leaks %q: %s", k, b)
		}
	}
	errs := []error{
		AddKey("relay", "dup", keyTeam),
		SetKeyOn("relay", "ffffffffffff", false),
		RemoveKey("relay", "ffffffffffff"),
		UseKey("relay", "ffffffffffff"),
		RenameKey("relay", "ffffffffffff", "x"),
		AddKey("relay", "dup", keyOld),
	}
	for _, err := range errs {
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), keyOld) || strings.Contains(err.Error(), keyTeam) {
			t.Fatalf("error leaks a key: %v", err)
		}
	}
}

// Acceptance 6: UseKey on the second key, then on the front key.
func TestUseKeyMovesToFront(t *testing.T) {
	isolateConfig(t)
	legacyRelay(t)
	AddKey("relay", "个人", keyTeam)
	AddKey("relay", "团队", keyFree)
	if err := UseKey("relay", keyID(keyFree)); err != nil {
		t.Fatal(err)
	}
	if got := names(mustFind(t, "relay")); !slices.Equal(got, []string{"*团队", "", "个人"}) {
		t.Fatalf("after UseKey: %v", got)
	}
	before := names(mustFind(t, "relay"))
	if err := UseKey("relay", keyID(keyFree)); err != nil {
		t.Fatal(err)
	}
	if got := names(mustFind(t, "relay")); !slices.Equal(got, before) {
		t.Fatalf("UseKey on front changed order: %v -> %v", before, got)
	}
}

// Acceptance 7: disabling the front key, the last-on guard, deleting the old front.
func TestTurnOffAndRemoveFront(t *testing.T) {
	isolateConfig(t)
	legacyRelay(t)
	AddKey("relay", "个人", keyTeam)
	// the legacy key is the front; name it so it can be told apart
	if err := RenameKey("relay", keyID(keyOld), "主力"); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", keyID(keyOld), false); err != nil {
		t.Fatal(err)
	}
	if got := names(mustFind(t, "relay")); !slices.Equal(got, []string{"*个人", "-主力"}) {
		t.Fatalf("after turning off front: %v", got)
	}
	// only one key is on now: turning it off or removing it must fail, config unchanged
	before, _ := os.ReadFile(Path())
	if err := SetKeyOn("relay", keyID(keyTeam), false); err == nil {
		t.Fatal("turned off the last key on")
	}
	if err := RemoveKey("relay", keyID(keyTeam)); err == nil {
		t.Fatal("removed the last key on")
	}
	if after, _ := os.ReadFile(Path()); string(after) != string(before) {
		t.Fatal("rejected change still rewrote the config")
	}
	// bring the other key back, then the old front can go
	if err := SetKeyOn("relay", keyID(keyOld), true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", keyID(keyTeam)); err != nil {
		t.Fatal(err)
	}
	if got := names(mustFind(t, "relay")); !slices.Equal(got, []string{"*主力"}) {
		t.Fatalf("after removing front: %v", got)
	}
	if p := mustFind(t, "relay"); p.Key != keyOld || p.KeyName != "主力" {
		t.Fatalf("remaining key or name lost: %+v", p)
	}
}

func TestRemoveFrontHandsOver(t *testing.T) {
	isolateConfig(t)
	legacyRelay(t)
	AddKey("relay", "个人", keyTeam)
	AddKey("relay", "免费", keyFree)
	// the free key is off; removing the front must pick the on key, not it
	SetKeyOn("relay", keyID(keyFree), false)
	if err := RemoveKey("relay", keyID(keyOld)); err != nil {
		t.Fatal(err)
	}
	if got := names(mustFind(t, "relay")); !slices.Equal(got, []string{"*个人", "-免费"}) {
		t.Fatalf("after removing front: %v", got)
	}
}

func TestSetKeyOnNonFrontKeepsFront(t *testing.T) {
	isolateConfig(t)
	legacyRelay(t)
	AddKey("relay", "个人", keyTeam)
	if err := SetKeyOn("relay", keyID(keyTeam), false); err != nil {
		t.Fatal(err)
	}
	if got := names(mustFind(t, "relay")); !slices.Equal(got, []string{"*", "-个人"}) {
		t.Fatalf("after turning off second: %v", got)
	}
	if err := SetKeyOn("relay", keyID(keyTeam), true); err != nil {
		t.Fatal(err)
	}
	if got := names(mustFind(t, "relay")); !slices.Equal(got, []string{"*", "个人"}) {
		t.Fatalf("after turning back on: %v", got)
	}
}

// Acceptance 9: renaming, clearing, and unknown refs everywhere.
func TestRenameAndUnknownRefs(t *testing.T) {
	isolateConfig(t)
	legacyRelay(t)
	AddKey("relay", "", keyTeam)
	team := keyID(keyTeam)
	if err := RenameKey("relay", team, "  团队  "); err != nil {
		t.Fatal(err)
	}
	if kl := mustFind(t, "relay").KeyList(); kl[1].Name != "团队" {
		t.Fatalf("rename: %+v", kl)
	}
	if err := RenameKey("relay", keyID(keyOld), "主力"); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("relay", keyID(keyOld), "   "); err != nil {
		t.Fatal(err)
	}
	if kl := mustFind(t, "relay").KeyList(); kl[0].Name != "" {
		t.Fatalf("clear front name: %+v", kl)
	}
	if err := RenameKey("relay", team, ""); err != nil {
		t.Fatal(err)
	}
	if kl := mustFind(t, "relay").KeyList(); kl[1].Name != "" {
		t.Fatalf("clear second name: %+v", kl)
	}
	for name, err := range map[string]error{
		"UseKey":     UseKey("relay", "ffffffffffff"),
		"SetKeyOn":   SetKeyOn("relay", "ffffffffffff", true),
		"RemoveKey":  RemoveKey("relay", "ffffffffffff"),
		"RenameKey":  RenameKey("relay", "ffffffffffff", "x"),
		"unknownPrv": UseKey("nope", team),
	} {
		if err == nil {
			t.Fatalf("%s: unknown ref or provider accepted", name)
		}
	}
	if n := len(mustFind(t, "relay").KeyList()); n != 2 {
		t.Fatalf("unknown refs changed the list: %d keys", n)
	}
}

// Acceptance 10: signed-in accounts have no keys.
func TestAddKeyRejectsAccount(t *testing.T) {
	signIn(t)
	if _, err := Find("codex"); err != nil {
		t.Fatalf("codex account missing: %v", err)
	}
	if err := AddKey("codex", "x", keyTeam); err == nil {
		t.Fatal("AddKey accepted a signed-in account")
	}
}

func TestKeysOnOrderAndKeyless(t *testing.T) {
	isolateConfig(t)
	legacyRelay(t)
	AddKey("relay", "off", keyTeam)
	AddKey("relay", "on", keyFree)
	SetKeyOn("relay", keyID(keyTeam), false)
	on := mustFind(t, "relay").KeysOn()
	if len(on) != 2 || on[0].Key != keyOld || on[1].Key != keyFree || on[1].Name != "on" {
		t.Fatalf("KeysOn: %+v", on)
	}
	// a provider with no key still gets one attempt
	if err := Save(Provider{ID: "local", Name: "Local", Chat: "http://localhost:11434/v1"}); err != nil {
		t.Fatal(err)
	}
	local := mustFind(t, "local")
	if len(local.KeyList()) != 0 {
		t.Fatalf("keyless KeyList: %+v", local.KeyList())
	}
	if on := local.KeysOn(); len(on) != 1 || on[0].Key != "" {
		t.Fatalf("keyless KeysOn: %+v", on)
	}
}

// The file keeps the new format: front key in key/keyName, the rest in keys,
// off ones marked.
func TestFileFormat(t *testing.T) {
	isolateConfig(t)
	legacyRelay(t)
	AddKey("relay", "个人", keyTeam)
	AddKey("relay", "免费", keyFree)
	RenameKey("relay", keyID(keyOld), "主力")
	SetKeyOn("relay", keyID(keyFree), false)
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Providers []struct {
			Key     string `json:"key"`
			KeyName string `json:"keyName"`
			Keys    []struct {
				Name string `json:"name"`
				Key  string `json:"key"`
				Off  bool   `json:"off"`
			} `json:"keys"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	got := f.Providers[0]
	if got.Key != keyOld || got.KeyName != "主力" || len(got.Keys) != 2 {
		t.Fatalf("file: %s", b)
	}
	if got.Keys[0].Name != "个人" || got.Keys[0].Key != keyTeam || got.Keys[0].Off {
		t.Fatalf("second key: %+v", got.Keys[0])
	}
	if got.Keys[1].Name != "免费" || !got.Keys[1].Off {
		t.Fatalf("off key: %+v", got.Keys[1])
	}
}

// A save that does not mention the keys keeps them; a save with a new front
// key replaces only the front.
func TestSaveKeepsKeys(t *testing.T) {
	isolateConfig(t)
	legacyRelay(t)
	AddKey("relay", "个人", keyTeam)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v2"}); err != nil {
		t.Fatal(err)
	}
	if got := names(mustFind(t, "relay")); !slices.Equal(got, []string{"*", "个人"}) {
		t.Fatalf("save without keys dropped them: %v", got)
	}
	if p := mustFind(t, "relay"); p.Key != keyOld {
		t.Fatalf("save without key changed front: %q", p.Key)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: keyTeam, Chat: "https://relay.example/v2"}); err != nil {
		t.Fatal(err)
	}
	if p := mustFind(t, "relay"); p.Key != keyTeam || len(p.KeyList()) != 1 {
		t.Fatalf("new front key: %+v", p.KeyList())
	}
}
