package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// keysHome points magpie's config at a fresh directory, so the tests never
// touch the real providers.json or the agents' sign-ins.
func keysHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// newRelay saves a custom relay whose current key is named "personal".
func newRelay(t *testing.T) {
	t.Helper()
	err := Save(Provider{ID: "relay", Name: "Relay", Anthropic: "https://relay.example.com",
		Key: "sk-personal-0001", KeyName: "personal"})
	if err != nil {
		t.Fatal(err)
	}
}

// listed gives the key names in priority order, "(off)" marking disabled ones.
func listed(t *testing.T) []string {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range p.KeyList() {
		s := k.Name
		if k.Off {
			s += "(off)"
		}
		out = append(out, s)
	}
	return out
}

func currentKey(t *testing.T) string {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	return p.Key
}

func refFor(t *testing.T, key string) string {
	t.Helper()
	return KeyRef(key)
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustFail(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected an error", what)
	}
}

func checkList(t *testing.T, want []string) {
	t.Helper()
	if got := listed(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

func TestKeyRefIsTenLowercaseHex(t *testing.T) {
	sum := sha256.Sum256([]byte("sk-1"))
	want := hex.EncodeToString(sum[:5])
	if got := KeyRef("sk-1"); got != want || len(got) != 10 {
		t.Fatalf("KeyRef = %q, want %q", got, want)
	}
}

func TestAddKeyAppendsEnabledAndRejectsBadInput(t *testing.T) {
	keysHome(t)
	newRelay(t)
	mustOK(t, AddKey("relay", "team", "sk-team-0002", Anthropic))
	checkList(t, []string{"personal", "team"})

	mustFail(t, "duplicate key", AddKey("relay", "again", "sk-team-0002", ""))
	mustFail(t, "duplicate of current", AddKey("relay", "again", "sk-personal-0001", ""))
	mustFail(t, "unknown protocol", AddKey("relay", "x", "sk-x-0003", "bogus"))
	mustFail(t, "gemini is not a key protocol", AddKey("relay", "x", "sk-x-0003", Gemini))
	mustFail(t, "empty key", AddKey("relay", "x", "  ", ""))
	mustFail(t, "unknown provider", AddKey("nobody", "x", "sk-x-0003", ""))
	checkList(t, []string{"personal", "team"})

	p, _ := Find("relay")
	if p.KeyList()[1].Protocol != Anthropic {
		t.Fatalf("protocol of added key = %q", p.KeyList()[1].Protocol)
	}
}

func TestAddKeyToProviderWithoutKeyMakesItCurrent(t *testing.T) {
	keysHome(t)
	// a local server needs no key, so it can be saved without one
	mustOK(t, Save(Provider{ID: "relay", Name: "Relay", Chat: "http://localhost:11434/v1"}))
	mustOK(t, AddKey("relay", "first", "sk-first-0001", ""))
	if got := currentKey(t); got != "sk-first-0001" {
		t.Fatalf("current key = %q", got)
	}
	checkList(t, []string{"first"})
}

func TestLastEnabledKeyCannotBeOffOrRemoved(t *testing.T) {
	keysHome(t)
	newRelay(t)
	mustOK(t, AddKey("relay", "team", "sk-team-0002", ""))
	mustOK(t, SetKeyOn("relay", refFor(t, "sk-team-0002"), false))
	checkList(t, []string{"personal", "team(off)"})

	mustFail(t, "turning off the last enabled key", SetKeyOn("relay", refFor(t, "sk-personal-0001"), false))
	mustFail(t, "removing the last enabled key", RemoveKey("relay", refFor(t, "sk-personal-0001")))
	checkList(t, []string{"personal", "team(off)"})

	// a disabled key can go
	mustOK(t, RemoveKey("relay", refFor(t, "sk-team-0002")))
	checkList(t, []string{"personal"})
	mustFail(t, "removing the only key", RemoveKey("relay", refFor(t, "sk-personal-0001")))
	checkList(t, []string{"personal"})
}

func TestTurningOffCurrentPromotesNext(t *testing.T) {
	keysHome(t)
	newRelay(t)
	mustOK(t, AddKey("relay", "team", "sk-team-0002", ""))
	mustOK(t, AddKey("relay", "backup", "sk-backup-0003", ""))
	checkList(t, []string{"personal", "team", "backup"})

	// the next enabled key takes over; the old current one stays, off, at the front of the rest
	mustOK(t, SetKeyOn("relay", refFor(t, "sk-personal-0001"), false))
	checkList(t, []string{"team", "personal(off)", "backup"})
	if got := currentKey(t); got != "sk-team-0002" {
		t.Fatalf("current key = %q, want the team key", got)
	}

	// with the current key off, turning on the others keeps the order
	mustOK(t, SetKeyOn("relay", refFor(t, "sk-team-0002"), false))
	checkList(t, []string{"backup", "team(off)", "personal(off)"})
	if got := currentKey(t); got != "sk-backup-0003" {
		t.Fatalf("current key = %q, want the backup key", got)
	}

	mustOK(t, SetKeyOn("relay", refFor(t, "sk-personal-0001"), true))
	checkList(t, []string{"backup", "team(off)", "personal"})
}

func TestTurningOffWithNoOtherEnabledChangesNothing(t *testing.T) {
	keysHome(t)
	newRelay(t)
	mustOK(t, AddKey("relay", "team", "sk-team-0002", ""))
	mustOK(t, SetKeyOn("relay", refFor(t, "sk-team-0002"), false))
	before := currentKey(t)

	mustFail(t, "current key with nothing else enabled", SetKeyOn("relay", refFor(t, "sk-personal-0001"), false))
	if currentKey(t) != before {
		t.Fatal("current key changed on a refused change")
	}
	checkList(t, []string{"personal", "team(off)"})
}

func TestUseKeyKeepsOthersAndProtocols(t *testing.T) {
	keysHome(t)
	newRelay(t)
	mustOK(t, AddKey("relay", "team", "sk-team-0002", Anthropic))
	mustOK(t, AddKey("relay", "backup", "sk-backup-0003", Responses))
	mustOK(t, SetKeyOn("relay", refFor(t, "sk-team-0002"), false))

	mustFail(t, "switching to an off key", UseKey("relay", refFor(t, "sk-team-0002")))
	mustOK(t, UseKey("relay", refFor(t, "sk-backup-0003")))
	checkList(t, []string{"backup", "personal", "team(off)"})

	p, _ := Find("relay")
	info := p.KeyList()
	if info[0].Protocol != Responses || info[2].Protocol != Anthropic || info[1].Protocol != "" {
		t.Fatalf("protocols lost: %+v", info)
	}
	if !info[0].Current || info[1].Current {
		t.Fatalf("current flags wrong: %+v", info)
	}
}

func TestRemoveCurrentPromotesNext(t *testing.T) {
	keysHome(t)
	newRelay(t)
	mustOK(t, AddKey("relay", "team", "sk-team-0002", ""))
	mustOK(t, RemoveKey("relay", refFor(t, "sk-personal-0001")))
	checkList(t, []string{"team"})
	if got := currentKey(t); got != "sk-team-0002" {
		t.Fatalf("current key = %q", got)
	}
}

func TestRenameKeyAndSetProtocol(t *testing.T) {
	keysHome(t)
	newRelay(t)
	mustOK(t, AddKey("relay", "team", "sk-team-0002", ""))

	mustOK(t, RenameKey("relay", refFor(t, "sk-personal-0001"), "mine"))
	mustOK(t, RenameKey("relay", refFor(t, "sk-team-0002"), "Team"))
	checkList(t, []string{"mine", "Team"})

	mustOK(t, SetKeyProtocol("relay", refFor(t, "sk-team-0002"), Anthropic))
	p, _ := Find("relay")
	if p.KeyList()[1].Protocol != Anthropic {
		t.Fatal("protocol not set")
	}
	mustOK(t, SetKeyProtocol("relay", refFor(t, "sk-team-0002"), ""))
	p, _ = Find("relay")
	if p.KeyList()[1].Protocol != "" {
		t.Fatal("protocol not cleared")
	}
	mustFail(t, "gemini limit", SetKeyProtocol("relay", refFor(t, "sk-team-0002"), Gemini))
	mustFail(t, "unknown key", RenameKey("relay", "0000000000", "x"))
}

func TestResolveKeyByFingerprintOrName(t *testing.T) {
	keysHome(t)
	newRelay(t)
	mustOK(t, AddKey("relay", "team", "sk-team-0002", ""))
	p, _ := Find("relay")
	if ref, err := p.ResolveKey("Team"); err != nil || ref != refFor(t, "sk-team-0002") {
		t.Fatalf("by name: %q, %v", ref, err)
	}
	if ref, err := p.ResolveKey(refFor(t, "sk-personal-0001")); err != nil || ref != refFor(t, "sk-personal-0001") {
		t.Fatalf("by fingerprint: %q, %v", ref, err)
	}
	if _, err := p.ResolveKey("nobody"); err == nil {
		t.Fatal("unknown name accepted")
	}
}

func TestSaveRejectsDuplicateAndUnknownProtocols(t *testing.T) {
	keysHome(t)
	base := Provider{ID: "relay", Name: "Relay", Anthropic: "https://relay.example.com"}

	dup := base
	dup.Key = "sk-a"
	dup.Keys = []ExtraKey{{Key: "sk-a"}}
	mustFail(t, "key listed twice", Save(dup))

	badProto := base
	badProto.Key = "sk-a"
	badProto.Keys = []ExtraKey{{Key: "sk-b", Protocol: "bogus"}}
	mustFail(t, "unknown protocol in file", Save(badProto))

	badCurrent := base
	badCurrent.Key = "sk-a"
	badCurrent.KeyProtocol = "gemini"
	mustFail(t, "unknown current protocol", Save(badCurrent))
}

func TestOldSingleKeyFileStillLoads(t *testing.T) {
	keysHome(t)
	path := Path()
	mustOK(t, os.MkdirAll(filepath.Dir(path), 0o755))
	old := `{"providers":[{"id":"relay","name":"Relay","anthropic":"https://relay.example.com","key":"sk-old-0001"}]}`
	mustOK(t, os.WriteFile(path, []byte(old), 0o600))

	p, err := Find("relay")
	mustOK(t, err)
	if p.Key != "sk-old-0001" || len(p.Keys) != 0 || len(p.KeyList()) != 1 {
		t.Fatalf("old file loaded as %+v", p)
	}
	mustOK(t, AddKey("relay", "team", "sk-team-0002", ""))
	checkList(t, []string{"", ""})
}
