package provider

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const (
	keyPersonal = "sk-personal-0123456789"
	keyTeam     = "sk-team-abcdefghijklmn"
	keyThird    = "sk-third-zyxwvutsrqpo"
)

// legacyRelay writes an old-style config: one provider, one key, no
// keyName or keys fields.
func legacyRelay(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")
	writeFile(t, Path(), map[string]any{"providers": []any{map[string]any{
		"id": "relay", "name": "Relay", "key": keyPersonal, "chat": "https://relay.example/v1",
	}}})
}

func relay(t *testing.T) *Provider {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func rawConfig(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func keyRef(t *testing.T, key string) string {
	t.Helper()
	for _, k := range relay(t).KeyList() {
		if k.ID == KeyID(key) {
			return k.ID
		}
	}
	t.Fatalf("no key %s in %+v", Mask(key), relay(t).KeyList())
	return ""
}

func TestLegacySingleKey(t *testing.T) {
	legacyRelay(t)
	p := relay(t)
	ks := p.KeyList()
	if len(ks) != 1 || !ks[0].Active || !ks[0].On || ks[0].Masked != Mask(keyPersonal) || ks[0].Name != "" {
		t.Fatalf("%+v", ks)
	}
	if on := p.KeysOn(); len(on) != 1 || on[0].Key != keyPersonal {
		t.Fatalf("%+v", on)
	}
}

func TestAddKey(t *testing.T) {
	legacyRelay(t)
	if err := AddKey("relay", "  团队 ", "  "+keyTeam+"\n"); err != nil {
		t.Fatal(err)
	}
	p := relay(t)
	ks := p.KeyList()
	if len(ks) != 2 {
		t.Fatalf("%+v", ks)
	}
	if !ks[0].Active || !ks[0].On || ks[0].ID != KeyID(keyPersonal) {
		t.Fatalf("front %+v", ks[0])
	}
	if ks[1].Active || !ks[1].On || ks[1].Name != "团队" || ks[1].Masked != Mask(keyTeam) {
		t.Fatalf("second %+v", ks[1])
	}
	if p.Key != keyPersonal {
		t.Fatalf("front key changed: %s", Mask(p.Key))
	}
	on := p.KeysOn()
	if len(on) != 2 || on[0].Key != keyPersonal || on[1].Key != keyTeam || on[1].Name != "团队" {
		t.Fatalf("%+v", on)
	}
	b, _ := json.Marshal(ks)
	if strings.Contains(string(b), keyPersonal) || strings.Contains(string(b), keyTeam) {
		t.Fatalf("list leaks a key: %s", b)
	}
	for _, k := range []string{keyTeam, keyPersonal, " " + keyTeam} {
		err := AddKey("relay", "again", k)
		if err == nil {
			t.Fatalf("duplicate %s accepted", Mask(k))
		}
		if strings.Contains(err.Error(), strings.TrimSpace(k)) {
			t.Fatalf("error leaks the key: %v", err)
		}
	}
	for _, k := range []string{"", "   "} {
		if err := AddKey("relay", "x", k); err == nil {
			t.Fatal("empty key accepted")
		}
	}
	if err := AddKey("nowhere", "x", keyThird); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if len(relay(t).KeyList()) != 2 {
		t.Fatal("failed adds changed the list")
	}
	cfg := rawConfig(t)
	if !strings.Contains(cfg, `"keys"`) || !strings.Contains(cfg, `"key": "`+keyPersonal+`"`) {
		t.Fatalf("config %s", cfg)
	}
}

func TestUseKey(t *testing.T) {
	legacyRelay(t)
	AddKey("relay", "团队", keyTeam)
	AddKey("relay", "", keyThird)
	if err := UseKey("relay", KeyID(keyThird)); err != nil {
		t.Fatal(err)
	}
	ks := relay(t).KeyList()
	if ks[0].ID != KeyID(keyThird) || !ks[0].Active || ks[1].ID != KeyID(keyPersonal) || !ks[1].On || ks[2].ID != KeyID(keyTeam) {
		t.Fatalf("%+v", ks)
	}
	if relay(t).Key != keyThird {
		t.Fatal("key field is not the front key")
	}
	before := rawConfig(t)
	if err := UseKey("relay", KeyID(keyThird)); err != nil {
		t.Fatal(err)
	}
	if rawConfig(t) != before {
		t.Fatal("using the front key changed something")
	}
	// using a key that is off switches it on
	if err := SetKeyOn("relay", KeyID(keyTeam), false); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", KeyID(keyTeam)); err != nil {
		t.Fatal(err)
	}
	ks = relay(t).KeyList()
	if ks[0].ID != KeyID(keyTeam) || !ks[0].On || ks[1].ID != KeyID(keyThird) || !ks[1].On {
		t.Fatalf("%+v", ks)
	}
}

func TestSetKeyOnAndRemove(t *testing.T) {
	legacyRelay(t)
	if err := RenameKey("relay", KeyID(keyPersonal), "个人"); err != nil {
		t.Fatal(err)
	}
	AddKey("relay", "团队", keyTeam)

	// switching the front off hands the front to the next key on
	if err := SetKeyOn("relay", KeyID(keyPersonal), false); err != nil {
		t.Fatal(err)
	}
	p := relay(t)
	ks := p.KeyList()
	if len(ks) != 2 || ks[0].ID != KeyID(keyTeam) || !ks[0].Active || !ks[0].On || ks[0].Name != "团队" {
		t.Fatalf("%+v", ks)
	}
	if ks[1].ID != KeyID(keyPersonal) || ks[1].Active || ks[1].On || ks[1].Name != "个人" {
		t.Fatalf("%+v", ks)
	}
	if on := p.KeysOn(); len(on) != 1 || on[0].Key != keyTeam {
		t.Fatalf("%+v", on)
	}

	// the last key on can't go
	before := rawConfig(t)
	if err := SetKeyOn("relay", KeyID(keyTeam), false); err == nil {
		t.Fatal("switched off the last key on")
	} else if strings.Contains(err.Error(), keyTeam) {
		t.Fatalf("error leaks the key: %v", err)
	}
	if err := RemoveKey("relay", KeyID(keyTeam)); err == nil {
		t.Fatal("removed the last key on")
	}
	if rawConfig(t) != before {
		t.Fatal("a refused change was written")
	}

	// switching back on keeps the order; then the old front can be removed
	if err := SetKeyOn("relay", KeyID(keyPersonal), true); err != nil {
		t.Fatal(err)
	}
	ks = relay(t).KeyList()
	if ks[0].ID != KeyID(keyTeam) || !ks[1].On {
		t.Fatalf("%+v", ks)
	}
	if err := UseKey("relay", KeyID(keyPersonal)); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", KeyID(keyPersonal)); err != nil {
		t.Fatal(err)
	}
	ks = relay(t).KeyList()
	if len(ks) != 1 || ks[0].ID != KeyID(keyTeam) || ks[0].Name != "团队" || !ks[0].Active || !ks[0].On {
		t.Fatalf("%+v", ks)
	}
	if relay(t).Key != keyTeam {
		t.Fatal("key field is not the front key")
	}
}

