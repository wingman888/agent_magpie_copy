package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// keyedRelay saves a custom provider whose current key is k-one.
func keyedRelay(t *testing.T) {
	t.Helper()
	freshHome(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k-one", Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
}

// refOf is the reference of the key with the given secret.
func refOf(t *testing.T, id, key string) string {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range p.KeyList() {
		if k.Key == key {
			return k.Ref
		}
	}
	t.Fatalf("no key %q on %s", key, id)
	return ""
}

// order lists the secrets of a provider's keys, current first.
func order(t *testing.T, id string) []string {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range p.KeyList() {
		s := k.Key
		if k.Off {
			s += "(off)"
		}
		out = append(out, s)
	}
	return out
}

func TestKeyRefIsSHA256Prefix(t *testing.T) {
	sum := sha256.Sum256([]byte("sk-abc"))
	want := hex.EncodeToString(sum[:5])
	if got := KeyRef("sk-abc"); got != want || len(got) != 10 {
		t.Fatalf("KeyRef = %q, want %q", got, want)
	}
}

func TestAddKeyRules(t *testing.T) {
	keyedRelay(t)
	if err := AddKey("relay", "team", "k-two", ""); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "again", "k-two", ""); err == nil {
		t.Error("duplicate key was accepted")
	}
	if err := AddKey("relay", "dup-current", "k-one", ""); err == nil {
		t.Error("the current key was added twice")
	}
	if err := AddKey("relay", "bad", "k-three", Protocol("gemini")); err == nil {
		t.Error("unknown protocol was accepted")
	}
	if got := strings.Join(order(t, "relay"), ","); got != "k-one,k-two" {
		t.Errorf("order = %s", got)
	}
	p, _ := Find("relay")
	if p.Keys[0].Name != "team" || p.Keys[0].Off {
		t.Errorf("added key = %+v, want enabled and named", p.Keys[0])
	}
}

func TestFirstKeyBecomesCurrent(t *testing.T) {
	freshHome(t)
	if err := Save(Provider{ID: "bare", Name: "Bare", Chat: "https://bare.example/v1", Key: ""}); err == nil {
		t.Fatal("a keyless remote provider was saved")
	}
	if err := Save(Provider{ID: "bare", Name: "Bare", Chat: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("bare", "first", "k-first", ""); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("bare")
	if p.Key != "k-first" || p.KeyName != "first" || len(p.Keys) != 0 {
		t.Errorf("provider = %+v, want k-first as the current key", p)
	}
}

func TestLastEnabledKeyCannotBeTurnedOffOrRemoved(t *testing.T) {
	keyedRelay(t)
	if err := AddKey("relay", "team", "k-two", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", refOf(t, "relay", "k-two"), false); err != nil {
		t.Fatal(err)
	}
	// k-one is the only enabled key now
	if err := SetKeyOn("relay", refOf(t, "relay", "k-one"), false); err == nil {
		t.Error("the last enabled key was turned off")
	}
	if err := RemoveKey("relay", refOf(t, "relay", "k-one")); err == nil {
		t.Error("the last enabled key was removed")
	}
	if got := strings.Join(order(t, "relay"), ","); got != "k-one,k-two(off)" {
		t.Errorf("order changed by a refused call: %s", got)
	}
}

func TestTurningOffCurrentHandsOverToNextEnabled(t *testing.T) {
	keyedRelay(t)
	if err := AddKey("relay", "team", "k-two", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", refOf(t, "relay", "k-one"), false); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if p.Key != "k-two" {
		t.Fatalf("current = %q, want k-two", p.Key)
	}
	if got := strings.Join(order(t, "relay"), ","); got != "k-two,k-one(off)" {
		t.Errorf("order = %s", got)
	}
	if err := SetKeyOn("relay", refOf(t, "relay", "k-one"), true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order(t, "relay"), ","); got != "k-two,k-one" {
		t.Errorf("re-enabled order = %s", got)
	}
}

func TestRemoveCurrentHandsOver(t *testing.T) {
	keyedRelay(t)
	if err := AddKey("relay", "team", "k-two", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", refOf(t, "relay", "k-one")); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if p.Key != "k-two" || p.KeyProtocol != Anthropic || p.KeyName != "team" || len(p.Keys) != 0 {
		t.Errorf("provider after removal = %+v", p)
	}
}

func TestUseKeyKeepsOthersAndProtocols(t *testing.T) {
	keyedRelay(t)
	if err := SetKeyProtocol("relay", refOf(t, "relay", "k-one"), Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "team", "k-two", ""); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "oai", "k-three", Chat); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", refOf(t, "relay", "k-three")); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if p.Key != "k-three" || p.KeyName != "oai" || p.KeyProtocol != Chat {
		t.Fatalf("current = %+v", p)
	}
	if got := strings.Join(order(t, "relay"), ","); got != "k-three,k-one,k-two" {
		t.Errorf("order = %s", got)
	}
	if p.Keys[0].Protocol != Anthropic || p.Keys[1].Name != "team" {
		t.Errorf("others lost their names or limits: %+v", p.Keys)
	}
}

func TestRenameAndSetProtocol(t *testing.T) {
	keyedRelay(t)
	if err := AddKey("relay", "", "k-two", ""); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("relay", refOf(t, "relay", "k-two"), "personal"); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", refOf(t, "relay", "k-two"), Responses); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", refOf(t, "relay", "k-two"), Protocol("gemini")); err == nil {
		t.Error("gemini accepted as a key protocol")
	}
	p, _ := Find("relay")
	if p.Keys[0].Name != "personal" || p.Keys[0].Protocol != Responses {
		t.Errorf("key = %+v", p.Keys[0])
	}
	if err := SetKeyProtocol("relay", refOf(t, "relay", "k-two"), ""); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Keys[0].Protocol != "" {
		t.Errorf("protocol not cleared: %+v", p.Keys[0])
	}
}

func TestSaveRejectsUnknownKeyProtocol(t *testing.T) {
	freshHome(t)
	err := Save(Provider{ID: "bad", Name: "Bad", Key: "k", Chat: "https://bad.example/v1", KeyProtocol: "gemini"})
	if err == nil {
		t.Error("unknown keyProtocol saved")
	}
	err = Save(Provider{ID: "bad", Name: "Bad", Key: "k", Chat: "https://bad.example/v1",
		Keys: []KeyEntry{{Key: "k2", Protocol: "bogus"}}})
	if err == nil {
		t.Error("unknown key protocol saved")
	}
}

func TestSaveKeepsStoredKeys(t *testing.T) {
	keyedRelay(t)
	if err := AddKey("relay", "team", "k-two", ""); err != nil {
		t.Fatal(err)
	}
	// saving the same provider again, with the same current key, keeps the others
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k-one", Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order(t, "relay"), ","); got != "k-one,k-two" {
		t.Errorf("order after re-save = %s", got)
	}
}
