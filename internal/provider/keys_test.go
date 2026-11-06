package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testKeyA = "sk-personal-aaaa1111"
	testKeyB = "sk-team-bbbb2222"
	testKeyC = "sk-free-cccc3333"
)

// seedProvider writes a config with one provider into a temp home.
func seedProvider(t *testing.T, p Provider) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	writeFile(t, Path(), file{Providers: []Provider{p}})
}

func legacyRelay() Provider {
	return Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: testKeyA}
}

// keyNames lists a provider's keys in order: name, "*" when first, "-off" when disabled.
func keyNames(t *testing.T, id string) []string {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range p.KeyList() {
		s := k.Name
		if k.Active {
			s += "*"
		}
		if !k.On {
			s += "-off"
		}
		out = append(out, s)
	}
	return out
}

func configBytes(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLegacySingleKeyGainsTeamKey(t *testing.T) {
	seedProvider(t, legacyRelay())
	if err := AddKey("relay", "团队", "  "+testKeyB+"  "); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	list := p.KeyList()
	if len(list) != 2 {
		t.Fatalf("want 2 keys, got %+v", list)
	}
	if !list[0].Active || !list[0].On || list[0].Name != "" {
		t.Fatalf("first key: %+v", list[0])
	}
	if list[1].Active || !list[1].On || list[1].Name != "团队" {
		t.Fatalf("second key: %+v", list[1])
	}
	if list[1].ID != KeyRef(testKeyB) {
		t.Fatalf("id %q, want %q", list[1].ID, KeyRef(testKeyB))
	}
	// the legacy field still holds the front key
	if p.Key != testKeyA {
		t.Fatalf("front key %q", p.Key)
	}
}

func TestAddKeyRejectsEmptyAndDuplicate(t *testing.T) {
	seedProvider(t, legacyRelay())
	if err := AddKey("relay", "", "   "); err == nil {
		t.Fatal("empty key accepted")
	}
	if err := AddKey("relay", "x", testKeyA); err == nil {
		t.Fatal("duplicate key accepted")
	} else if strings.Contains(err.Error(), testKeyA) {
		t.Fatalf("error leaks full key: %v", err)
	}
	if err := AddKey("relay", "x", "  "+testKeyA+" "); err == nil {
		t.Fatal("duplicate key with spaces accepted")
	}
	if err := AddKey("nope", "x", testKeyB); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestAddKeyRejectsSignedInAccount(t *testing.T) {
	signIn(t)
	if err := AddKey("codex", "", testKeyB); err == nil {
		t.Fatal("account accepted an API key")
	}
}

func TestNoFullKeyInListing(t *testing.T) {
	seedProvider(t, legacyRelay())
	if err := AddKey("relay", "团队", testKeyB); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	for _, k := range p.KeyList() {
		if strings.Contains(k.Masked, testKeyA) || strings.Contains(k.Masked, testKeyB) {
			t.Fatalf("masked shows full key: %+v", k)
		}
	}
	if err := UseKey("relay", "deadbeef"); err == nil || strings.Contains(err.Error(), testKeyA) {
		t.Fatalf("unknown ref error: %v", err)
	}
}

func TestSetKeyOnAndRemoveKeepOneEnabled(t *testing.T) {
	seedProvider(t, legacyRelay())
	if err := RenameKey("relay", KeyRef(testKeyA), "个人"); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "团队", testKeyB); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "免费", testKeyC); err != nil {
		t.Fatal(err)
	}
	// [个人 团队 免费] -> disable the front: team takes over, personal stays listed off
	if err := SetKeyOn("relay", KeyRef(testKeyA), false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(keyNames(t, "relay"), " "); got != "团队* 个人-off 免费" {
		t.Fatalf("after disabling front: %s", got)
	}
	// disable the new front too: free takes over
	if err := SetKeyOn("relay", KeyRef(testKeyB), false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(keyNames(t, "relay"), " "); got != "免费* 团队-off 个人-off" {
		t.Fatalf("after second disable: %s", got)
	}
	// the last enabled key cannot be disabled or removed, and nothing changes
	before := configBytes(t)
	if err := SetKeyOn("relay", KeyRef(testKeyC), false); err == nil {
		t.Fatal("disabled the last enabled key")
	}
	if err := RemoveKey("relay", KeyRef(testKeyC)); err == nil {
		t.Fatal("removed the last enabled key")
	}
	if configBytes(t) != before {
		t.Fatal("config changed after a rejected change")
	}
	// re-enable personal, then removing the front hands over to it
	if err := SetKeyOn("relay", KeyRef(testKeyA), true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", KeyRef(testKeyC)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(keyNames(t, "relay"), " "); got != "个人* 团队-off" {
		t.Fatalf("after removing front: %s", got)
	}
	// a disabled key can always go
	if err := RemoveKey("relay", KeyRef(testKeyB)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(keyNames(t, "relay"), " "); got != "个人*" {
		t.Fatalf("after removing off key: %s", got)
	}
}

func TestUseKeyPromotesAndKeepsOldFrontEnabled(t *testing.T) {
	seedProvider(t, legacyRelay())
	AddKey("relay", "团队", testKeyB)
	AddKey("relay", "免费", testKeyC)
	if err := UseKey("relay", KeyRef(testKeyC)); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	list := p.KeyList()
	if !list[0].Active || list[0].ID != KeyRef(testKeyC) || !list[1].On || list[1].ID != KeyRef(testKeyA) || !list[2].On {
		t.Fatalf("after UseKey: %+v", list)
	}
	before := configBytes(t)
	if err := UseKey("relay", KeyRef(testKeyC)); err != nil {
		t.Fatal(err)
	}
	if configBytes(t) != before {
		t.Fatal("using the front key changed the config")
	}
	if err := UseKey("relay", "deadbeef"); err == nil {
		t.Fatal("unknown ref accepted")
	}
}

func TestRenameKeySetsAndClears(t *testing.T) {
	seedProvider(t, legacyRelay())
	AddKey("relay", "团队", testKeyB)
	if err := RenameKey("relay", KeyRef(testKeyB), "  新名字  "); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if p.KeyList()[1].Name != "新名字" {
		t.Fatalf("rename: %+v", p.KeyList())
	}
	if err := RenameKey("relay", KeyRef(testKeyB), "   "); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.KeyList()[1].Name != "" {
		t.Fatalf("clear: %+v", p.KeyList())
	}
	if err := RenameKey("relay", "deadbeef", "x"); err == nil {
		t.Fatal("unknown ref accepted")
	}
}

func TestSingleKeyConfigStaysPlain(t *testing.T) {
	seedProvider(t, legacyRelay())
	if err := RenameKey("relay", KeyRef(testKeyA), ""); err != nil {
		t.Fatal(err)
	}
	cfg := configBytes(t)
	if strings.Contains(cfg, "keyName") || strings.Contains(cfg, `"keys"`) {
		t.Fatalf("single key config grew fields:\n%s", cfg)
	}
	var f file
	if err := json.Unmarshal([]byte(cfg), &f); err != nil {
		t.Fatal(err)
	}
	if f.Providers[0].Key != testKeyA {
		t.Fatalf("key field: %+v", f.Providers[0])
	}
}

func TestKeysOnOrderAndCooldown(t *testing.T) {
	seedProvider(t, legacyRelay())
	t.Cleanup(func() {
		keyRest.Lock()
		keyRest.m = map[string]time.Time{}
		keyRest.Unlock()
	})
	AddKey("relay", "团队", testKeyB)
	AddKey("relay", "免费", testKeyC)
	SetKeyOn("relay", KeyRef(testKeyC), false)
	p, _ := Find("relay")
	on := p.KeysOn()
	if len(on) != 2 || on[0].Key != testKeyA || on[1].Key != testKeyB || on[1].Name != "团队" {
		t.Fatalf("KeysOn: %+v", on)
	}
	MarkKeyFailed("relay", KeyRef(testKeyA))
	on = p.KeysOn()
	if len(on) != 2 || on[0].Key != testKeyB || on[1].Key != testKeyA {
		t.Fatalf("resting key not moved to the back: %+v", on)
	}
	if !KeyResting("relay", KeyRef(testKeyA)) || KeyResting("relay", KeyRef(testKeyB)) {
		t.Fatal("cooldown is not per key")
	}
}

func TestSaveKeepsExtraKeys(t *testing.T) {
	seedProvider(t, legacyRelay())
	AddKey("relay", "团队", testKeyB)
	// a form that only knows the front key saves the provider again
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: testKeyA}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if list := p.KeyList(); len(list) != 2 || list[1].Name != "团队" {
		t.Fatalf("Save dropped keys: %+v", list)
	}
}
