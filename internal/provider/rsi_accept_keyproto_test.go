package provider_test

import (
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func isolate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func base(t *testing.T) {
	t.Helper()
	isolate(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1",
		Anthropic: "https://relay.test", Responses: "https://relay.test/v1", Key: "sk-oai-1111"}); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptAddKeyProtocol(t *testing.T) {
	base(t)
	if err := provider.AddKey("relay", "bad", "sk-x-3333", "carrier-pigeon"); err == nil {
		t.Fatal("unknown protocol accepted")
	}
	p, _ := provider.Find("relay")
	if len(p.KeyList()) != 1 {
		t.Fatalf("bad key was saved: %+v", p.KeyList())
	}
	if err := provider.AddKey("relay", "Claude", "sk-ant-2222", provider.Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "Any", "sk-any-4444", ""); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	ks := p.KeyList()
	if len(ks) != 3 || ks[0].Protocol != "" || ks[1].Protocol != provider.Anthropic || ks[2].Protocol != "" {
		t.Fatalf("keys %+v", ks)
	}
	raw, _ := os.ReadFile(provider.Path())
	if !strings.Contains(string(raw), `"protocol": "anthropic"`) && !strings.Contains(string(raw), `"protocol":"anthropic"`) {
		t.Fatalf("config lacks protocol: %s", raw)
	}
}

func TestAcceptSetKeyProtocolFollowsKey(t *testing.T) {
	base(t)
	if err := provider.AddKey("relay", "Claude", "sk-ant-2222", provider.Anthropic); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("relay")
	oai, ant := p.KeyList()[0].ID, p.KeyList()[1].ID
	if err := provider.SetKeyProtocol("relay", oai, provider.Chat); err != nil {
		t.Fatal(err)
	}
	if provider.SetKeyProtocol("relay", oai, "carrier-pigeon") == nil {
		t.Fatal("unknown protocol accepted")
	}
	if provider.SetKeyProtocol("relay", "nonexistent", provider.Chat) == nil {
		t.Fatal("unknown key accepted")
	}
	p, _ = provider.Find("relay")
	ks := p.KeyList()
	if ks[0].Protocol != provider.Chat || ks[1].Protocol != provider.Anthropic {
		t.Fatalf("keys %+v", ks)
	}
	if err := provider.UseKey("relay", ant); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	ks = p.KeyList()
	if ks[0].ID != ant || ks[0].Protocol != provider.Anthropic || ks[1].ID != oai || ks[1].Protocol != provider.Chat {
		t.Fatalf("after use: %+v", ks)
	}
	// clear it again
	if err := provider.SetKeyProtocol("relay", oai, ""); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	if ks = p.KeyList(); ks[1].Protocol != "" || ks[0].Protocol != provider.Anthropic {
		t.Fatalf("after clear: %+v", ks)
	}
}

func TestAcceptKeyProtocolSurvivesDisableAndRemove(t *testing.T) {
	base(t)
	provider.AddKey("relay", "Claude", "sk-ant-2222", provider.Anthropic)
	provider.AddKey("relay", "Resp", "sk-resp-3333", provider.Responses)
	p, _ := provider.Find("relay")
	oai, ant, resp := p.KeyList()[0].ID, p.KeyList()[1].ID, p.KeyList()[2].ID
	provider.SetKeyProtocol("relay", oai, provider.Chat)
	// turn the current key off: next in line takes over; protocols stay with their keys
	if err := provider.SetKeyOn("relay", oai, false); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	want := map[string]provider.Protocol{oai: provider.Chat, ant: provider.Anthropic, resp: provider.Responses}
	for _, k := range p.KeyList() {
		if k.Protocol != want[k.ID] {
			t.Fatalf("misplaced protocol: %+v", p.KeyList())
		}
	}
	if p.KeyList()[0].ID != ant {
		t.Fatalf("current key %+v", p.KeyList())
	}
	// remove the current key: the next takes its place with its own protocol
	if err := provider.RemoveKey("relay", ant); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	for _, k := range p.KeyList() {
		if k.Protocol != want[k.ID] {
			t.Fatalf("misplaced after remove: %+v", p.KeyList())
		}
	}
	if p.KeyList()[0].ID != resp {
		t.Fatalf("after remove %+v", p.KeyList())
	}
}

func TestAcceptWithKey(t *testing.T) {
	base(t)
	provider.AddKey("relay", "Claude", "sk-ant-2222", provider.Anthropic)
	p, _ := provider.Find("relay")
	q := p.WithKey(provider.KeyAccount{Key: "k1", Protocol: provider.Chat})
	if q.Key != "k1" || q.Chat == "" || q.Anthropic != "" || q.Responses != "" {
		t.Fatalf("chat key: %+v", q)
	}
	q = p.WithKey(provider.KeyAccount{Key: "k2", Protocol: provider.Anthropic})
	if q.Chat != "" || q.Anthropic == "" || q.Responses != "" {
		t.Fatalf("anthropic key: %+v", q)
	}
	q = p.WithKey(provider.KeyAccount{Key: "k3"})
	if q.Key != "k3" || q.Chat == "" || q.Anthropic == "" || q.Responses == "" {
		t.Fatalf("any key: %+v", q)
	}
	if p.Chat == "" || p.Anthropic == "" {
		t.Fatal("original provider modified")
	}
}

func TestAcceptKeyProtocolFromConfigFile(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-a-1"}); err != nil {
		t.Fatal(err)
	}
	cfg := `{"providers":[{"id":"relay","name":"Relay","chat":"https://relay.test/v1","anthropic":"https://relay.test","key":"sk-a-1","keyProtocol":"chat","keys":[{"key":"sk-b-2","protocol":"anthropic"},{"key":"sk-c-3"}]}]}`
	if err := os.WriteFile(provider.Path(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	ks := p.KeyList()
	if len(ks) != 3 || ks[0].Protocol != provider.Chat || ks[1].Protocol != provider.Anthropic || ks[2].Protocol != "" {
		t.Fatalf("keys %+v", ks)
	}
}
