package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// vendor is one upstream endpoint that accepts exactly one key and records
// the key every request carried.
type vendor struct {
	srv  *httptest.Server
	mu   sync.Mutex
	keys []string
}

func newVendor(t *testing.T, accept string, failModels bool) *vendor {
	t.Helper()
	v := &vendor{}
	v.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		v.mu.Lock()
		v.keys = append(v.keys, key)
		v.mu.Unlock()
		if key != accept {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"wrong key for this endpoint"}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/models") {
			if failModels {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Write([]byte(`{"data":[{"id":"claude-opus-4-8"}]}`))
			return
		}
		w.Write([]byte(`{"id":"msg_1"}`))
	}))
	t.Cleanup(v.srv.Close)
	return v
}

// seen is every key the endpoint was sent, and how many of them were not its own.
func (v *vendor) seen(own string) (keys []string, foreign int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, k := range v.keys {
		if k != own {
			foreign++
		}
	}
	return append([]string(nil), v.keys...), foreign
}

// relay is a provider with a Chat and an Anthropic endpoint; keys[0] is the
// key in use, the rest are saved behind it.
func relay(chat, ant string, keys ...KeyAccount) Provider {
	p := Provider{ID: "relay", Name: "Relay", Chat: chat + "/v1", Anthropic: ant}
	p.Key, p.KeyProtocol = keys[0].Key, keys[0].Protocol
	p.Keys = keys[1:]
	return p
}

func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

const (
	chatKey = "sk-chat-1111"
	antKey  = "sk-ant-2222"
)

// The Chat endpoint's /models fails, so Fetch has to go on to the Anthropic
// endpoint. Each endpoint must be asked only with its own key, whichever key
// is listed first.
func TestFetchAsksEachEndpointWithItsOwnKey(t *testing.T) {
	for name, keys := range map[string][]KeyAccount{
		"chat key first":      {{Key: chatKey, Protocol: Chat}, {Key: antKey, Protocol: Anthropic}},
		"anthropic key first": {{Key: antKey, Protocol: Anthropic}, {Key: chatKey, Protocol: Chat}},
	} {
		t.Run(name, func(t *testing.T) {
			isolateConfig(t)
			chat := newVendor(t, chatKey, true)
			ant := newVendor(t, antKey, false)
			p := relay(chat.srv.URL, ant.srv.URL, keys...)

			ms, err := p.Fetch(context.Background())
			if err != nil || len(ms) != 1 {
				t.Fatalf("Fetch: %v, %d models", err, len(ms))
			}
			if _, foreign := chat.seen(chatKey); foreign != 0 {
				t.Errorf("chat endpoint was sent %d request(s) with another key", foreign)
			}
			if _, foreign := ant.seen(antKey); foreign != 0 {
				t.Errorf("anthropic endpoint was sent %d request(s) with another key", foreign)
			}
			if keys, _ := ant.seen(antKey); len(keys) != 1 {
				t.Errorf("anthropic endpoint was asked %d times, want 1", len(keys))
			}
		})
	}
}

func TestTestProbesEachEndpointWithItsOwnKey(t *testing.T) {
	isolateConfig(t)
	chat := newVendor(t, chatKey, false)
	ant := newVendor(t, antKey, false)
	// the Anthropic-limited key is first; the probe of each endpoint must still use its own
	p := relay(chat.srv.URL, ant.srv.URL, KeyAccount{Key: antKey, Protocol: Anthropic}, KeyAccount{Key: chatKey, Protocol: Chat})

	res := p.Test(context.Background())
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(res), res)
	}
	for _, r := range res {
		if !r.OK {
			t.Errorf("%s probe failed: %d %s", r.Protocol, r.Status, r.Error)
		}
	}
	if _, foreign := chat.seen(chatKey); foreign != 0 {
		t.Errorf("chat endpoint was sent %d request(s) with another key", foreign)
	}
	if _, foreign := ant.seen(antKey); foreign != 0 {
		t.Errorf("anthropic endpoint was sent %d request(s) with another key", foreign)
	}
}

// A key limited to a protocol the provider has no endpoint for takes no part:
// nothing is sent, and nothing is reported as an error by Test.
func TestEndpointWithoutUsableKeyIsSkipped(t *testing.T) {
	isolateConfig(t)
	ant := newVendor(t, antKey, false)
	p := Provider{ID: "relay", Name: "Relay", Anthropic: ant.srv.URL, Key: chatKey, KeyProtocol: Chat}

	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch succeeded with no usable key")
	}
	if keys, _ := ant.seen(antKey); len(keys) != 0 {
		t.Errorf("anthropic endpoint was sent %v, want nothing", keys)
	}
	if res := p.Test(context.Background()); len(res) != 0 {
		t.Errorf("Test probed with no usable key: %+v", res)
	}
	if keys, _ := ant.seen(antKey); len(keys) != 0 {
		t.Errorf("anthropic endpoint was sent %v, want nothing", keys)
	}
}

func TestKeyForPrefersLimitedKey(t *testing.T) {
	p := Provider{Chat: "https://c/v1", Anthropic: "https://a", Key: "k-any", Keys: []KeyAccount{
		{Key: "k-chat", Protocol: Chat},
		{Key: "k-ant", Protocol: Anthropic},
	}}
	for proto, want := range map[Protocol]string{Chat: "k-chat", Anthropic: "k-ant"} {
		k, ok := p.KeyFor(proto)
		if !ok || k.Key != want {
			t.Errorf("KeyFor(%s) = %+v, %v; want %s", proto, k, ok, want)
		}
	}
	if _, ok := (Provider{Chat: "https://c/v1", Key: "k", KeyProtocol: Anthropic}).KeyFor(Chat); ok {
		t.Error("an Anthropic-limited key was offered for Chat")
	}
	if _, ok := (Provider{Chat: "https://c/v1", Key: "k", KeyProtocol: Chat}).KeyFor(Responses); ok {
		t.Error("KeyFor offered a key for an endpoint the provider lacks")
	}
}

func TestSaveRejectsUnknownKeyProtocol(t *testing.T) {
	isolateConfig(t)
	base := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-1"}

	bad := base
	bad.KeyProtocol = "carrier-pigeon"
	if err := Save(bad); err == nil {
		t.Fatal("Save accepted an unknown first-key protocol")
	}
	bad = base
	bad.Keys = []KeyAccount{{Key: "sk-2", Protocol: "carrier-pigeon"}}
	if err := Save(bad); err == nil {
		t.Fatal("Save accepted an unknown saved-key protocol")
	}
	if _, err := Find("relay"); err == nil {
		t.Fatal("a rejected provider was saved")
	}

	ok := base
	ok.Keys = []KeyAccount{{Key: "sk-2"}}
	if err := Save(ok); err != nil {
		t.Fatalf("empty protocols were rejected: %v", err)
	}
}
