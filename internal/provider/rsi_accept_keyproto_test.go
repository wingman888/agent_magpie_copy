package provider_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func isolate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func saveRelay(t *testing.T) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-oai-1111"}); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptAddKeyProtocol(t *testing.T) {
	isolate(t)
	saveRelay(t)
	if err := provider.AddKey("relay", "Claude", "sk-ant-2222", provider.Anthropic); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("relay")
	ks := p.KeyList()
	if len(ks) != 2 || ks[0].Protocol != "" || ks[1].Protocol != provider.Anthropic {
		t.Fatalf("keys %+v", ks)
	}
	if p.Keys[0].Protocol != provider.Anthropic {
		t.Fatalf("%+v", p.Keys)
	}
}

func TestAcceptAddKeyFirstGetsKeyProtocol(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "A", "sk-a-1", provider.Responses); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("relay")
	if p.Key != "sk-a-1" || p.KeyProtocol != provider.Responses || len(p.Keys) != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestAcceptAddKeyUnknownProtocol(t *testing.T) {
	isolate(t)
	saveRelay(t)
	err := provider.AddKey("relay", "", "sk-x-3333", "carrier-pigeon")
	if err == nil || !strings.Contains(err.Error(), `unknown protocol "carrier-pigeon"`) {
		t.Fatalf("err %v", err)
	}
	p, _ := provider.Find("relay")
	if len(p.Keys) != 0 || len(p.KeyList()) != 1 {
		t.Fatalf("saved something: %+v", p)
	}
}

func TestAcceptSetKeyProtocol(t *testing.T) {
	isolate(t)
	saveRelay(t)
	if err := provider.AddKey("relay", "Claude", "sk-ant-2222", provider.Anthropic); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("relay")
	ks := p.KeyList()
	if err := provider.SetKeyProtocol("relay", ks[0].ID, provider.Chat); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	if got := p.KeyList(); got[0].Protocol != provider.Chat || got[1].Protocol != provider.Anthropic {
		t.Fatalf("%+v", got)
	}
	if err := provider.SetKeyProtocol("relay", ks[1].ID, ""); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	if got := p.KeyList(); got[1].Protocol != "" {
		t.Fatalf("%+v", got)
	}
	if err := provider.SetKeyProtocol("relay", "nope", provider.Chat); err == nil || !strings.Contains(err.Error(), "has no such key") {
		t.Fatalf("err %v", err)
	}
	if err := provider.SetKeyProtocol("relay", ks[0].ID, "bogus"); err == nil || !strings.Contains(err.Error(), "unknown protocol") {
		t.Fatalf("err %v", err)
	}
	p, _ = provider.Find("relay")
	if p.KeyList()[0].Protocol != provider.Chat {
		t.Fatal("changed on invalid protocol")
	}
}

func TestAcceptProtocolFollowsKey(t *testing.T) {
	isolate(t)
	saveRelay(t)
	if err := provider.AddKey("relay", "Claude", "sk-ant-2222", provider.Anthropic); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("relay")
	ks := p.KeyList()
	if err := provider.SetKeyProtocol("relay", ks[0].ID, provider.Chat); err != nil {
		t.Fatal(err)
	}
	if err := provider.UseKey("relay", ks[1].ID); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	if p.Key != "sk-ant-2222" || p.KeyProtocol != provider.Anthropic || len(p.Keys) != 1 || p.Keys[0].Protocol != provider.Chat || p.Keys[0].Key != "sk-oai-1111" {
		t.Fatalf("after use: %+v", p)
	}
	if on := p.KeysOn(); on[0].Key != "sk-ant-2222" || on[0].Protocol != provider.Anthropic {
		t.Fatalf("KeysOn %+v", on)
	}
	// turn the first off: the next one is promoted with its protocol
	if err := provider.SetKeyOn("relay", p.KeyList()[0].ID, false); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	if p.Key != "sk-oai-1111" || p.KeyProtocol != provider.Chat {
		t.Fatalf("after off: %+v", p)
	}
	if got := p.KeyList(); got[0].Protocol != provider.Chat || got[1].Protocol != provider.Anthropic {
		t.Fatalf("%+v", got)
	}
}

func TestAcceptWithKey(t *testing.T) {
	p := provider.Provider{ID: "r", Chat: "c", Responses: "r", Anthropic: "a", Key: "k0"}
	q := p.WithKey(provider.KeyAccount{Name: "n", Key: "k1", Protocol: provider.Chat})
	if q.Key != "k1" || q.KeyName != "n" || q.KeyProtocol != provider.Chat || q.Chat == "" || q.Anthropic != "" || q.Responses != "" {
		t.Fatalf("%+v", q)
	}
	q = p.WithKey(provider.KeyAccount{Key: "k2", Protocol: provider.Anthropic})
	if q.Anthropic == "" || q.Chat != "" || q.Responses != "" {
		t.Fatalf("%+v", q)
	}
	q = p.WithKey(provider.KeyAccount{Key: "k"})
	if q.Chat == "" || q.Anthropic == "" || q.Responses == "" || q.KeyProtocol != "" {
		t.Fatalf("%+v", q)
	}
	if p.Anthropic == "" {
		t.Fatal("original modified")
	}
}

func TestAcceptKeyProtocolJSON(t *testing.T) {
	b, _ := json.Marshal(provider.Provider{ID: "r", Key: "k", Keys: []provider.KeyAccount{{Key: "a"}}})
	if strings.Contains(string(b), `"protocol"`) || strings.Contains(string(b), `"keyProtocol"`) {
		t.Fatalf("empty protocol emitted: %s", b)
	}
	b, _ = json.Marshal(provider.Provider{ID: "r", Key: "k", KeyProtocol: provider.Chat, Keys: []provider.KeyAccount{{Key: "a", Protocol: provider.Anthropic}}})
	if !strings.Contains(string(b), `"keyProtocol":"`) || !strings.Contains(string(b), `"protocol":"`) {
		t.Fatalf("%s", b)
	}
	var p provider.Provider
	if err := json.Unmarshal([]byte(`{"id":"r","key":"k","keys":[{"key":"a"}]}`), &p); err != nil {
		t.Fatal(err)
	}
	for _, k := range p.KeyList() {
		if k.Protocol != "" {
			t.Fatalf("%+v", p.KeyList())
		}
	}
}
