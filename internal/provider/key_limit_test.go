package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestKeyProtocolNormalizedOnSave(t *testing.T) {
	useTempConfig(t)
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

func TestAuthHeadersNeverSendsKeyToOtherProtocol(t *testing.T) {
	p := Provider{ID: "relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test",
		Key: "sk-chat-1111", KeyProtocol: Chat}
	if h := AuthHeaders(p, Anthropic); len(h) != 0 {
		t.Fatalf("chat-limited key sent to anthropic: %v", h)
	}
	if h := AuthHeaders(p, Chat); h["Authorization"] != "Bearer sk-chat-1111" {
		t.Fatalf("chat-limited key not sent to chat: %v", h)
	}
}

func TestTestUsesLimitedKeyOnlyOnItsEndpoint(t *testing.T) {
	useTempConfig(t)
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
