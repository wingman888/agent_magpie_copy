package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkeyTestEnv isolates the config and home directories for one test.
func mkeyTestEnv(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

// mkeyTestWriteConfig writes the raw providers.json the tests start from.
func mkeyTestWriteConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mkeyTestRaw(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mkeyTestList(t *testing.T, id string) []KeyInfo {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return p.KeyList()
}

// mkeyTestRef returns the reference of the key whose name is name.
func mkeyTestRef(t *testing.T, id, name string) string {
	t.Helper()
	for _, k := range mkeyTestList(t, id) {
		if k.Name == name {
			return k.ID
		}
	}
	t.Fatalf("no key named %q on %s", name, id)
	return ""
}

func mkeyTestWant(t *testing.T, got []KeyInfo, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d keys, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Name != w {
			t.Fatalf("key %d is %q, want %q (all: %+v)", i, got[i].Name, w, got)
		}
	}
}

func mkeyTestRelay(t *testing.T, key string) {
	t.Helper()
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: key}); err != nil {
		t.Fatal(err)
	}
}

const mkeyTestLegacy = `{"providers":[{"id":"relay","name":"Relay","chat":"https://relay.example.com/v1","key":"sk-old-1234567890"}]}`

func TestMkeyTestLegacyConfigIsOneKey(t *testing.T) {
	mkeyTestEnv(t)
	mkeyTestWriteConfig(t, mkeyTestLegacy)
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	ks := p.KeyList()
	if len(ks) != 1 || !ks[0].Active || !ks[0].On || ks[0].Name != "" {
		t.Fatalf("legacy provider keys = %+v", ks)
	}
	if ks[0].Masked != Mask("sk-old-1234567890") {
		t.Fatalf("masked = %q", ks[0].Masked)
	}
	if on := p.KeysOn(); len(on) != 1 || on[0].Key != "sk-old-1234567890" {
		t.Fatalf("KeysOn = %+v", on)
	}
}

func TestMkeyTestAddKeyAppendsAndValidates(t *testing.T) {
	mkeyTestEnv(t)
	mkeyTestWriteConfig(t, mkeyTestLegacy)

	if err := AddKey("relay", "团队", "  sk-team-0987654321  "); err != nil {
		t.Fatal(err)
	}
	ks := mkeyTestList(t, "relay")
	mkeyTestWant(t, ks, "", "团队")
	if !ks[0].Active || !ks[0].On || ks[1].Active || !ks[1].On {
		t.Fatalf("flags wrong: %+v", ks)
	}
	if ks[1].Masked != Mask("sk-team-0987654321") {
		t.Fatalf("masked = %q", ks[1].Masked)
	}

	err := AddKey("relay", "again", "sk-team-0987654321")
	if err == nil {
		t.Fatal("duplicate key accepted")
	}
	if strings.Contains(err.Error(), "sk-team-0987654321") {
		t.Fatalf("duplicate error leaks the full key: %v", err)
	}
	if err := AddKey("relay", "blank", "   "); err == nil {
		t.Fatal("empty key accepted")
	}
	mkeyTestWant(t, mkeyTestList(t, "relay"), "", "团队")
}

func TestMkeyTestAddKeyToKeylessProviderBecomesFront(t *testing.T) {
	mkeyTestEnv(t)
	// A provider with no key at all (Save wouldn't write one, so write the config directly).
	mkeyTestWriteConfig(t, `{"providers":[{"id":"relay","name":"Relay","chat":"https://relay.example.com/v1"}]}`)
	if err := AddKey("relay", "solo", "sk-solo-1234567890"); err != nil {
		t.Fatal(err)
	}
	ks := mkeyTestList(t, "relay")
	mkeyTestWant(t, ks, "solo")
	if !ks[0].Active {
		t.Fatal("first key should be the front one")
	}
}

func TestMkeyTestSingleKeyFormatUnchanged(t *testing.T) {
	mkeyTestEnv(t)
	mkeyTestRelay(t, "sk-only-1234567890")
	raw := mkeyTestRaw(t)
	if strings.Contains(raw, "keyName") || strings.Contains(raw, `"keys"`) {
		t.Fatalf("single key config gained new fields:\n%s", raw)
	}
	if !strings.Contains(raw, `"key": "sk-only-1234567890"`) {
		t.Fatalf("key missing from config:\n%s", raw)
	}
}

func TestMkeyTestUseKey(t *testing.T) {
	mkeyTestEnv(t)
	mkeyTestWriteConfig(t, mkeyTestLegacy)
	if err := AddKey("relay", "个人", "sk-me-1234567890"); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "团队", "sk-team-1234567890"); err != nil {
		t.Fatal(err)
	}
	// Front key is already "" (legacy); bring "团队" to the front.
	if err := UseKey("relay", mkeyTestRef(t, "relay", "团队")); err != nil {
		t.Fatal(err)
	}
	ks := mkeyTestList(t, "relay")
	mkeyTestWant(t, ks, "团队", "", "个人")
	for _, k := range ks {
		if !k.On {
			t.Fatalf("key %q should stay enabled: %+v", k.Name, ks)
		}
	}
	// Using the front key again changes nothing.
	if err := UseKey("relay", ks[0].ID); err != nil {
		t.Fatal(err)
	}
	mkeyTestWant(t, mkeyTestList(t, "relay"), "团队", "", "个人")
	if err := UseKey("relay", "nope"); err == nil {
		t.Fatal("unknown key ref accepted")
	}
}

