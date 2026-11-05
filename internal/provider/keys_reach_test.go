package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestKeyReachesOnlyItsOwnProtocol(t *testing.T) {
	unlimited := Provider{}
	for _, proto := range Protocols {
		if !unlimited.keyReaches(proto) {
			t.Errorf("unlimited key should reach %s", proto)
		}
	}
	limited := Provider{KeyProtocol: Anthropic}
	for _, proto := range Protocols {
		if got, want := limited.keyReaches(proto), proto == Anthropic; got != want {
			t.Errorf("anthropic-limited key reaches %s = %v, want %v", proto, got, want)
		}
	}
}

// A key limited to Anthropic is never probed against the Chat endpoint.
func TestTestSkipsEndpointsTheKeyCannotReach(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/chat", Anthropic: srv.URL + "/anth",
		Key: "k-anth", KeyProtocol: Anthropic, Models: []string{"claude-opus-4-8"}}
	res := p.Test(context.Background())
	if len(res) != 1 || res[0].Protocol != Anthropic {
		t.Fatalf("results %+v, want only the Anthropic endpoint", res)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, h := range hits {
		if h == "/chat/chat/completions" || h == "/chat/models" {
			t.Fatalf("Anthropic-limited key was sent to the Chat endpoint: %v", hits)
		}
	}
}
