package provider

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	keyA = "sk-personal-0001"
	keyB = "sk-team-0002"
	keyC = "sk-spare-0003"
)

// keysRelay sets up an isolated config with one legacy single-key provider.
func keysRelay(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: keyA, Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
}

// keyState summarizes KeyList as "id:name:active:on" strings for comparison.
func keyState(t *testing.T) []string {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range p.KeyList() {
		s := k.ID
		if k.Name != "" {
			s += ":" + k.Name
		}
		if k.Active {
			s += ":active"
		}
		if k.On {
			s += ":on"
		} else {
			s += ":off"
		}
		out = append(out, s)
	}
	return out
}

func kid(key string) string { return keyID(key) }

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustErr(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected an error", what)
	}
	for _, k := range []string{keyA, keyB, keyC} {
		if strings.Contains(err.Error(), k) {
			t.Fatalf("%s: error leaks a full key: %v", what, err)
		}
	}
}

func TestLegacySingleKeyUnchanged(t *testing.T) {
	keysRelay(t)
	p, err := Find("relay")
	mustNoErr(t, err)
	if p.Key != keyA || p.KeyName != "" || len(p.Keys) != 0 {
		t.Fatalf("legacy provider changed: %+v", p)
	}
	list := p.KeyList()
	if len(list) != 1 || !list[0].Active || !list[0].On || list[0].ID != kid(keyA) {
		t.Fatalf("KeyList: %+v", list)
	}
	if list[0].Masked != Mask(keyA) || strings.Contains(list[0].Masked, keyA) {
		t.Fatalf("masked key: %q", list[0].Masked)
	}
	// the file keeps the legacy shape: no key list fields appear
	b, err := os.ReadFile(Path())
	mustNoErr(t, err)
	if strings.Contains(string(b), `"keys"`) || strings.Contains(string(b), `"keyName"`) {
		t.Fatalf("legacy config grew key list fields:\n%s", b)
	}
}

