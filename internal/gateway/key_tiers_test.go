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

// keysOf names a candidate list by the keys it tries, with asides marked.
func keysOf(cs []candidate) string {
	var parts []string
	for _, c := range cs {
		s := c.p.Key
		if c.aside {
			s += "*"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

// A key made for the request's protocol is the main pool; keys that name no
// protocol come next, then keys made for another, all marked aside.
func TestKeyTiersMainPoolThenAside(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := provider.Provider{ID: "tiers-main", Name: "Tiers", Chat: "http://127.0.0.1:1/v1", Anthropic: "http://127.0.0.1:1", Key: "k1",
		Keys: []provider.KeyAccount{
			{Key: "k2", Protocol: provider.Chat},
			{Key: "k3", Protocol: provider.Chat},
			{Key: "k4"},
			{Key: "k5", Protocol: provider.Anthropic},
		}}
	cs, _ := (&Server{}).plan(p, "some-model", provider.Chat)
	if got, want := keysOf(cs), "k2 k3 k1* k4* k5*"; got != want {
		t.Fatalf("order %q, want %q", got, want)
	}
}

// Only the main pool is rotated; the keys aside stay behind it, and the
// trace marks them aside, the main pool not.
func TestKeyTiersRotateTheMainPool(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := provider.Provider{ID: "tiers-rotate", Name: "Tiers", Chat: "http://127.0.0.1:1/v1", Key: "k1",
		Routing: provider.Rotate,
		Keys: []provider.KeyAccount{
			{Key: "k2", Protocol: provider.Chat},
			{Key: "k3", Protocol: provider.Chat},
			{Key: "k4"},
		}}
	var firsts []string
	for i := 0; i < 3; i++ {
		cs, pl := (&Server{}).plan(p, "some-model", provider.Chat)
		if got := keysOf(cs[:2]); strings.Contains(got, "*") {
			t.Fatalf("main pool %q is marked aside", got)
		}
		if cs[2].p.Key != "k1" || !cs[2].aside {
			t.Fatalf("aside order: %q", keysOf(cs))
		}
		if len(pl.order) != 4 || pl.order[2].Aside != true || pl.order[0].Aside {
			t.Fatalf("trace aside marks: %+v", pl.order)
		}
		firsts = append(firsts, cs[0].p.Key)
	}
	if got := strings.Join(firsts, " "); got != "k2 k3 k2" {
		t.Fatalf("rotation of the main pool: %q", got)
	}
}

// When the main pool's key fails, the key made for no protocol in particular
// answers, after it, as an aside.
func TestKeyAsideAnswersWhenMainFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		seen = append(seen, key)
		if key == "k-chat" {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()
	p := provider.Provider{ID: "relay-aside", Name: "Relay", Chat: srv.URL + "/v1", Key: "k-chat",
		KeyProtocol: provider.Chat, Models: []string{"m"},
		Keys: []provider.KeyAccount{{Key: "k-any"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"relay-aside/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if got := strings.Join(seen, " "); got != "k-chat k-any" {
		t.Fatalf("keys tried: %q", got)
	}
}
