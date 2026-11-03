package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func resetRestsForTiers() {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.note = map[string]Rest{}
}

// keysIn names candidates, in order, by the key each one sends.
func keysIn(cs []candidate, names map[string]string) string {
	s := ""
	for _, c := range cs {
		s += names[c.rest] + " "
	}
	return s
}

func TestKeyFit(t *testing.T) {
	chat := provider.Provider{KeyProtocol: provider.Chat}
	ant := provider.Provider{KeyProtocol: provider.Anthropic}
	for _, c := range []struct {
		q     provider.Provider
		model string
		from  provider.Protocol
		want  int
	}{
		// unspecified
		{provider.Provider{}, "m", provider.Chat, 1},
		// made for the request's protocol
		{chat, "m", provider.Chat, 0},
		// made for another
		{ant, "m", provider.Chat, 2},
		{chat, "m", provider.Anthropic, 2},
		// a Claude model is at home in Anthropic, so a Chat key is aside for it
		{ant, "claude-opus-4-8", provider.Chat, 0},
		{chat, "claude-opus-4-8", provider.Chat, 2},
		{ant, "anthropic/claude-opus-4-8", provider.Chat, 0},
	} {
		if got := keyFit(c.q, c.model, c.from); got != c.want {
			t.Errorf("keyFit(%q, %s, %s) = %d, want %d", c.q.KeyProtocol, c.model, c.from, got, c.want)
		}
	}
}

func TestKeyTiers(t *testing.T) {
	resetRestsForTiers()
	// two keys made for no protocol in particular, and a Chat-only key, rotating
	p := provider.Provider{ID: "tiers-test", Name: "Tiers", Chat: "http://upstream.invalid/v1", Routing: provider.Rotate,
		Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3", Protocol: provider.Chat}}}
	names := map[string]string{}
	id := func(k string) string { return p.ID + "#" + provider.KeyID(k) }
	for _, k := range []string{"k1", "k2", "k3"} {
		names[id(k)] = k
	}
	const model = "claude-opus-4-8"
	s := New()

	// the main pool rotates between k1 and k2; k3 is aside, always last
	for i, want := range []string{"k1 k2 k3 ", "k2 k1 k3 ", "k1 k2 k3 "} {
		cs, pl := s.plan(p, model, provider.Chat)
		if got := keysIn(cs, names); got != want {
			t.Fatalf("turn %d: %q, want %q", i, got, want)
		}
		for j, w := range pl.order {
			if w.Aside != (j == 2) {
				t.Fatalf("turn %d: %s aside %v", i, w.ID, w.Aside)
			}
		}
	}

	// k1 fails and rests: k2 is tried first, k3 still after both
	s.restAfter(candidate{p: p, model: model, rest: id("k1")}, 429, http.Header{}, []byte("slow down"))
	cs, _ := s.plan(p, model, provider.Chat)
	if got := keysIn(cs, names); !strings.HasPrefix(got, "k2 ") || !strings.HasSuffix(got, " k3 ") || len(cs) != 3 {
		t.Fatalf("with k1 resting: %q", got)
	}
	// and with the main pool all resting, the aside key is still there, last
	s.restAfter(candidate{p: p, model: model, rest: id("k2")}, 429, http.Header{}, []byte("slow down"))
	cs, _ = s.plan(p, model, provider.Chat)
	if got := keysIn(cs, names); len(cs) != 3 || !strings.HasSuffix(got, " k3 ") {
		t.Fatalf("with the main pool resting: %q", got)
	}
}

// Keys all made for one protocol, or none, are one pool: nothing is aside.
func TestKeyTiersOnePool(t *testing.T) {
	resetRestsForTiers()
	s := New()
	for _, p := range []provider.Provider{
		{ID: "tiers-plain", Chat: "http://upstream.invalid/v1", Routing: provider.Rotate,
			Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}}},
		{ID: "tiers-same", Chat: "http://upstream.invalid/v1", Routing: provider.Rotate,
			Key: "k1", KeyProtocol: provider.Chat, Keys: []provider.KeyAccount{{Key: "k2", Protocol: provider.Chat}}},
	} {
		cs, pl := s.plan(p, "claude-opus-4-8", provider.Chat)
		if len(cs) != 2 {
			t.Fatalf("%s: %d candidates", p.ID, len(cs))
		}
		for _, w := range pl.order {
			if w.Aside {
				t.Fatalf("%s: %s is aside", p.ID, w.ID)
			}
		}
	}
}

// The end to end: k1 and k2 are rate limited, so the aside Chat key answers.
func TestKeyTiersFallBackToAside(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetRestsForTiers()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		seen = append(seen, key)
		w.Header().Set("Content-Type", "application/json")
		if key != "k3" {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"slow down"}}`)
			return
		}
		io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()
	p := provider.Provider{ID: "e2e-tiers", Name: "Tiers", Chat: srv.URL + "/v1", Models: []string{"claude-opus-4-8"},
		Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3", Protocol: provider.Chat}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"e2e-tiers/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || strings.Join(seen, " ") != "k1 k2 k3" {
		t.Fatalf("%d %s, upstream saw %v", code, body, seen)
	}
}