func TestRemoveKeyThatIsOff(t *testing.T) {
	legacyRelay(t)
	AddKey("relay", "团队", keyTeam)
	AddKey("relay", "free", keyThird)
	SetKeyOn("relay", KeyID(keyThird), false)
	if err := RemoveKey("relay", KeyID(keyThird)); err != nil {
		t.Fatal(err)
	}
	// removing the front when the next key is off skips to one that is on
	SetKeyOn("relay", KeyID(keyTeam), false)
	if err := RemoveKey("relay", KeyID(keyPersonal)); err == nil {
		t.Fatal("removed the only key on")
	}
	AddKey("relay", "", keyThird)
	if err := RemoveKey("relay", KeyID(keyPersonal)); err != nil {
		t.Fatal(err)
	}
	ks := relay(t).KeyList()
	if len(ks) != 2 || ks[0].ID != KeyID(keyThird) || !ks[0].On || ks[1].ID != KeyID(keyTeam) || ks[1].On {
		t.Fatalf("%+v", ks)
	}
}

func TestRenameKey(t *testing.T) {
	legacyRelay(t)
	AddKey("relay", "团队", keyTeam)
	if err := RenameKey("relay", KeyID(keyPersonal), " 个人 "); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("relay", KeyID(keyTeam), ""); err != nil {
		t.Fatal(err)
	}
	ks := relay(t).KeyList()
	if ks[0].Name != "个人" || ks[1].Name != "" {
		t.Fatalf("%+v", ks)
	}
	if !strings.Contains(rawConfig(t), `"keyName": "个人"`) {
		t.Fatal(rawConfig(t))
	}
	if err := RenameKey("relay", KeyID(keyPersonal), ""); err != nil {
		t.Fatal(err)
	}
	if relay(t).KeyList()[0].Name != "" {
		t.Fatal("name not cleared")
	}
}

func TestUnknownKeyRef(t *testing.T) {
	legacyRelay(t)
	AddKey("relay", "团队", keyTeam)
	before := rawConfig(t)
	for name, err := range map[string]error{
		"use":    UseKey("relay", "nope"),
		"on":     SetKeyOn("relay", "nope", true),
		"off":    SetKeyOn("relay", "nope", false),
		"remove": RemoveKey("relay", "nope"),
		"rename": RenameKey("relay", "nope", "x"),
		"raw":    UseKey("relay", keyTeam), // the key itself is not a ref
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if rawConfig(t) != before {
		t.Fatal("config changed")
	}
	if _, err := Find("relay"); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("nowhere", KeyID(keyTeam)); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestSaveKeepsOtherKeys(t *testing.T) {
	legacyRelay(t)
	AddKey("relay", "团队", keyTeam)
	// an editor that only knows the front key saves the provider again
	if err := Save(Provider{ID: "relay", Name: "Relay 2", Key: keyPersonal, Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
	ks := relay(t).KeyList()
	if len(ks) != 2 || ks[1].Name != "团队" || relay(t).Name != "Relay 2" {
		t.Fatalf("%+v", ks)
	}
	// saving what Find returned keeps everything
	p := relay(t)
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if len(relay(t).KeyList()) != 2 {
		t.Fatal("keys lost")
	}
}

func TestAddKeyToSignedInAccount(t *testing.T) {
	signIn(t)
	if _, err := Find("codex"); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("codex", "x", keyThird); err == nil {
		t.Fatal("added a key to a signed-in account")
	}
}

func TestAddKeyToProviderWithoutKey(t *testing.T) {
	legacyRelay(t)
	if err := Save(Provider{ID: "local", Name: "Local", Chat: "http://localhost:11434/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("local", "main", keyThird); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("local")
	if p.Key != keyThird || p.KeyName != "main" || len(p.KeyList()) != 1 || !p.KeyList()[0].Active {
		t.Fatalf("%+v", p.KeyList())
	}
}
