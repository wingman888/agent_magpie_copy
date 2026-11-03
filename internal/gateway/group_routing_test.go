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

// A rotating pool takes its turns per user turn: the requests of one turn,
// tool results sent back within it, stay on the key it started on, the next
// turn takes the next key, and another model's requests in between leave the
// conversation's place alone.
func TestF09RotateTakesTurnsWithinASession(t *testing.T) {
	f09KeyPoolSetup(t)
	p := provider.Provider{ID: "turnrot", Name: "Turn", Routing: provider.Rotate, Chat: "http://relay.test/v1",
		Models: []string{"m", "other"}, Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3"}}}
	rest := func(k string) string { return "turnrot#" + provider.KeyID(k) }
	s := New()
	// first is the key the request's turn starts on
	first := func(model string, turn int, within bool) string {
		cs, _ := s.planIn(p, model, provider.Chat, turnRef{session: "ses-turns", model: model, turn: turn, within: within})
		return cs[0].rest
	}
	for _, c := range []struct {
		turn   int
		within bool
		want   string
	}{
		{1, false, rest("k1")},
		{1, true, rest("k1")}, // tool result
		{1, true, rest("k1")}, // and another
	} {
		if got := first("m", c.turn, c.within); got != c.want {
			t.Fatalf("turn %d within=%v: %s, want %s", c.turn, c.within, got, c.want)
		}
	}
	first("other", 1, false) // a helper model's request, in the middle of the conversation
	for _, c := range []struct {
		turn   int
		within bool
		want   string
	}{
		{2, false, rest("k2")},
		{2, true, rest("k2")},
	} {
		if got := first("m", c.turn, c.within); got != c.want {
			t.Fatalf("turn %d within=%v: %s, want %s", c.turn, c.within, got, c.want)
		}
	}
	first("other", 2, false)
	if got := first("m", 3, false); got != rest("k3") {
		t.Fatalf("turn 3: %s, want k3", got)
	}
	if got := first("m", 3, true); got != rest("k3") {
		t.Fatalf("turn 3 tool result: %s, want k3", got)
	}
	if got := first("m", 4, false); got != rest("k1") {
		t.Fatalf("turn 4: %s, want k1 again", got)
	}
}

// A group that rotates weighs the keys of all its members as one pool, and
// turns through them a turn at a time.
func TestF09GroupRotatesItsMembersKeysAsOnePool(t *testing.T) {
	f09KeyPoolSetup(t)
	a := provider.Provider{ID: "grota", Name: "A", Chat: "http://a.test/v1", Models: []string{"m"}, Key: "ka"}
	b := provider.Provider{ID: "grotb", Name: "B", Chat: "http://b.test/v1", Models: []string{"m"}, Key: "kb"}
	ms := []memberOf{{p: a, model: "m"}, {p: b, model: "m"}}
	g := provider.Group{ID: "rot", Routing: provider.Rotate}
	s := New()
	var got []string
	for turn := 1; turn <= 4; turn++ {
		cs, _ := s.planGroup(g, ms, provider.Chat, turnRef{session: "ses-group", model: "group/rot", turn: turn})
		got = append(got, cs[0].rest)
	}
	if want := "grota grotb grota grotb"; strings.Join(got, " ") != want {
		t.Fatalf("turns took %v, want %s", got, want)
	}
}

// The conversation keeps to the key that last answered it, as its affinity
// says, and the reason is what the trace says.
func TestF09AffinityModesAndWhy(t *testing.T) {
	f09KeyPoolSetup(t)
	p := provider.Provider{ID: "aff", Name: "Aff", Chat: "http://relay.test/v1", Models: []string{"m"},
		Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}}}
	k1, k2 := "aff#"+provider.KeyID("k1"), "aff#"+provider.KeyID("k2")
	s := New()
	cands, _ := s.planIn(p, "m", provider.Chat, turnRef{})
	if len(cands) != 2 || cands[0].rest != k1 || cands[1].rest != k2 {
		t.Fatalf("candidates: %v", restsOf(cands))
	}
	now := time.Now()
	reply := func(session, rest string, cacheRead int, at time.Time) {
		replies.Lock()
		replies.m[replyKey(session, "m")] = lastReply{cand: rest + "\x00m", rest: rest, cacheRead: cacheRead, at: at}
		replies.Unlock()
	}
	for _, c := range []struct {
		name     string
		mode     string
		tn       turnRef
		last     string // what answered last
		cache    int
		age      time.Duration
		resting  bool
		why      string
		kept     bool
		movesTo0 string // who is first once affinity has run
	}{
		{"auto within the turn keeps", "", turnRef{turn: 2, within: true}, k2, 0, 0, false, "turn", true, k2},
		{"auto across turns without a warm cache does not", "", turnRef{turn: 2}, k2, 0, time.Minute, false, "no-cache", false, k1},
		{"auto across turns with the cache warm keeps", "", turnRef{turn: 2}, k2, 2000, time.Minute, false, "cache", true, k2},
		{"auto across turns with the cache cold does not", "", turnRef{turn: 2}, k2, 2000, 10 * time.Minute, false, "cold", false, k1},
		{"session keeps across turns", "session", turnRef{turn: 2}, k2, 0, 0, false, "session", true, k2},
		{"turn does not keep a new turn", "turn", turnRef{turn: 2}, k2, 0, 0, false, "new-turn", false, k1},
		{"turn keeps within the turn", "turn", turnRef{turn: 2, within: true}, k2, 0, 0, false, "turn", true, k2},
		{"off keeps nothing", "off", turnRef{turn: 2, within: true}, k2, 2000, 0, false, "off", false, k1},
		{"a resting answerer is not kept to", "session", turnRef{turn: 2, within: true}, k2, 0, 0, true, "resting", false, k1},
		{"an answerer no longer a candidate is gone", "session", turnRef{turn: 2, within: true}, "aff#zz", 0, 0, false, "gone", false, k1},
	} {
		t.Run(c.name, func(t *testing.T) {
			reply("ses-case", c.last, c.cache, now.Add(-c.age))
			if c.resting {
				restingUntil.Lock()
				restingUntil.m[k2] = time.Now().Add(time.Minute)
				restingUntil.Unlock()
				defer func() {
					restingUntil.Lock()
					restingUntil.m = map[string]time.Time{}
					restingUntil.Unlock()
				}()
			}
			i, to, at := affine(cands, "ses-case", "m", c.mode, c.tn, now)
			if at == nil || at.Why != c.why || at.Kept != c.kept {
				t.Fatalf("trace %+v, want why %s kept %v", at, c.why, c.kept)
			}
			if i < 0 {
				if c.kept {
					t.Fatal("kept, but nothing moved")
				}
				return
			}
			if first := shift(cands, i, to)[0].rest; first != c.movesTo0 {
				t.Fatalf("first is %s, want %s", first, c.movesTo0)
			}
		})
	}

	// the first request of a session, and a session without an id, have
	// nothing to keep to
	if _, _, at := affine(cands, "ses-new", "m", "", turnRef{turn: 1}, now); at == nil || at.Why != "first" {
		t.Fatalf("first request: %+v", at)
	}
	if _, _, at := affine(cands, "", "m", "", turnRef{turn: 1}, now); at != nil {
		t.Fatalf("no session has no affinity trace: %+v", at)
	}
}