func TestMkeyTestSetKeyOnFrontAndLastEnabled(t *testing.T) {
	mkeyTestEnv(t)
	mkeyTestWriteConfig(t, mkeyTestLegacy)
	if err := AddKey("relay", "B", "sk-b-1234567890"); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "C", "sk-c-1234567890"); err != nil {
		t.Fatal(err)
	}
	// Disable the front key: B moves to the front, the old front stays listed, off.
	if err := SetKeyOn("relay", mkeyTestList(t, "relay")[0].ID, false); err != nil {
		t.Fatal(err)
	}
	ks := mkeyTestList(t, "relay")
	mkeyTestWant(t, ks, "B", "", "C")
	if !ks[0].Active || !ks[0].On || ks[1].On || !ks[2].On {
		t.Fatalf("after disabling front: %+v", ks)
	}
	if on := mkeyTestOn(t, "relay"); len(on) != 2 || on[0] != "B" || on[1] != "C" {
		t.Fatalf("KeysOn = %v", on)
	}

	// Disable C: B is still enabled, so this is allowed.
	if err := SetKeyOn("relay", mkeyTestRef(t, "relay", "C"), false); err != nil {
		t.Fatal(err)
	}
	// Disabling the last enabled key is refused and nothing changes.
	before := mkeyTestRaw(t)
	err := SetKeyOn("relay", mkeyTestRef(t, "relay", "B"), false)
	if err == nil {
		t.Fatal("disabling the last enabled key accepted")
	}
	if mkeyTestRaw(t) != before {
		t.Fatal("refused operation still changed the config")
	}
	if err := SetKeyOn("relay", "nope", true); err == nil {
		t.Fatal("unknown key ref accepted")
	}
}

func TestMkeyTestRemoveKey(t *testing.T) {
	mkeyTestEnv(t)
	mkeyTestWriteConfig(t, mkeyTestLegacy)
	if err := AddKey("relay", "A", "sk-a-1234567890"); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", mkeyTestList(t, "relay")[0].ID, false); err != nil {
		t.Fatal(err)
	}
	// Only "A" is enabled now; removing it must fail.
	before := mkeyTestRaw(t)
	if err := RemoveKey("relay", mkeyTestRef(t, "relay", "A")); err == nil {
		t.Fatal("removing the last enabled key accepted")
	}
	if mkeyTestRaw(t) != before {
		t.Fatal("refused removal still changed the config")
	}
	// Enable the old front key again, then remove "A", which is now the front.
	if err := SetKeyOn("relay", mkeyTestRef(t, "relay", ""), true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", mkeyTestRef(t, "relay", "A")); err != nil {
		t.Fatal(err)
	}
	ks := mkeyTestList(t, "relay")
	if len(ks) != 1 || ks[0].Name != "" || !ks[0].Active {
		t.Fatalf("after removal: %+v", ks)
	}
	if err := RemoveKey("relay", "nope"); err == nil {
		t.Fatal("unknown key ref accepted")
	}
}

func TestMkeyTestRemoveFrontPromotesNextEnabled(t *testing.T) {
	mkeyTestEnv(t)
	mkeyTestRelay(t, "sk-front-1234567890")
	if err := AddKey("relay", "name-kept", "sk-next-1234567890"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", mkeyTestList(t, "relay")[0].ID); err != nil {
		t.Fatal(err)
	}
	ks := mkeyTestList(t, "relay")
	mkeyTestWant(t, ks, "name-kept")
	if !ks[0].Active {
		t.Fatal("next key should be the front one")
	}
}

func TestMkeyTestRenameKey(t *testing.T) {
	mkeyTestEnv(t)
	mkeyTestWriteConfig(t, mkeyTestLegacy)
	if err := AddKey("relay", "团队", "sk-team-1234567890"); err != nil {
		t.Fatal(err)
	}
	front := mkeyTestList(t, "relay")[0].ID
	if err := RenameKey("relay", front, "  个人  "); err != nil {
		t.Fatal(err)
	}
	mkeyTestWant(t, mkeyTestList(t, "relay"), "个人", "团队")

	if err := RenameKey("relay", mkeyTestRef(t, "relay", "团队"), ""); err != nil {
		t.Fatal(err)
	}
	mkeyTestWant(t, mkeyTestList(t, "relay"), "个人", "")

	if err := RenameKey("relay", "nope", "x"); err == nil {
		t.Fatal("unknown key ref accepted")
	}
}

func TestMkeyTestKeysOnSkipsDisabled(t *testing.T) {
	mkeyTestEnv(t)
	mkeyTestWriteConfig(t, mkeyTestLegacy)
	if err := AddKey("relay", "off", "sk-off-1234567890"); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", mkeyTestRef(t, "relay", "off"), false); err != nil {
		t.Fatal(err)
	}
	if on := mkeyTestOn(t, "relay"); len(on) != 1 || on[0] != "" {
		t.Fatalf("KeysOn = %v, want only the front key", on)
	}
}

func TestMkeyTestSaveKeepsOtherKeys(t *testing.T) {
	mkeyTestEnv(t)
	mkeyTestWriteConfig(t, mkeyTestLegacy)
	if err := AddKey("relay", "team", "sk-team-1234567890"); err != nil {
		t.Fatal(err)
	}
	// A record built from scratch, without the key list, must not drop the second key.
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-old-1234567890"}); err != nil {
		t.Fatal(err)
	}
	mkeyTestWant(t, mkeyTestList(t, "relay"), "", "team")
}

func TestMkeyTestAccountTakesNoKeys(t *testing.T) {
	home := mkeyTestEnv(t)
	writeAuth := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(writeAuth), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(writeAuth, []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"tok","refresh_token":"r","account_id":"acct"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("codex", "x", "sk-whatever-1234567890"); err == nil {
		t.Fatal("AddKey on a signed-in account accepted")
	}
}

// mkeyTestOn returns the names of the enabled keys in order.
func mkeyTestOn(t *testing.T, id string) []string {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range p.KeysOn() {
		out = append(out, k.Name)
	}
	return out
}
