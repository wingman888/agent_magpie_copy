package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// multikeyHome points HOME and the config dir at a fresh temp dir, so the
// tests read and write their own providers.json and see no signed-in agent.
func multikeyHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
}

// multikeyLegacy writes a providers.json as an older magpie did: one key, no
// keyName, no keys.
func multikeyLegacy(t *testing.T, key string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"providers": []any{map[string]any{
		"id": "relay", "name": "Relay", "key": key, "chat": "https://relay.example/v1",
	}}})
	if err := os.WriteFile(Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func multikeyFind(t *testing.T) Provider {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

// multikeyOrder summarizes KeyList as "name(on|off)" entries, front first.
func multikeyOrder(p Provider) []string {
	var out []string
	for _, k := range p.KeyList() {
		state := "off"
		if k.On {
			state = "on"
		}
		out = append(out, k.Name+"("+state+")")
	}
	return out
}

func TestMultikeyLegacySingleKeyUnchanged(t *testing.T) {
	multikeyHome(t)
	const key = "sk-legacy-1234567890"
	multikeyLegacy(t, key)

	p := multikeyFind(t)
	if p.Key != key || p.KeyName != "" || len(p.Keys) != 0 {
		t.Fatalf("legacy config read wrong: key=%q name=%q keys=%v", p.Key, p.KeyName, p.Keys)
	}
	ks := p.KeyList()
	if len(ks) != 1 || !ks[0].Active || !ks[0].On || ks[0].Name != "" {
		t.Fatalf("legacy KeyList: %+v", ks)
	}
	if on := p.KeysOn(); len(on) != 1 || on[0].Key != key {
		t.Fatalf("legacy KeysOn: %+v", on)
	}
	// the front key is not rewritten by a read
	if got := multikeyFind(t); got.Key != key {
		t.Fatalf("read changed the key: %q", got.Key)
	}
}

func TestMultikeyAddKeyRules(t *testing.T) {
	multikeyHome(t)
	multikeyLegacy(t, "sk-first-aaaaaaaa")

	if err := AddKey("relay", " team ", "  sk-team-bbbbbbbb  "); err != nil {
		t.Fatal(err)
	}
	p := multikeyFind(t)
	if got := multikeyOrder(p); strings.Join(got, ",") != "(on),team(on)" {
		t.Fatalf("after AddKey: %v", got)
	}
	if p.Keys[0].Key != "sk-team-bbbbbbbb" || p.Keys[0].Name != "team" {
		t.Fatalf("added key stored wrong: %+v", p.Keys)
	}

	// duplicate, and empty, are refused; the error never shows the full key
	if err := AddKey("relay", "again", "sk-team-bbbbbbbb"); err == nil {
		t.Fatal("duplicate key accepted")
	} else if strings.Contains(err.Error(), "sk-team-bbbbbbbb") {
		t.Fatalf("duplicate error leaks the key: %v", err)
	}
	if err := AddKey("relay", "again", "sk-first-aaaaaaaa"); err == nil {
		t.Fatal("duplicate of the front key accepted")
	}
	if err := AddKey("relay", "blank", "   "); err == nil {
		t.Fatal("empty key accepted")
	}
	if got := multikeyOrder(multikeyFind(t)); len(got) != 2 {
		t.Fatalf("refused adds changed the list: %v", got)
	}

	// a provider with no key yet takes the first one as its front key
	if err := Save(Provider{ID: "bare", Name: "Bare", Chat: "https://bare.example/v1", Key: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("bare", multikeyID(t, "bare")); err == nil {
		t.Fatal("removing the only enabled key was allowed")
	}
}

// multikeyID returns the id of the first key of a provider.
func multikeyID(t *testing.T, id string) string {
	t.Helper()
	p, err := Find(id)
	if err != nil || len(p.KeyList()) == 0 {
		t.Fatalf("no key on %s: %v", id, err)
	}
	return p.KeyList()[0].ID
}

func TestMultikeyUseKeyAndOff(t *testing.T) {
	multikeyHome(t)
	multikeyLegacy(t, "sk-one-cccccccc")
	if err := AddKey("relay", "two", "sk-two-dddddddd"); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "free", "sk-free-eeeeeeee"); err != nil {
		t.Fatal(err)
	}
	ids := func() []string {
		var out []string
		for _, k := range multikeyFind(t).KeyList() {
			out = append(out, k.ID)
		}
		return out
	}
	before := ids()

	// UseKey on the front key changes nothing
	if err := UseKey("relay", before[0]); err != nil {
		t.Fatal(err)
	}
	if got := ids(); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("UseKey on front changed order: %v -> %v", before, got)
	}

	// UseKey on the second key: it leads, the old front follows, still on
	if err := UseKey("relay", before[1]); err != nil {
		t.Fatal(err)
	}
	p := multikeyFind(t)
	if got := multikeyOrder(p); strings.Join(got, ",") != "two(on),(on),free(on)" {
		t.Fatalf("after UseKey: %v", got)
	}
	if p.Key != "sk-two-dddddddd" || p.KeyName != "two" {
		t.Fatalf("front fields after UseKey: %q %q", p.Key, p.KeyName)
	}

	// turn off the front key: the next enabled one takes over, the off one stays listed
	if err := SetKeyOn("relay", before[1], false); err != nil {
		t.Fatal(err)
	}
	p = multikeyFind(t)
	if got := multikeyOrder(p); strings.Join(got, ",") != "(on),free(on),two(off)" {
		t.Fatalf("after turning off front: %v", got)
	}
	if on := p.KeysOn(); len(on) != 2 || on[0].Key != "sk-one-cccccccc" || on[1].Key != "sk-free-eeeeeeee" {
		t.Fatalf("KeysOn skips off keys wrongly: %+v", on)
	}

	// turning off a key that is already off is a no-op
	if err := SetKeyOn("relay", before[1], false); err != nil {
		t.Fatal(err)
	}
}

func TestMultikeyLastEnabledProtected(t *testing.T) {
	multikeyHome(t)
	multikeyLegacy(t, "sk-only-ffffffff")
	if err := AddKey("relay", "spare", "sk-spare-gggggggg"); err != nil {
		t.Fatal(err)
	}
	only := multikeyID(t, "relay")
	spare := multikeyFind(t).KeyList()[1].ID

	if err := SetKeyOn("relay", spare, false); err != nil {
		t.Fatal(err)
	}
	before := multikeyOrder(multikeyFind(t))

	// now only one enabled key is left: turning it off, or removing it, is refused
	if err := SetKeyOn("relay", only, false); err == nil {
		t.Fatal("turned off the last enabled key")
	}
	if err := RemoveKey("relay", only); err == nil {
		t.Fatal("removed the last enabled key")
	}
	if got := multikeyOrder(multikeyFind(t)); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("refused operations changed config: %v -> %v", before, got)
	}

	// re-enable the spare, then the original front can be removed
	if err := SetKeyOn("relay", spare, true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", only); err != nil {
		t.Fatal(err)
	}
	p := multikeyFind(t)
	if got := multikeyOrder(p); strings.Join(got, ",") != "spare(on)" || p.Key != "sk-spare-gggggggg" || p.KeyName != "spare" {
		t.Fatalf("after removing front: %v key=%q", got, p.Key)
	}
}

func TestMultikeyRemoveKeepsNames(t *testing.T) {
	multikeyHome(t)
	multikeyLegacy(t, "sk-main-hhhhhhhh")
	if err := RenameKey("relay", multikeyID(t, "relay"), "personal"); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "team", "sk-team-iiiiiiii"); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "free", "sk-free-jjjjjjjj"); err != nil {
		t.Fatal(err)
	}
	team := multikeyFind(t).KeyList()[1].ID
	if err := SetKeyOn("relay", team, false); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", multikeyID(t, "relay")); err != nil {
		t.Fatal(err)
	}
	p := multikeyFind(t)
	if got := multikeyOrder(p); strings.Join(got, ",") != "free(on),team(off)" {
		t.Fatalf("after removing personal: %v", got)
	}
	if p.KeyName != "free" || p.Key != "sk-free-jjjjjjjj" {
		t.Fatalf("front after removal: %q %q", p.KeyName, p.Key)
	}
}

