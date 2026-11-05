package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func resetGroupTest(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.note = map[string]Rest{}
	restingUntil.Unlock()
}

// A group with no member ready answers 404: no model is ready for it.
func TestGroupWithNoModelReady(t *testing.T) {
	resetGroupTest(t)
	if err := provider.SaveGroup(provider.Group{ID: "dead", Members: []string{"nope/m"}}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New().Handler())
	defer ts.Close()
	res, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"group/dead","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 404 || !strings.Contains(string(body), "没有模型就绪") {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
}

// A group in order tries its first member, and a 429 from it hands the
// request to the next member; the trace names the group and its order.
func TestGroupInOrderFallsOverOnRateLimit(t *testing.T) {
	resetGroupTest(t)
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"slow down"}}`)
	}))
	defer busy.Close()
	var hits int
	spare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer spare.Close()
	for _, p := range []provider.Provider{
		{ID: "b", Name: "B", Key: "kb", Models: []string{"vendor/m"}, Chat: busy.URL + "/v1"},
		{ID: "a", Name: "A", Key: "ka", Models: []string{"only-a"}, Chat: spare.URL + "/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"b/vendor/m", "a/only-a"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	res, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "ok") || hits != 1 {
		t.Fatalf("%d %s, spare hit %d times", res.StatusCode, body, hits)
	}
	var found *Route
	for _, rt := range s.Trace(context.Background(), 0, 0).Routes {
		if rt.Group != nil && rt.Group.ID == "mine" {
			r := rt
			found = &r
		}
	}
	if found == nil {
		t.Fatal("no trace for group/mine")
	}
	if found.Group.Routing != "order" || strings.Join(found.Group.Members, " ") != "b/vendor/m a/only-a" {
		t.Fatalf("group trace: %+v", found.Group)
	}
	if len(found.Order) != 2 || found.Order[0].Provider != "b" || found.Order[1].Provider != "a" {
		t.Fatalf("order: %+v", found.Order)
	}
}

// Rotation moves on once per turn: a turn's tool-result requests stay on
// the key the turn started on, and another model's requests don't count.
func TestRotateMovesOnPerTurn(t *testing.T) {
	cs := []candidate{{rest: "rot#k1"}, {rest: "rot#k2"}, {rest: "rot#k3"}}
	p := provider.Provider{ID: "rot", Routing: provider.Rotate}
	var got []string
	for range 4 {
		turn := &turnScope{key: "rot/turn-test"}
		out, _ := weighT(p, cs, "m", provider.Chat, turn)
		got = append(got, out[0].rest)
		// another model's request in between moves nothing
		other := &turnScope{key: "rot/other-model"}
		weighT(p, cs, "other", provider.Chat, other)
		// the turn's tool results come back and stay on its key
		within := &turnScope{key: "rot/turn-test", within: true, start: turn.picked}
		again, _ := weighT(p, cs, "m", provider.Chat, within)
		if again[0].rest != turn.picked {
			t.Fatalf("within the turn: %s, want %s", again[0].rest, turn.picked)
		}
	}
	if strings.Join(got, " ") != "rot#k1 rot#k2 rot#k3 rot#k1" {
		t.Fatalf("turns: %v", got)
	}
}

// Affinity says why a conversation stays on its last answerer, or why not.
func TestAffinityWhy(t *testing.T) {
	now := time.Now()
	cs := []candidate{{rest: "aff#a"}, {rest: "aff#b"}}
	warm := conversation{last: "aff#a\x00", at: now, cacheRead: 2000}
	cold := conversation{last: "aff#a\x00", at: now.Add(-10 * time.Minute), cacheRead: 2000}
	few := conversation{last: "aff#a\x00", at: now, cacheRead: 100}
	gone := conversation{last: "aff#z\x00", at: now, cacheRead: 2000}
	for _, c := range []struct {
		mode   string
		conv   conversation
		have   bool
		within bool
		want   string
	}{
		{"off", warm, true, true, "off"},
		{"auto", conversation{}, false, true, "first"},
		{"session", warm, true, false, "session"},
		{"turn", warm, true, false, "new-turn"},
		{"turn", warm, true, true, "turn"},
		{"auto", warm, true, true, "turn"},
		{"auto", warm, true, false, "cache"},
		{"auto", few, true, false, "no-cache"},
		{"auto", cold, true, false, "cold"},
		{"session", gone, true, false, "gone"},
	} {
		if got := keepWhy(c.mode, c.conv, c.have, c.within, cs, provider.Chat, now); got != c.want {
			t.Errorf("%s %+v within=%v: %s, want %s", c.mode, c.conv, c.within, got, c.want)
		}
	}
	restingUntil.Lock()
	restingUntil.m["aff#a"] = now.Add(time.Minute)
	restingUntil.Unlock()
	defer func() {
		restingUntil.Lock()
		delete(restingUntil.m, "aff#a")
		restingUntil.Unlock()
	}()
	if got := keepWhy("session", warm, true, true, cs, provider.Chat, now); got != "resting" {
		t.Errorf("resting: %s", got)
	}
}

// Keys made for another protocol are asides: after the main pool, in the
// order of how well they suit the request.
func TestKeysOfAnotherProtocolAreAsides(t *testing.T) {
	p := provider.Provider{ID: "mix", Key: "k-any", Chat: "http://127.0.0.1:1/v1", Anthropic: "http://127.0.0.1:1",
		Keys: []provider.KeyAccount{{Key: "k-chat", Protocol: provider.Chat}, {Key: "k-ant", Protocol: provider.Anthropic}}}
	s := &Server{}
	cs, pl := s.planGroup(provider.Group{ID: "mix"}, []provider.Member{{Provider: p, Model: "gpt-x"}}, provider.Chat, nil)
	want := []string{"mix#" + provider.KeyID("k-chat"), "mix#" + provider.KeyID("k-any"), "mix#" + provider.KeyID("k-ant")}
	if len(cs) != 3 {
		t.Fatalf("candidates: %v", cs)
	}
	for i, c := range cs {
		if c.rest != want[i] {
			t.Errorf("candidate %d: %s, want %s", i, c.rest, want[i])
		}
	}
	if pl.order[0].Aside || !pl.order[1].Aside || !pl.order[2].Aside {
		t.Errorf("aside marks: %v %v %v", pl.order[0].Aside, pl.order[1].Aside, pl.order[2].Aside)
	}
}
