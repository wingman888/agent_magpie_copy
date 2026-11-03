package provider

// Tests for "an API key may be limited to one protocol". This file is
// self-contained: every helper it uses is defined here, and every name
// carries an f10 prefix so it cannot collide with other test files.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// f10UseTempConfig points magpie's config and cache at fresh temp dirs.
func f10UseTempConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestF10AddKeyProtocol(t *testing.T) {
	f10UseTempConfig(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-chat-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Bad", "sk-bad-3333", "carrier-pigeon"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	if p, _ := Find("relay"); len(p.KeyList()) != 1 || len(p.Keys) != 0 {
		t.Fatalf("a rejected key was saved: %+v", p.Keys)
	}
	if err := AddKey("relay", "Claude", "sk-ant-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	ks := p.KeyList()
	if len(ks) != 2 || ks[1].Protocol != Anthropic || ks[0].Protocol != "" {
		t.Fatalf("protocols after add: %+v", ks)
	}
}

func TestF10SetKeyProtocolFirstKey(t *testing.T) {
	f10UseTempConfig(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-chat-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Claude", "sk-ant-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), "CHAT"); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if ks := p.KeyList(); ks[0].Protocol != Chat {
		t.Fatalf("first key protocol %q, want chat", ks[0].Protocol)
	}
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), "gemini"); err == nil {
		t.Fatal("gemini accepted as a key protocol")
	}
	if err := SetKeyProtocol("relay", keyID("sk-missing"), Chat); err == nil {
		t.Fatal("a missing key was found")
	}
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), ""); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("relay"); p.KeyProtocol != "" {
		t.Fatalf("protocol not cleared: %q", p.KeyProtocol)
	}
}

func TestF10ProtocolFollowsKeyOnSwitch(t *testing.T) {
	f10UseTempConfig(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-chat-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), Chat); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Claude", "sk-ant-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", keyID("sk-ant-2222")); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if p.Key != "sk-ant-2222" || p.KeyProtocol != Anthropic {
		t.Fatalf("new first key lost its limit: %q %q", p.Key, p.KeyProtocol)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-chat-1111" || p.Keys[0].Protocol != Chat {
		t.Fatalf("old first key lost its limit: %+v", p.Keys)
	}
	// turning the first key off: the next one on takes the lead with its own limit
	if err := SetKeyOn("relay", keyID("sk-ant-2222"), false); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-chat-1111" || p.KeyProtocol != Chat {
		t.Fatalf("after off, lead %q limited to %q", p.Key, p.KeyProtocol)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-ant-2222" || p.Keys[0].Protocol != Anthropic || !p.Keys[0].Off {
		t.Fatalf("after off, saved %+v", p.Keys)
	}
	// removing the saved key takes its limit with it
	if err := RemoveKey("relay", keyID("sk-ant-2222")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if len(p.Keys) != 0 || p.KeyProtocol != Chat {
		t.Fatalf("after remove: %+v limit %q", p.Keys, p.KeyProtocol)
	}
}

func TestF10WithKeyEndpoints(t *testing.T) {
	p := Provider{ID: "relay", Chat: "https://relay.test/v1", Responses: "https://relay.test/r/v1", Anthropic: "https://relay.test"}

	chat := p.WithKey(KeyAccount{Key: "sk-chat", Protocol: Chat})
	if chat.Chat != p.Chat || chat.Responses != "" || chat.Anthropic != "" || chat.Key != "sk-chat" {
		t.Fatalf("chat-limited copy: %+v", chat)
	}
	if p.Responses == "" || p.Anthropic == "" || p.Key != "" {
		t.Fatalf("WithKey changed the receiver: %+v", p)
	}

	all := p.WithKey(KeyAccount{Key: "sk-any"})
	if all.Chat != p.Chat || all.Responses != p.Responses || all.Anthropic != p.Anthropic || all.Key != "sk-any" {
		t.Fatalf("unlimited copy lost endpoints: %+v", all)
	}
}

