package provider

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func keyProtoSetup(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test",
		Key: "sk-first-1111", KeyName: "First"}); err != nil {
		t.Fatal(err)
	}
}

func protoOf(t *testing.T, key string) Protocol {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range p.KeyList() {
		if k.ID == keyID(key) {
			return k.Protocol
		}
	}
	t.Fatalf("no key %s in %+v", key, p.KeyList())
	return ""
}

func TestAddKeyProtocol(t *testing.T) {
	keyProtoSetup(t)
	if err := AddKey("relay", "Bird", "sk-bird-3333", "carrier-pigeon"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	p, _ := Find("relay")
	if len(p.Keys) != 0 {
		t.Fatalf("saved anyway: %+v", p.Keys)
	}
	if err := AddKey("relay", "Claude", "sk-anth-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	if got := protoOf(t, "sk-anth-2222"); got != Anthropic {
		t.Fatalf("protocol %q", got)
	}
	if got := protoOf(t, "sk-first-1111"); got != "" {
		t.Fatalf("first key got %q", got)
	}
	// it's in the file, under keys[].protocol
	b, _ := os.ReadFile(Path())
	if !strings.Contains(string(b), `"protocol": "anthropic"`) || strings.Contains(string(b), `"keyProtocol"`) {
		t.Fatalf("file: %s", b)
	}
	// and KeyList's JSON leaves an empty one out
	p, _ = Find("relay")
	j, _ := json.Marshal(p.KeyList())
	if strings.Count(string(j), `"protocol"`) != 1 {
		t.Fatalf("key list: %s", j)
	}
	// a key added as the first gets KeyProtocol
	if err := Save(Provider{ID: "solo", Name: "Solo", Chat: "http://localhost:1/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("solo", "", "sk-solo-4444", Chat); err != nil {
		t.Fatal(err)
	}
	if s, _ := Find("solo"); s.Key != "sk-solo-4444" || s.KeyProtocol != Chat {
		t.Fatalf("solo: %+v", s)
	}
}

func TestSetKeyProtocolFollowsTheKey(t *testing.T) {
	keyProtoSetup(t)
	if err := AddKey("relay", "Claude", "sk-anth-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Spare", "sk-spare-5555", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", keyID("sk-first-1111"), Chat); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if ks := p.KeyList(); ks[0].Protocol != Chat || !ks[0].Active || p.KeyProtocol != Chat {
		t.Fatalf("keys %+v", ks)
	}
	if err := SetKeyProtocol("relay", keyID("sk-first-1111"), "carrier-pigeon"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	if err := SetKeyProtocol("relay", "nope", Chat); err == nil {
		t.Fatal("an unknown key was accepted")
	}
	if protoOf(t, "sk-first-1111") != Chat {
		t.Fatal("a failed call changed the key")
	}

	// switching keys: each keeps its own
	if err := UseKey("relay", keyID("sk-anth-2222")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-anth-2222" || p.KeyProtocol != Anthropic {
		t.Fatalf("after use: %+v", p)
	}
	if protoOf(t, "sk-first-1111") != Chat || protoOf(t, "sk-anth-2222") != Anthropic || protoOf(t, "sk-spare-5555") != "" {
		t.Fatalf("after use: %+v", p.KeyList())
	}
	if ks := p.KeyList(); ks[0].Protocol != Anthropic {
		t.Fatalf("after use: %+v", ks)
	}

	// turning the first off: the next takes its place, with its own protocol
	if err := SetKeyOn("relay", keyID("sk-anth-2222"), false); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-first-1111" || p.KeyProtocol != Chat {
		t.Fatalf("after off: %+v", p)
	}
	if protoOf(t, "sk-anth-2222") != Anthropic || protoOf(t, "sk-spare-5555") != "" {
		t.Fatalf("after off: %+v", p.KeyList())
	}

	// removing the first: the spare takes over, for all protocols
	if err := RemoveKey("relay", keyID("sk-first-1111")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-spare-5555" || p.KeyProtocol != "" || protoOf(t, "sk-anth-2222") != Anthropic {
		t.Fatalf("after remove: %+v", p)
	}

	// clearing one
	if err := SetKeyProtocol("relay", keyID("sk-anth-2222"), ""); err != nil {
		t.Fatal(err)
	}
	if protoOf(t, "sk-anth-2222") != "" {
		t.Fatal("not cleared")
	}
}

func TestWithKey(t *testing.T) {
	p := Provider{ID: "r", Name: "R", Chat: "https://r.test/v1", Responses: "https://r.test/v1", Anthropic: "https://r.test",
		Key: "a", KeyName: "A"}
	q := p.WithKey(KeyAccount{Name: "B", Key: "b", Protocol: Chat})
	if q.Key != "b" || q.KeyName != "B" || q.KeyProtocol != Chat || q.Chat == "" || q.Responses != "" || q.Anthropic != "" {
		t.Fatalf("chat key: %+v", q)
	}
	if got := q.Speaks(); len(got) != 1 || got[0] != Chat {
		t.Fatalf("speaks %v", got)
	}
	q = p.WithKey(KeyAccount{Key: "c", Protocol: Anthropic})
	if q.Chat != "" || q.Responses != "" || q.Anthropic == "" {
		t.Fatalf("anthropic key: %+v", q)
	}
	q = p.WithKey(KeyAccount{Key: "d"})
	if q.Key != "d" || q.KeyProtocol != "" || q.Chat == "" || q.Responses == "" || q.Anthropic == "" {
		t.Fatalf("key for all: %+v", q)
	}
	if p.Key != "a" || p.Anthropic == "" {
		t.Fatal("WithKey changed the original")
	}
	if ks := (Provider{Key: "a", KeyProtocol: Anthropic, Keys: []KeyAccount{{Key: "b", Protocol: Chat}}}).KeysOn(); ks[0].Protocol != Anthropic || ks[1].Protocol != Chat {
		t.Fatalf("keys on %+v", ks)
	}
}
