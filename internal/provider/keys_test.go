package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// keyEnv points magpie at an empty config and home, with no signed-in agents.
func keyEnv(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func saveRelay(t *testing.T, key string) {
	t.Helper()
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: key, Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
}

func mustFind(t *testing.T, id string) *Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// checkOrder asserts the provider's keys, in try order, by id and enabled flag.
func checkOrder(t *testing.T, id string, ids []string, on []bool) {
	t.Helper()
	ks := mustFind(t, id).KeyList()
	if len(ks) != len(ids) {
		t.Fatalf("got %d keys %+v, want %d", len(ks), ks, len(ids))
	}
	for i, k := range ks {
		if k.ID != ids[i] || k.On != on[i] || k.Active != (i == 0) {
			t.Fatalf("key %d: got %+v, want id %s on %v", i, k, ids[i], on[i])
		}
	}
}

func TestLegacyConfigIsOneKey(t *testing.T) {
	keyEnv(t)
	writeFile(t, Path(), map[string]any{"providers": []any{map[string]any{
		"id": "relay", "name": "Relay", "key": "sk-old-0000000001", "chat": "https://relay.example/v1",
	}}})
	p := mustFind(t, "relay")
	if p.Key != "sk-old-0000000001" || p.KeyName != "" || len(p.Keys) != 0 {
		t.Fatalf("legacy provider changed: %+v", p)
	}
	ks := p.KeyList()
	if len(ks) != 1 || !ks[0].Active || !ks[0].On || ks[0].Name != "" {
		t.Fatalf("legacy KeyList: %+v", ks)
	}
	if on := p.KeysOn(); len(on) != 1 || on[0].Key != "sk-old-0000000001" {
		t.Fatalf("legacy KeysOn: %+v", on)
	}
}

func TestAddKeyAppendsAndRejects(t *testing.T) {
	keyEnv(t)
	saveRelay(t, "sk-personal-0001")
	if err := AddKey("relay", "团队", "  sk-team-0002  "); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t, "relay")
	ks := p.KeyList()
	if len(ks) != 2 || !ks[0].Active || !ks[0].On || ks[0].ID != keyID("sk-personal-0001") ||
		ks[1].Active || !ks[1].On || ks[1].Name != "团队" || ks[1].ID != keyID("sk-team-0002") {
		t.Fatalf("after AddKey: %+v", ks)
	}
	if p.Key != "sk-personal-0001" || p.KeyName != "" {
		t.Fatalf("front key changed: key=%q name=%q", p.Key, p.KeyName)
	}
	if err := AddKey("relay", "again", "sk-team-0002"); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if err := AddKey("relay", "again", "sk-personal-0001"); err == nil {
		t.Fatal("duplicate of the front key accepted")
	}
	if err := AddKey("relay", "", "   "); err == nil {
		t.Fatal("empty key accepted")
	}
	if len(mustFind(t, "relay").KeyList()) != 2 {
		t.Fatal("rejected AddKey changed the keys")
	}
}

func TestAddKeyToProviderWithoutKey(t *testing.T) {
	keyEnv(t)
	if err := Save(Provider{ID: "local", Name: "Local", Chat: "http://localhost:11434/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("local", "", "sk-first-0001"); err != nil {
		t.Fatal(err)
	}
	ks := mustFind(t, "local").KeyList()
	if len(ks) != 1 || !ks[0].Active || !ks[0].On {
		t.Fatalf("first key should front: %+v", ks)
	}
}

func TestAddKeyRejectsSignedInAccount(t *testing.T) {
	signIn(t)
	if err := AddKey("codex", "x", "sk-whatever-1234"); err == nil {
		t.Fatal("AddKey on a signed-in account accepted")
	}
}

func TestUseKeyPromotes(t *testing.T) {
	keyEnv(t)
	saveRelay(t, "sk-a-0001")
	if err := AddKey("relay", "b", "sk-b-0002"); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "c", "sk-c-0003"); err != nil {
		t.Fatal(err)
	}
	a, b, c := keyID("sk-a-0001"), keyID("sk-b-0002"), keyID("sk-c-0003")
	on3 := []bool{true, true, true}
	if err := UseKey("relay", b); err != nil {
		t.Fatal(err)
	}
	checkOrder(t, "relay", []string{b, a, c}, on3)
	p := mustFind(t, "relay")
	if p.Key != "sk-b-0002" || p.KeyName != "b" {
		t.Fatalf("front after UseKey: key=%q name=%q", p.Key, p.KeyName)
	}
	// already the front: nothing moves
	if err := UseKey("relay", b); err != nil {
		t.Fatal(err)
	}
	checkOrder(t, "relay", []string{b, a, c}, on3)
	if err := UseKey("relay", "nope0000"); err == nil {
		t.Fatal("unknown keyRef accepted")
	}
}

func TestSetKeyOnDisablesFront(t *testing.T) {
	keyEnv(t)
	saveRelay(t, "sk-a-0001")
	if err := AddKey("relay", "b", "sk-b-0002"); err != nil {
		t.Fatal(err)
	}
	a, b := keyID("sk-a-0001"), keyID("sk-b-0002")
	if err := SetKeyOn("relay", a, false); err != nil {
		t.Fatal(err)
	}
	checkOrder(t, "relay", []string{b, a}, []bool{true, false})
	p := mustFind(t, "relay")
	if p.Key != "sk-b-0002" {
		t.Fatalf("next enabled key should front, got %q", p.Key)
	}
	if on := p.KeysOn(); len(on) != 1 || on[0].Key != "sk-b-0002" {
		t.Fatalf("KeysOn after disable: %+v", on)
	}
	raw, err := os.ReadFile(Path())
	if err != nil || !strings.Contains(string(raw), `"off": true`) {
		t.Fatalf("disabled key not persisted: %v %s", err, raw)
	}

	// the last enabled key cannot be turned off, and nothing changes
	if err := SetKeyOn("relay", b, false); err == nil {
		t.Fatal("disabling the last enabled key accepted")
	}
	checkOrder(t, "relay", []string{b, a}, []bool{true, false})

	// turning the off key back on keeps it where it is
	if err := SetKeyOn("relay", a, true); err != nil {
		t.Fatal(err)
	}
	checkOrder(t, "relay", []string{b, a}, []bool{true, true})
}