func TestF10ProtocolJSON(t *testing.T) {
	limited := Provider{ID: "relay", KeyProtocol: Chat, Keys: []KeyAccount{{Key: "sk-a", Protocol: Anthropic}}}
	b, err := json.Marshal(limited)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !strings.Contains(s, `"keyProtocol":"chat"`) || !strings.Contains(s, `"protocol":"anthropic"`) {
		t.Fatalf("limits not serialized: %s", s)
	}
	plain := Provider{ID: "relay", Key: "sk-a", Keys: []KeyAccount{{Key: "sk-b"}}}
	b, _ = json.Marshal(plain)
	if s := string(b); strings.Contains(s, "keyProtocol") || strings.Contains(s, `"protocol"`) {
		t.Fatalf("empty limits not omitted: %s", s)
	}
	b, _ = json.Marshal(KeyInfo{ID: "x"})
	if strings.Contains(string(b), "protocol") {
		t.Fatalf("empty KeyInfo protocol not omitted: %s", b)
	}
	b, _ = json.Marshal(KeyInfo{ID: "x", Protocol: Responses})
	if !strings.Contains(string(b), `"protocol":"responses"`) {
		t.Fatalf("KeyInfo protocol not serialized: %s", b)
	}
}

func TestF10KeyProtocolNormalizedOnSave(t *testing.T) {
	f10UseTempConfig(t)
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test",
		Key: "sk-chat-1111", KeyProtocol: " Chat ",
		Keys: []KeyAccount{{Name: "Claude", Key: "sk-ant-2222", Protocol: "Anthropic"}}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyProtocol != Chat || got.Keys[0].Protocol != Anthropic {
		t.Fatalf("protocols after save: %q, %q", got.KeyProtocol, got.Keys[0].Protocol)
	}
}

func TestF10AuthHeadersNeverSendsKeyToOtherProtocol(t *testing.T) {
	p := Provider{ID: "relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test",
		Key: "sk-chat-1111", KeyProtocol: Chat}
	if h := AuthHeaders(p, Anthropic); len(h) != 0 {
		t.Fatalf("chat-limited key sent to anthropic: %v", h)
	}
	if h := AuthHeaders(p, Chat); h["Authorization"] != "Bearer sk-chat-1111" {
		t.Fatalf("chat-limited key not sent to chat: %v", h)
	}
}

func TestF10TestUsesLimitedKeyOnlyOnItsEndpoint(t *testing.T) {
	f10UseTempConfig(t)
	var mu sync.Mutex
	var antHits []string
	ant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		antHits = append(antHits, r.URL.Path)
		mu.Unlock()
		http.NotFound(w, r)
	}))
	defer ant.Close()
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"c1","object":"chat.completion","choices":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer chat.Close()

	p := Provider{ID: "relay", Name: "Relay", Chat: chat.URL + "/v1", Anthropic: ant.URL,
		Models: []string{"gpt-5.5"}, Key: "sk-chat-1111", KeyProtocol: Chat}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	saved, _ := Find("relay")
	results := saved.Test(context.Background())
	for _, r := range results {
		if r.Protocol != Chat {
			t.Fatalf("probed %s, but the key is limited to chat: %+v", r.Protocol, results)
		}
	}
	if len(results) != 1 {
		t.Fatalf("results %+v, want one chat probe", results)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(antHits) != 0 {
		t.Fatalf("anthropic endpoint got requests %v; the chat-limited key must not go there", antHits)
	}
}