func TestLegacyFileWithKeyList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"providers":[{"id":"relay","name":"Relay","key":"` + keyA + `","keyName":"个人",` +
		`"chat":"https://relay.example/v1","keys":[{"name":"团队","key":"` + keyB + `"},` +
		`{"key":"` + keyC + `","off":true}]}]}`
	mustNoErr(t, os.WriteFile(Path(), []byte(cfg), 0o600))
	p, err := Find("relay")
	mustNoErr(t, err)
	want := []string{kid(keyA) + ":个人:active:on", kid(keyB) + ":团队:on", kid(keyC) + ":off"}
	if got := keyState(t); !slices.Equal(got, want) {
		t.Fatalf("KeyList: got %v want %v", got, want)
	}
	on := p.KeysOn()
	if len(on) != 2 || on[0].Key != keyA || on[0].Name != "个人" || on[1].Key != keyB {
		t.Fatalf("KeysOn: %+v", on)
	}
}

func TestAddKeyTeam(t *testing.T) {
	keysRelay(t)
	mustNoErr(t, AddKey("relay", "团队", "  "+keyB+"  "))
	p, err := Find("relay")
	mustNoErr(t, err)
	list := p.KeyList()
	if len(list) != 2 {
		t.Fatalf("want 2 keys, got %+v", list)
	}
	if !list[0].Active || !list[0].On || list[0].ID != kid(keyA) {
		t.Fatalf("original key should stay front: %+v", list[0])
	}
	if list[1].Active || !list[1].On || list[1].Name != "团队" || list[1].ID != kid(keyB) {
		t.Fatalf("new key: %+v", list[1])
	}
	if p.Key != keyA {
		t.Fatalf("front key changed: %q", p.Key)
	}
	mustErr(t, AddKey("relay", "again", keyB), "duplicate key")
	mustErr(t, AddKey("relay", "again", " "+keyA+" "), "duplicate of front key")
	mustErr(t, AddKey("relay", "blank", "   "), "blank key")
	mustErr(t, AddKey("relay", "", ""), "empty key")
	if len(keyState(t)) != 2 {
		t.Fatalf("failed adds changed the list: %v", keyState(t))
	}
}

func TestAddKeyToEmptyProviderBecomesFront(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mustNoErr(t, Save(Provider{ID: "relay", Name: "Relay", Chat: "http://localhost:11434/v1"}))
	mustNoErr(t, AddKey("relay", "", keyA))
	p, err := Find("relay")
	mustNoErr(t, err)
	if p.Key != keyA || len(p.KeyList()) != 1 || !p.KeyList()[0].Active {
		t.Fatalf("first key should be front: %+v", p.KeyList())
	}
}

func TestUseKey(t *testing.T) {
	keysRelay(t)
	mustNoErr(t, AddKey("relay", "团队", keyB))
	mustNoErr(t, UseKey("relay", kid(keyB)))
	want := []string{kid(keyB) + ":团队:active:on", kid(keyA) + ":on"}
	if got := keyState(t); !slices.Equal(got, want) {
		t.Fatalf("after UseKey: got %v want %v", got, want)
	}
	p, _ := Find("relay")
	if p.Key != keyB {
		t.Fatalf("front key not switched: %q", p.Key)
	}
	// already front: no change
	mustNoErr(t, UseKey("relay", kid(keyB)))
	if got := keyState(t); !slices.Equal(got, want) {
		t.Fatalf("UseKey on front changed the list: %v", got)
	}
	mustErr(t, UseKey("relay", "deadbeef"), "unknown ref")
	mustErr(t, UseKey("relay", keyC), "secret typed as ref")
}

func TestSetKeyOnAndRemoveRules(t *testing.T) {
	keysRelay(t)
	mustNoErr(t, AddKey("relay", "团队", keyB))
	mustNoErr(t, AddKey("relay", "备用", keyC))

	// disabling the front hands the front to the next enabled key
	mustNoErr(t, SetKeyOn("relay", kid(keyA), false))
	want := []string{kid(keyB) + ":团队:active:on", kid(keyA) + ":off", kid(keyC) + ":备用:on"}
	if got := keyState(t); !slices.Equal(got, want) {
		t.Fatalf("after disabling front: got %v want %v", got, want)
	}
	p, _ := Find("relay")
	if on := p.KeysOn(); len(on) != 2 || on[0].Key != keyB || on[1].Key != keyC {
		t.Fatalf("KeysOn order: %+v", on)
	}

	// disabling the last enabled key is refused and changes nothing
	mustNoErr(t, SetKeyOn("relay", kid(keyC), false))
	before := keyState(t)
	mustErr(t, SetKeyOn("relay", kid(keyB), false), "disable last enabled")
	mustErr(t, RemoveKey("relay", kid(keyB)), "remove last enabled")
	if got := keyState(t); !slices.Equal(got, before) {
		t.Fatalf("refused change altered config: %v", got)
	}

	// re-enable one, then the old front can go
	mustNoErr(t, SetKeyOn("relay", kid(keyA), true))
	mustNoErr(t, RemoveKey("relay", kid(keyB)))
	p, _ = Find("relay")
	list := p.KeyList()
	if len(list) != 2 || list[0].ID != kid(keyA) || !list[0].Active || list[1].ID != kid(keyC) || list[1].On {
		t.Fatalf("after removing front: %+v", list)
	}
}

func TestRemoveLastKeyRefused(t *testing.T) {
	keysRelay(t)
	mustErr(t, RemoveKey("relay", kid(keyA)), "remove only key")
	if len(keyState(t)) != 1 {
		t.Fatal("only key was removed")
	}
}

func TestRemoveNonFrontKey(t *testing.T) {
	keysRelay(t)
	mustNoErr(t, AddKey("relay", "团队", keyB))
	mustNoErr(t, RemoveKey("relay", kid(keyB)))
	p, _ := Find("relay")
	if len(p.KeyList()) != 1 || p.Key != keyA {
		t.Fatalf("after removing second key: %+v", p.KeyList())
	}
}

func TestRenameKey(t *testing.T) {
	keysRelay(t)
	mustNoErr(t, AddKey("relay", "", keyB))
	mustNoErr(t, RenameKey("relay", kid(keyB), "  团队  "))
	p, _ := Find("relay")
	if p.KeyList()[1].Name != "团队" {
		t.Fatalf("rename: %+v", p.KeyList())
	}
	mustNoErr(t, RenameKey("relay", kid(keyB), "   "))
	p, _ = Find("relay")
	if p.KeyList()[1].Name != "" {
		t.Fatalf("clear name: %+v", p.KeyList())
	}
	mustNoErr(t, RenameKey("relay", kid(keyA), "个人"))
	p, _ = Find("relay")
	if p.KeyList()[0].Name != "个人" || p.KeyName != "个人" {
		t.Fatalf("front rename: %+v", p.KeyList())
	}
	mustErr(t, RenameKey("relay", "deadbeef", "x"), "rename unknown")
}

func TestUnknownProviderRefused(t *testing.T) {
	keysRelay(t)
	mustErr(t, AddKey("nope", "", keyB), "unknown provider")
	mustErr(t, SetKeyOn("relay", "deadbeef", false), "unknown ref on SetKeyOn")
	mustErr(t, RemoveKey("relay", "deadbeef"), "unknown ref on RemoveKey")
}

func TestAccountRefusesKeys(t *testing.T) {
	signIn(t)
	mustErr(t, AddKey("codex", "", keyB), "AddKey on signed-in account")
}
