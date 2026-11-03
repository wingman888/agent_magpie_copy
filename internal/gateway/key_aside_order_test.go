package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// weighedIDs names the plan's candidates in order, as the trace has them:
// the rest id, and whether each is aside.
func weighedIDs(pl planned) string {
	s := ""
	for _, w := range pl.order {
		s += w.ID
		if w.Aside {
			s += "(aside)"
		}
		s += " "
	}
	return s
}

// Every main pool, the primary's and its fallback's, is tried before any key
// aside from a pool: the fallback's main key comes ahead of the primary's
// aside Chat key.
func TestAsideAfterFallbackMainPools(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetRestsForTiers()
	fb := provider.Provider{ID: "aside-fb", Name: "Fallback", Chat: "http://upstream.invalid/v1",
		Key: "f1", Models: []string{"claude-opus-4-8"}}
	if err := provider.Save(fb); err != nil {
		t.Fatal(err)
	}
	p := provider.Provider{ID: "aside-fa", Name: "Primary", Chat: "http://upstream.invalid/v1",
		Routing: provider.Ordered, Fallback: []string{"aside-fb/claude-opus-4-8"},
		Key: "k1", Keys: []provider.KeyAccount{{Key: "k3", Protocol: provider.Chat}}}
	s := New()
	cs, pl := s.plan(p, "claude-opus-4-8", provider.Chat)
	want := []string{p.ID + "#" + provider.KeyID("k1"), fb.ID, p.ID + "#" + provider.KeyID("k3")}
	if len(cs) != len(want) || len(pl.order) != len(want) {
		t.Fatalf("got %d candidates, want %d: %s", len(cs), len(want), weighedIDs(pl))
	}
	for i, c := range cs {
		if c.rest != want[i] {
			t.Fatalf("candidate %d = %s, want %s: %s", i, c.rest, want[i], weighedIDs(pl))
		}
	}
	if !pl.order[2].Aside || pl.order[0].Aside || pl.order[1].Aside {
		t.Fatalf("aside marks: %s", weighedIDs(pl))
	}
}

// Turn is for a main pool with more than one key to turn between: a main pool
// of one key with an aside key behind it has no turn.
func TestAsideDoesNotCountTowardTurn(t *testing.T) {
	resetRestsForTiers()
	s := New()
	one := provider.Provider{ID: "aside-turn-one", Chat: "http://upstream.invalid/v1", Routing: provider.Rotate,
		Key: "k1", Keys: []provider.KeyAccount{{Key: "k3", Protocol: provider.Chat}}}
	_, pl := s.plan(one, "claude-opus-4-8", provider.Chat)
	if len(pl.order) != 2 || pl.order[0].Turn || pl.order[0].Aside || !pl.order[1].Aside {
		t.Fatalf("one main key: %s", weighedIDs(pl))
	}
	two := provider.Provider{ID: "aside-turn-two", Chat: "http://upstream.invalid/v1", Routing: provider.Rotate,
		Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3", Protocol: provider.Chat}}}
	_, pl = s.plan(two, "claude-opus-4-8", provider.Chat)
	if len(pl.order) != 3 || !pl.order[0].Turn || pl.order[0].Aside || pl.order[2].Turn || !pl.order[2].Aside {
		t.Fatalf("two main keys: %s", weighedIDs(pl))
	}
}

// The main pool's key answers 400, which no other provider may do better
// with, so it is not retried anywhere; the Chat key aside from the pool still
// gets its try, and answers.
func TestMainPool400ThenAsideAnswers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetRestsForTiers()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		seen = append(seen, key)
		w.Header().Set("Content-Type", "application/json")
		if key != "k3" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"bad request"}}`)
			return
		}
		io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from-aside"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()
	p := provider.Provider{ID: "aside-400", Name: "Tiers", Chat: srv.URL + "/v1", Models: []string{"claude-opus-4-8"},
		Key: "k1", Keys: []provider.KeyAccount{{Key: "k3", Protocol: provider.Chat}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"aside-400/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from-aside") || strings.Join(seen, " ") != "k1 k3" {
		t.Fatalf("%d %s, upstream saw %v", code, body, seen)
	}
	// a 400 rests nothing: the main pool's key is not set aside for it
	if _, ok := restOf(p.ID + "#" + provider.KeyID("k1")); ok {
		t.Fatal("the main pool's key rests after a 400")
	}
}