func TestRemoveKey(t *testing.T) {
	keyEnv(t)
	saveRelay(t, "sk-a-0001")
	if err := AddKey("relay", "team", "sk-b-0002"); err != nil {
		t.Fatal(err)
	}
	a, b := keyID("sk-a-0001"), keyID("sk-b-0002")
	if err := SetKeyOn("relay", a, false); err != nil {
		t.Fatal(err)
	}
	// only one enabled key left: it cannot be removed
	if err := RemoveKey("relay", b); err == nil {
		t.Fatal("removing the last enabled key accepted")
	}
	checkOrder(t, "relay", []string{b, a}, []bool{true, false})

	// enable the other key, then the old front can go
	if err := SetKeyOn("relay", a, true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", a); err != nil {
		t.Fatal(err)
	}
	checkOrder(t, "relay", []string{b}, []bool{true})
	p := mustFind(t, "relay")
	if p.Key != "sk-b-0002" || p.KeyName != "team" {
		t.Fatalf("surviving key lost its name: key=%q name=%q", p.Key, p.KeyName)
	}
	if err := RemoveKey("relay", b); err == nil {
		t.Fatal("removing the only key accepted")
	}
}

func TestRemoveFrontHandsOver(t *testing.T) {
	keyEnv(t)
	saveRelay(t, "sk-a-0001")
	if err := AddKey("relay", "b", "sk-b-0002"); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "c", "sk-c-0003"); err != nil {
		t.Fatal(err)
	}
	a, b, c := keyID("sk-a-0001"), keyID("sk-b-0002"), keyID("sk-c-0003")
	if err := RemoveKey("relay", a); err != nil {
		t.Fatal(err)
	}
	checkOrder(t, "relay", []string{b, c}, []bool{true, true})
	if p := mustFind(t, "relay"); p.Key != "sk-b-0002" || p.KeyName != "b" {
		t.Fatalf("front after RemoveKey: key=%q name=%q", p.Key, p.KeyName)
	}
}

func TestRenameKey(t *testing.T) {
	keyEnv(t)
	saveRelay(t, "sk-a-0001")
	a := keyID("sk-a-0001")
	if err := RenameKey("relay", a, "  个人  "); err != nil {
		t.Fatal(err)
	}
	if ks := mustFind(t, "relay").KeyList(); ks[0].Name != "个人" {
		t.Fatalf("rename: %+v", ks)
	}
	if err := RenameKey("relay", a, "   "); err != nil {
		t.Fatal(err)
	}
	if ks := mustFind(t, "relay").KeyList(); ks[0].Name != "" {
		t.Fatalf("clearing name: %+v", ks)
	}
}

func TestUnknownKeyRefErrors(t *testing.T) {
	keyEnv(t)
	saveRelay(t, "sk-a-0001")
	errs := map[string]error{
		"UseKey":    UseKey("relay", "deadbeef0000"),
		"SetKeyOn":  SetKeyOn("relay", "deadbeef0000", false),
		"RemoveKey": RemoveKey("relay", "deadbeef0000"),
		"RenameKey": RenameKey("relay", "deadbeef0000", "x"),
	}
	for name, err := range errs {
		if err == nil {
			t.Errorf("%s with an unknown keyRef accepted", name)
		}
	}
	if err := AddKey("nosuch", "", "sk-x-0001"); err == nil {
		t.Error("AddKey on an unknown provider accepted")
	}
}

func TestKeyListNeverShowsFullKey(t *testing.T) {
	keyEnv(t)
	const front, team = "sk-personal-0001", "sk-team-0002"
	saveRelay(t, front)
	if err := AddKey("relay", "团队", team); err != nil {
		t.Fatal(err)
	}
	shown := fmt.Sprintf("%+v", mustFind(t, "relay").KeyList())
	errText := AddKey("relay", "again", team).Error()
	for _, s := range []string{shown, errText} {
		if strings.Contains(s, front) || strings.Contains(s, team) {
			t.Fatalf("full key leaked: %s", s)
		}
	}
	for _, c := range mustFind(t, "relay").KeysOn() {
		if c.Label("relay") == "" {
			t.Fatal("empty label")
		}
	}
	if got := (Credential{ID: "x", Key: team}).Label("relay"); got != "relay("+Mask(team)+")" {
		t.Fatalf("unnamed label: %s", got)
	}
	if got := (Credential{ID: "x", Name: "个人", Key: front}).Label("relay"); got != "relay(个人)" {
		t.Fatalf("named label: %s", got)
	}
}

func TestKeysOnSkipsDisabled(t *testing.T) {
	keyEnv(t)
	saveRelay(t, "sk-a-0001")
	if err := AddKey("relay", "b", "sk-b-0002"); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "c", "sk-c-0003"); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", keyID("sk-b-0002"), false); err != nil {
		t.Fatal(err)
	}
	on := mustFind(t, "relay").KeysOn()
	if len(on) != 2 || on[0].Key != "sk-a-0001" || on[1].Key != "sk-c-0003" {
		t.Fatalf("KeysOn: %+v", on)
	}
}