// Acceptance 6: an unknown protocol on AddKey fails and saves nothing; a valid
// protocol is saved and visible in the key list.
func TestF10Acceptance6AddKeyWithProtocol(t *testing.T) {
	f10UseTempConfig(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-chat-1111"}); err != nil {
		t.Fatal(err)
	}

	if err := AddKey("relay", "Pigeon", "sk-pigeon-3333", "carrier-pigeon"); err == nil {
		t.Fatal("AddKey accepted the unknown protocol carrier-pigeon")
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Keys) != 0 || len(p.KeyList()) != 1 {
		t.Fatalf("a key with an unknown protocol was saved: %+v", p.KeyList())
	}
	for _, k := range p.KeyList() {
		if k.ID == KeyID("sk-pigeon-3333") {
			t.Fatal("the rejected key shows up in the key list")
		}
	}

	if err := AddKey("relay", "Claude", "sk-ant-2222", Anthropic); err != nil {
		t.Fatalf("AddKey with a valid protocol: %v", err)
	}
	p, err = Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	ks := p.KeyList()
	if len(ks) != 2 {
		t.Fatalf("key list %+v, want 2 keys", ks)
	}
	if ks[1].ID != KeyID("sk-ant-2222") || ks[1].Protocol != Anthropic || ks[1].Name != "Claude" {
		t.Fatalf("added key in list: %+v", ks[1])
	}
	if ks[0].Protocol != "" {
		t.Fatalf("first key picked up a protocol it was never given: %q", ks[0].Protocol)
	}

	// the list output carries protocol for limited keys and omits it otherwise
	b, err := json.Marshal(ks)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"protocol":"anthropic"`) {
		t.Fatalf("key list JSON has no protocol for the limited key: %s", s)
	}
	if strings.Count(s, `"protocol"`) != 1 {
		t.Fatalf("key list JSON protocol count wrong (empty should be omitted): %s", s)
	}
}

// Acceptance 7: SetKeyProtocol applies to the first key; after switching the
// current key, each limit stays with its own key.
func TestF10Acceptance7KeyProtocolFollowsKeyOnSwitch(t *testing.T) {
	f10UseTempConfig(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-chat-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Claude", "sk-ant-2222", Anthropic); err != nil {
		t.Fatal(err)
	}

	if err := SetKeyProtocol("relay", KeyID("sk-chat-1111"), Chat); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	ks := p.KeyList()
	if ks[0].ID != KeyID("sk-chat-1111") || ks[0].Protocol != Chat || !ks[0].Active {
		t.Fatalf("first key after SetKeyProtocol: %+v", ks[0])
	}

	// make the Anthropic key current: the chat-limited key goes behind it, still chat
	if err := UseKey("relay", KeyID("sk-ant-2222")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	ks = p.KeyList()
	if len(ks) != 2 {
		t.Fatalf("key list %+v", ks)
	}
	if ks[0].ID != KeyID("sk-ant-2222") || ks[0].Protocol != Anthropic || !ks[0].Active {
		t.Fatalf("new current key lost its limit: %+v", ks[0])
	}
	if ks[1].ID != KeyID("sk-chat-1111") || ks[1].Protocol != Chat {
		t.Fatalf("old current key's limit did not follow it: %+v", ks[1])
	}
	if p.Key != "sk-ant-2222" || p.KeyProtocol != Anthropic {
		t.Fatalf("stored first key %q limited to %q", p.Key, p.KeyProtocol)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-chat-1111" || p.Keys[0].Protocol != Chat {
		t.Fatalf("stored saved key %+v", p.Keys)
	}

	// changing the limit of the key now behind it does not touch the current one
	if err := SetKeyProtocol("relay", KeyID("sk-chat-1111"), Responses); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.KeyProtocol != Anthropic || p.Keys[0].Protocol != Responses {
		t.Fatalf("limits after changing the saved key: first %q, saved %q", p.KeyProtocol, p.Keys[0].Protocol)
	}
}

// Acceptance 8: WithKey keeps only the endpoint of the key's limit; an
// unlimited key keeps every endpoint.
func TestF10Acceptance8WithKeyKeepsOnlyTheLimitedEndpoint(t *testing.T) {
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Responses: "https://relay.test/r/v1", Anthropic: "https://relay.test"}

	chatOnly := p.WithKey(KeyAccount{Name: "GPT", Key: "sk-chat", Protocol: Chat})
	if chatOnly.Chat != p.Chat || chatOnly.Responses != "" || chatOnly.Anthropic != "" {
		t.Fatalf("chat-limited key kept endpoints chat=%q responses=%q anthropic=%q", chatOnly.Chat, chatOnly.Responses, chatOnly.Anthropic)
	}
	if chatOnly.Key != "sk-chat" || chatOnly.KeyName != "GPT" {
		t.Fatalf("chat-limited copy uses key %q name %q", chatOnly.Key, chatOnly.KeyName)
	}

	respOnly := p.WithKey(KeyAccount{Key: "sk-resp", Protocol: Responses})
	if respOnly.Chat != "" || respOnly.Responses != p.Responses || respOnly.Anthropic != "" {
		t.Fatalf("responses-limited key kept endpoints %+v", respOnly)
	}

	antOnly := p.WithKey(KeyAccount{Key: "sk-ant", Protocol: Anthropic})
	if antOnly.Chat != "" || antOnly.Responses != "" || antOnly.Anthropic != p.Anthropic {
		t.Fatalf("anthropic-limited key kept endpoints %+v", antOnly)
	}

	anyKey := p.WithKey(KeyAccount{Key: "sk-any"})
	if anyKey.Chat != p.Chat || anyKey.Responses != p.Responses || anyKey.Anthropic != p.Anthropic || anyKey.Key != "sk-any" {
		t.Fatalf("unlimited key lost endpoints: %+v", anyKey)
	}

	// the receiver is unchanged by any of the above
	if p.Chat == "" || p.Responses == "" || p.Anthropic == "" || p.Key != "" {
		t.Fatalf("WithKey changed its receiver: %+v", p)
	}
}
