package provider

import (
	"strings"
	"testing"
)

// keysRelay saves a custom relay whose current key is cur, with no other keys.
func keysRelay(t *testing.T, cur string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: cur, Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
}

func mustFind(t *testing.T) Provider {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

// names lists the key names in order, the current first, marking off keys.
func names(p Provider) []string {
	var out []string
	for _, k := range p.KeyList() {
		n := k.Name
		if k.Current {
			n = "*" + n
		}
		if k.Off {
			n = "-" + n
		}
		out = append(out, n)
	}
	return out
}

func TestKeyRefIsTenHexOfSHA256(t *testing.T) {
	// sha256("abc") starts ba7816bf 8f01cfea 4141… ; five bytes of it
	if got := KeyRef("abc"); got != "ba7816bf8f" {
		t.Fatalf("KeyRef(abc) = %q, want ba7816bf8f", got)
	}
	if len(KeyRef("anything")) != 10 {
		t.Fatal("a key ref is 10 characters")
	}
}

func TestAddKeyFirstBecomesCurrent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// a local server needs no key, so the provider starts without one
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "http://localhost:1234/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "personal", "sk-personal", ""); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t)
	if p.Key != "sk-personal" || p.KeyName != "personal" {
		t.Fatalf("first key = %q/%q, want the current key", p.Key, p.KeyName)
	}
	if err := AddKey("relay", "team", "sk-team", Anthropic); err != nil {
		t.Fatal(err)
	}
	p = mustFind(t)
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-team" || p.Keys[0].Off || p.Keys[0].Protocol != Anthropic {
		t.Fatalf("second key = %+v, want enabled, anthropic, after the first", p.Keys)
	}
}

func TestAddKeyRejectsDuplicatesAndUnknownProtocols(t *testing.T) {
	keysRelay(t, "sk-1")
	if err := AddKey("relay", "again", "sk-1", ""); err == nil {
		t.Error("a key already on the provider was accepted")
	}
	if err := AddKey("relay", "b", "sk-2", ""); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "b2", "sk-2", ""); err == nil {
		t.Error("a repeated extra key was accepted")
	}
	if err := AddKey("relay", "g", "sk-3", Gemini); err == nil {
		t.Error("gemini is not a key limit, but was accepted")
	}
	if err := AddKey("relay", "x", "sk-4", "bogus"); err == nil {
		t.Error("an unknown protocol was accepted")
	}
	if err := AddKey("relay", "x", "  ", ""); err == nil {
		t.Error("an empty key was accepted")
	}
	if err := AddKey("nope", "x", "sk-5", ""); err == nil {
		t.Error("a key was added to an unknown provider")
	}
	if got := len(mustFind(t).KeyList()); got != 2 {
		t.Fatalf("%d keys after the rejected adds, want 2", got)
	}
}

func TestUseKeyKeepsOthersNamesAndProtocols(t *testing.T) {
	keysRelay(t, "sk-1")
	if err := RenameKey("relay", KeyRef("sk-1"), "personal"); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "team", "sk-2", Chat); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "spare", "sk-3", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", KeyRef("sk-2")); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t)
	if p.Key != "sk-2" || p.KeyName != "team" || p.KeyProtocol != Chat {
		t.Fatalf("current = %q/%q/%q, want sk-2 team chat", p.Key, p.KeyName, p.KeyProtocol)
	}
	// the old current key stays enabled and goes first among the others
	if got := strings.Join(names(p), ","); got != "*team,personal,spare" {
		t.Fatalf("keys = %s, want *team,personal,spare", got)
	}
	if p.Keys[0].Protocol != "" || p.Keys[1].Protocol != Anthropic {
		t.Fatalf("protocol limits lost: %+v", p.Keys)
	}
	// using the current key again changes nothing
	if err := UseKey("relay", KeyRef("sk-2")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(mustFind(t)), ","); got != "*team,personal,spare" {
		t.Fatalf("keys after re-using current = %s", got)
	}
}

func TestSetKeyOnHandsOverFromCurrent(t *testing.T) {
	keysRelay(t, "sk-1")
	if err := AddKey("relay", "two", "sk-2", ""); err != nil {
		t.Fatal(err)
	}
	// turning off the current key hands the role to the next enabled one
	if err := SetKeyOn("relay", KeyRef("sk-1"), false); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t)
	if p.Key != "sk-2" {
		t.Fatalf("current after off = %q, want sk-2", p.Key)
	}
	if got := strings.Join(names(p), ","); got != "*two,-"+"" {
		// the old current key is kept, off, with its (empty) name
		t.Fatalf("keys = %s", got)
	}
	// now sk-2 is the last enabled key: it cannot be turned off, and nothing changes
	if err := SetKeyOn("relay", KeyRef("sk-2"), false); err == nil {
		t.Fatal("the last enabled key was turned off")
	}
	if mustFind(t).Key != "sk-2" {
		t.Fatal("a refused change still moved the current key")
	}
	// turning a spare back on works
	if err := SetKeyOn("relay", KeyRef("sk-1"), true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(mustFind(t)), ","); got != "*two,"+"" {
		t.Fatalf("keys after re-enable = %s", got)
	}
}