// A group named by an ordered route: b answers 429, so a answers, and the
// trace names the group and its candidates in member order.
func TestF09GroupOrderFallsOverToTheNextMember(t *testing.T) {
	f09KeyPoolSetup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		if key == "kb" {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from-`+key+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()
	for _, p := range []provider.Provider{
		{ID: "grpb", Name: "B", Key: "kb", Chat: srv.URL + "/v1", Models: []string{"vendor/m"}},
		{ID: "grpa", Name: "A", Key: "ka", Chat: srv.URL + "/v1", Models: []string{"only-a"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "mine", Routing: provider.Ordered,
		Members: []provider.Member{{Provider: "grpb", Model: "vendor/m"}, {Provider: "grpa", Model: "only-a"}}}); err != nil {
		t.Fatal(err)
	}

	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`))
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-ka") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var rt *Route
	st := s.Trace(context.Background(), 0, 0)
	for i := range st.Routes {
		if st.Routes[i].Group != nil {
			rt = &st.Routes[i]
		}
	}
	if rt == nil {
		t.Fatal("no route names a group")
	}
	if rt.Group.ID != "mine" || rt.Group.Auto || rt.Group.Routing != provider.Ordered {
		t.Fatalf("group: %+v", rt.Group)
	}
	if got := strings.Join(rt.Group.Members, ","); got != "grpb/vendor/m,grpa/only-a" {
		t.Fatalf("members: %s", got)
	}
	if len(rt.Order) != 2 || rt.Order[0].ID != "grpb" || rt.Order[1].ID != "grpa" {
		t.Fatalf("order: %+v", rt.Order)
	}
	if len(rt.Tries) != 2 || rt.Tries[0].ID != "grpb" || rt.Tries[0].Status != 429 || rt.Tries[1].ID != "grpa" || rt.Tries[1].Status != 200 {
		t.Fatalf("tries: %+v", rt.Tries)
	}

	// a group with no member ready is a 404 that says so
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"group/nope","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "has no model ready") {
		t.Fatalf("missing group: %d %s", rec.Code, rec.Body.String())
	}
}
