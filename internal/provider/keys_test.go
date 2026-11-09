package provider

import (
	"strings"
	"testing"
)

// keysTestProvider saves a keyless local provider, so the key tests can add
// keys one at a time.
func keysTestProvider(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "kr", Name: "KR", Chat: "http://localhost:9/v1"}); err != nil {
		t.Fatal(err)
	}
}

func keysTestGet(t *testing.T) Provider {
	t.Helper()
	p, err := Find("kr")
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

func TestKeysAddRules(t *testing.T) {
	keysTestProvider(t)
	if err := AddKey("kr", "first", "sk-a", Anthropic); err != nil {
		t.Fatal(err)
	}
	if p := keysTestGet(t); p.Key != "sk-a" || p.KeyName != "first" || p.KeyProtocol != Anthropic {
		t.Fatalf("first key should be current, got %+v", p)
	}
	if err := AddKey("kr", "dup", "sk-a", ""); err == nil {
		t.Fatal("a repeated key must be refused")
	}
	if err := AddKey("kr", "bad", "sk-b", Gemini); err == nil {
		t.Fatal("an unknown protocol must be refused")
	}
	if err := AddKey("kr", "second", "sk-c", ""); err != nil {
		t.Fatal(err)
	}
	p := keysTestGet(t)
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-c" || p.Keys[0].Off {
		t.Fatalf("a new key should be on and after the current one, got %+v", p.Keys)
	}
}

func TestKeysUseKeepsOthers(t *testing.T) {
	keysTestProvider(t)
	AddKey("kr", "a", "sk-a", Anthropic)
	AddKey("kr", "c", "sk-c", "")
	AddKey("kr", "d", "sk-d", Chat)
	if err := UseKey("kr", KeyRef("sk-d")); err != nil {
		t.Fatal(err)
	}
	p := keysTestGet(t)
	if p.Key != "sk-d" || p.KeyName != "d" || p.KeyProtocol != Chat {
		t.Fatalf("sk-d should be current with its name and protocol, got %+v", p)
	}
	if len(p.Keys) != 2 || p.Keys[0].Key != "sk-a" || p.Keys[0].Name != "a" || p.Keys[0].Protocol != Anthropic || p.Keys[1].Key != "sk-c" {
		t.Fatalf("the others should stay, the old current first, got %+v", p.Keys)
	}
}

func TestKeysOffTakeoverAndLastOn(t *testing.T) {
	keysTestProvider(t)
	AddKey("kr", "a", "sk-a", "")
	AddKey("kr", "b", "sk-b", "")
	if err := SetKeyOn("kr", KeyRef("sk-a"), false); err != nil {
		t.Fatal(err)
	}
	p := keysTestGet(t)
	if p.Key != "sk-b" || p.KeyName != "b" {
		t.Fatalf("the next key on should take over, got %+v", p)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-a" || !p.Keys[0].Off || p.Keys[0].Name != "a" {
		t.Fatalf("the old current key should stay, off, got %+v", p.Keys)
	}
	if err := SetKeyOn("kr", KeyRef("sk-b"), false); err == nil {
		t.Fatal("the last key on must not be turned off")
	}
	if p := keysTestGet(t); p.Key != "sk-b" || len(p.Keys) != 1 {
		t.Fatalf("a refused switch must change nothing, got %+v", p)
	}
	if err := RemoveKey("kr", KeyRef("sk-b")); err == nil {
		t.Fatal("the last key on must not be removed")
	}
	if err := SetKeyOn("kr", KeyRef("sk-a"), true); err != nil {
		t.Fatal(err)
	}
	if p := keysTestGet(t); p.Keys[0].Off {
		t.Fatal("turning a key on should clear its off flag")
	}
}

func TestKeysRemoveRenameProtocol(t *testing.T) {
	keysTestProvider(t)
	AddKey("kr", "a", "sk-a", "")
	AddKey("kr", "b", "sk-b", "")
	if err := RemoveKey("kr", KeyRef("sk-a")); err != nil {
		t.Fatal(err)
	}
	p := keysTestGet(t)
	if p.Key != "sk-b" || p.KeyName != "b" || len(p.Keys) != 0 {
		t.Fatalf("removing the current key should promote the next, got %+v", p)
	}
	if err := RenameKey("kr", KeyRef("sk-b"), "team"); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("kr", KeyRef("sk-b"), Responses); err != nil {
		t.Fatal(err)
	}
	if p := keysTestGet(t); p.KeyName != "team" || p.KeyProtocol != Responses {
		t.Fatalf("rename and protocol should apply to the current key, got %+v", p)
	}
	if err := SetKeyProtocol("kr", KeyRef("sk-b"), ""); err != nil {
		t.Fatal(err)
	}
	if p := keysTestGet(t); p.KeyProtocol != "" {
		t.Fatal("an empty protocol should lift the limit")
	}
	if err := SetKeyProtocol("kr", KeyRef("sk-b"), "gemini"); err == nil {
		t.Fatal("gemini is not a key protocol")
	}
	if err := RenameKey("kr", "0123456789", "x"); err == nil {
		t.Fatal("an unknown key must be an error")
	}
}

func TestKeysChoiceOrder(t *testing.T) {
	p := Provider{ID: "r", Name: "R", Chat: "https://r.example/v1", Anthropic: "https://r.example",
		Key: "k-any", Keys: []KeyEntry{
			{Key: "k-ant", Protocol: Anthropic},
			{Key: "k-chat", Protocol: Chat},
			{Key: "k-off", Protocol: Anthropic, Off: true},
			{Key: "k-resp", Protocol: Responses}, // no Responses endpoint: never used
		}}
	refs := func(cs []KeyChoice) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Key)
		}
		return out
	}
	cases := []struct {
		model string
		from  Protocol
		want  string
	}{
		{"claude-sonnet-5-5", Chat, "k-ant k-any k-chat"},
		{"gpt-5.5", Anthropic, "k-chat k-any k-ant"},
		{"deepseek/o3-mini", Anthropic, "k-chat k-any k-ant"},
		{"deepseek-chat", Chat, "k-any k-chat k-ant"},
	}
	for _, c := range cases {
		got := strings.Join(refs(p.KeyChoices(c.model, c.from)), " ")
		if got != c.want {
			t.Errorf("%s from %s: got %q, want %q", c.model, c.from, got, c.want)
		}
	}
	for _, c := range p.KeyChoices("claude-x", Chat) {
		if c.Key == "k-off" || c.Key == "k-resp" {
			t.Errorf("%s must never be a choice", c.Key)
		}
	}
}

func TestKeysUsingLimitsEndpoints(t *testing.T) {
	p := Provider{ID: "r", Chat: "https://r.example/v1", Anthropic: "https://r.example", Key: "k"}
	pc := p.Using(KeyChoice{Key: "k-ant", Protocol: Anthropic})
	if pc.Chat != "" || pc.Anthropic == "" {
		t.Fatalf("an Anthropic-limited key should keep only the Anthropic endpoint, got %q / %q", pc.Chat, pc.Anthropic)
	}
	if p.Chat == "" {
		t.Fatal("Using must not change the original provider")
	}
}