func TestSetKeyOnCurrentWithoutOtherEnabledKeyChangesNothing(t *testing.T) {
	keysRelay(t, "sk-1")
	if err := SetKeyOn("relay", KeyRef("sk-1"), false); err == nil {
		t.Fatal("the only key was turned off")
	}
	p := mustFind(t)
	if p.Key != "sk-1" || len(p.Keys) != 0 {
		t.Fatalf("a refused change altered the provider: %+v", p)
	}
}

func TestRemoveKeyLastEnabledAndCurrentHandover(t *testing.T) {
	keysRelay(t, "sk-1")
	if err := RemoveKey("relay", KeyRef("sk-1")); err == nil {
		t.Fatal("the last enabled key was removed")
	}
	if mustFind(t).Key != "sk-1" {
		t.Fatal("a refused removal changed the key")
	}
	if err := AddKey("relay", "two", "sk-2", ""); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "three", "sk-3", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", KeyRef("sk-2"), false); err != nil {
		t.Fatal(err)
	}
	// removing the current key: the next enabled one takes over, skipping the off one
	if err := RemoveKey("relay", KeyRef("sk-1")); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t)
	if p.Key != "sk-3" {
		t.Fatalf("current after remove = %q, want sk-3", p.Key)
	}
	if got := strings.Join(names(p), ","); got != "*three,-two" {
		t.Fatalf("keys = %s, want *three,-two", got)
	}
	// an off key can always go
	if err := RemoveKey("relay", KeyRef("sk-2")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", KeyRef("sk-404")); err == nil {
		t.Fatal("an unknown key was removed")
	}
}

func TestRemoveKeyNonCurrentLeavesCurrent(t *testing.T) {
	keysRelay(t, "sk-1")
	if err := AddKey("relay", "two", "sk-2", ""); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", KeyRef("sk-2")); err != nil {
		t.Fatal(err)
	}
	if got := len(mustFind(t).KeyList()); got != 1 {
		t.Fatalf("%d keys left, want 1", got)
	}
}

func TestRenameKey(t *testing.T) {
	keysRelay(t, "sk-1")
	if err := AddKey("relay", "old", "sk-2", ""); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("relay", KeyRef("sk-1"), "  personal "); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("relay", KeyRef("sk-2"), "team"); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t)
	if p.KeyName != "personal" || p.Keys[0].Name != "team" {
		t.Fatalf("names = %q, %q", p.KeyName, p.Keys[0].Name)
	}
	if err := RenameKey("relay", KeyRef("sk-404"), "x"); err == nil {
		t.Fatal("renamed an unknown key")
	}
}

func TestKeyCandidatesOrderAndOffKeysNeverListed(t *testing.T) {
	keysRelay(t, "sk-1")
	AddKey("relay", "two", "sk-2", "")
	AddKey("relay", "three", "sk-3", "")
	SetKeyOn("relay", KeyRef("sk-2"), false)
	var got []string
	for _, c := range mustFind(t).KeyCandidates() {
		got = append(got, c.Key)
	}
	if strings.Join(got, ",") != "sk-1,sk-3" {
		t.Fatalf("candidates = %v, want sk-1,sk-3", got)
	}
}

// Saving the provider from a form that knows nothing of its extra keys keeps them.
func TestSaveKeepsExtraKeys(t *testing.T) {
	keysRelay(t, "sk-1")
	if err := AddKey("relay", "two", "sk-2", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "sk-1", Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	if got := len(mustFind(t).Keys); got != 1 {
		t.Fatalf("a save dropped the extra key: %d left", got)
	}
}

// A save cannot introduce a duplicate key or an unknown protocol.
func TestSaveRejectsBadKeys(t *testing.T) {
	keysRelay(t, "sk-1")
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "sk-1", Chat: "https://relay.example.com/v1",
		Keys: []ExtraKey{{Key: "sk-1"}}}); err == nil {
		t.Error("a duplicate key was saved")
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "sk-1", Chat: "https://relay.example.com/v1",
		Keys: []ExtraKey{{Key: "sk-2", Protocol: "gemini"}}}); err == nil {
		t.Error("an unknown protocol was saved")
	}
}