func TestMultikeyRenameSetAndClear(t *testing.T) {
	multikeyHome(t)
	multikeyLegacy(t, "sk-name-kkkkkkkk")
	if err := AddKey("relay", "", "sk-name2-llllllll"); err != nil {
		t.Fatal(err)
	}
	second := multikeyFind(t).KeyList()[1].ID

	if err := RenameKey("relay", second, "  团队  "); err != nil {
		t.Fatal(err)
	}
	if got := multikeyFind(t).KeyList()[1].Name; got != "团队" {
		t.Fatalf("rename: %q", got)
	}
	if err := RenameKey("relay", second, "   "); err != nil {
		t.Fatal(err)
	}
	if got := multikeyFind(t).KeyList()[1].Name; got != "" {
		t.Fatalf("clear: %q", got)
	}
	// front key can be named and cleared too
	if err := RenameKey("relay", multikeyID(t, "relay"), "personal"); err != nil {
		t.Fatal(err)
	}
	if got := multikeyFind(t).KeyList()[0].Name; got != "personal" {
		t.Fatalf("front rename: %q", got)
	}
	if err := RenameKey("relay", multikeyID(t, "relay"), ""); err != nil {
		t.Fatal(err)
	}
	if got := multikeyFind(t).KeyList()[0].Name; got != "" {
		t.Fatalf("front clear: %q", got)
	}
}

func TestMultikeyUnknownRefErrors(t *testing.T) {
	multikeyHome(t)
	multikeyLegacy(t, "sk-ref-mmmmmmmm")
	const bogus = "sk-not-a-real-key-zzzz"
	for name, err := range map[string]error{
		"UseKey":     UseKey("relay", "nope"),
		"SetKeyOn":   SetKeyOn("relay", "nope", true),
		"RemoveKey":  RemoveKey("relay", "nope"),
		"RenameKey":  RenameKey("relay", "nope", "x"),
		"UseKeyFull": UseKey("relay", bogus),
	} {
		if err == nil {
			t.Errorf("%s with unknown ref succeeded", name)
		} else if strings.Contains(err.Error(), bogus) {
			t.Errorf("%s error echoes the ref: %v", name, err)
		}
	}
	if err := AddKey("nobody", "x", "sk-x"); err == nil {
		t.Error("AddKey on a missing provider succeeded")
	}
}

func TestMultikeyAccountRefusesKeys(t *testing.T) {
	signIn(t) // a signed-in codex account, offered as provider "codex"
	if err := AddKey("codex", "x", "sk-whatever-nnnnnnnn"); err == nil {
		t.Fatal("AddKey accepted a signed-in account")
	}
}
