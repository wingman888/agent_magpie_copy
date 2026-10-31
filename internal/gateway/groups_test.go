package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyed is an upstream that answers by the key it was called with: it
// records who was asked, fails the keys it was told to, and reads a number
// of tokens from its cache.
type keyed struct {
	mu     sync.Mutex
	hits   []string
	fail   map[string]int
	cached int
}

func (u *keyed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u.mu.Lock()
	u.hits = append(u.hits, key)
	code, cached := u.fail[key], u.cached
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(code)
		fmt.Fprint(w, `{"error":{"message":"nope"}}`)
		return
	}
	fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":%d}}}`, cached+10, cached)
}

func (u *keyed) last() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.hits) == 0 {
		return ""
	}
	return u.hits[len(u.hits)-1]
}

func (u *keyed) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.hits)
}

func clearRests() {
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
}

// groupSetup points magpie at a temp config.
func groupSetup(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	clearRests()
}

// addKeyed adds a Chat provider served by u with the keys given (the first
// is the main one).
func addKeyed(t *testing.T, u *keyed, id string, models []string, keys ...provider.KeyAccount) {
	t.Helper()
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: id, Name: id, Chat: srv.URL + "/v1", Key: keys[0].Key, KeyProtocol: keys[0].Protocol, Keys: keys[1:], Models: models}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func k(key string) provider.KeyAccount { return provider.KeyAccount{Key: key} }

func ask(t *testing.T, s *Server, model, session string, msgs string) (int, Route) {
	t.Helper()
	body := fmt.Sprintf(`{"model":%q,"messages":%s}`, model, msgs)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	if session != "" {
		req.Header.Set("x-session-id", session)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	st := s.Trace(context.Background(), 0, 0)
	if len(st.Routes) == 0 {
		return rec.Code, Route{}
	}
	return rec.Code, st.Routes[len(st.Routes)-1]
}

const (
	hi     = `[{"role":"user","content":"hi"}]`
	toolRT = `[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"t1","content":"done"}]`
)

// turnN is a conversation on its nth user turn, ending in a new message.
func turnN(n int) string {
	parts := []string{`{"role":"user","content":"first"}`}
	for i := 1; i < n; i++ {
		parts = append(parts, `{"role":"assistant","content":"a"}`, fmt.Sprintf(`{"role":"user","content":"u%d"}`, i))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func TestGroupOrderedFallsToNextMember(t *testing.T) {
	groupSetup(t)
	ub, ua := &keyed{fail: map[string]int{"kb": 429}}, &keyed{}
	addKeyed(t, ua, "a", []string{"only-a"}, k("ka"))
	addKeyed(t, ub, "b", []string{"vendor/m"}, k("kb"))
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"b/vendor/m", "a/only-a", "group/other"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, r := ask(t, s, "group/mine", "", hi)
	if code != 200 || ua.last() != "ka" || ub.count() != 1 {
		t.Fatalf("%d a=%v b=%v", code, ua.hits, ub.hits)
	}
	if r.Group == nil || r.Group.ID != "mine" || r.Group.Routing != "order" || r.Group.Auto || len(r.Group.Members) != 2 {
		t.Fatalf("group trace %+v", r.Group)
	}
	if len(r.Order) != 2 || r.Order[0].Provider != "b" || r.Order[1].Provider != "a" {
		t.Fatalf("order %+v", r.Order)
	}
	// a name with no members ready is a 404 of its own
	provider.SaveGroup(provider.Group{ID: "ghost", Members: []string{"nowhere/x"}})
	if code, _ := ask(t, s, "group/ghost", "", hi); code != 404 {
		t.Fatalf("ghost: %d", code)
	}
	if code, _ := ask(t, s, "group/unknown", "", hi); code != 404 {
		t.Fatalf("unknown: %d", code)
	}
}

func TestAutoGroupServes(t *testing.T) {
	groupSetup(t)
	ua, ub := &keyed{}, &keyed{}
	addKeyed(t, ua, "a", []string{"m"}, k("ka"))
	addKeyed(t, ub, "b", []string{"vendor/m"}, k("kb"))
	s := New()
	code, r := ask(t, s, "group/auto-m", "", hi)
	if code != 200 || r.Group == nil || !r.Group.Auto || r.Group.Members[0] != "a/m" {
		t.Fatalf("%d %+v", code, r.Group)
	}
	provider.DeleteGroup("auto-m")
	if code, _ := ask(t, s, "group/auto-m", "", hi); code != 404 {
		t.Fatalf("hidden: %d", code)
	}
	provider.ShowGroup("auto-m")
	if code, _ := ask(t, s, "group/auto-m", "", hi); code != 200 {
		t.Fatalf("shown: %d", code)
	}
}

func TestSessionAffinity(t *testing.T) {
	for _, tc := range []struct {
		mode, second, why string
		keep              bool
	}{
		{"", toolRT, "turn", true},
		{"session", turnN(2), "session", true},
		{"turn", turnN(2), "new-turn", false},
		{"off", toolRT, "off", false},
	} {
		t.Run("mode "+tc.mode, func(t *testing.T) {
			groupSetup(t)
			u := &keyed{}
			id := "aff" + tc.mode
			addKeyed(t, u, id, []string{"m"}, k("k1"), k("k2"), k("k3"))
			provider.SetRouting(id, provider.LeastUsed)
			if tc.mode != "" {
				provider.SetAffinity(id, tc.mode)
			}
			s := New()
			sess := "sess-" + tc.mode + t.Name()
			if _, r := ask(t, s, id+"/m", sess, hi); u.last() != "k1" || r.Affinity == nil || r.Affinity.Why != map[bool]string{true: "off", false: "first"}[tc.mode == "off"] {
				t.Fatalf("first: %s %+v", u.last(), r.Affinity)
			}
			_, r := ask(t, s, id+"/m", sess, tc.second)
			if r.Affinity == nil || r.Affinity.Why != tc.why || r.Affinity.Kept != tc.keep {
				t.Fatalf("affinity %+v", r.Affinity)
			}
			if (u.last() == "k1") != tc.keep {
				t.Fatalf("answered by %s", u.last())
			}
			// another session starts fresh
			if _, r := ask(t, s, id+"/m", sess+"-other", hi); tc.mode != "off" && r.Affinity.Why != "first" {
				t.Fatalf("other session %+v", r.Affinity)
			}
		})
	}
}

func TestAutoAffinityNeedsTheCache(t *testing.T) {
	groupSetup(t)
	u := &keyed{cached: 2000}
	addKeyed(t, u, "cached", []string{"m"}, k("k1"), k("k2"))
	provider.SetRouting("cached", provider.LeastUsed)
	s := New()
	ask(t, s, "cached/m", "s-cache", hi)
	_, r := ask(t, s, "cached/m", "s-cache", turnN(2))
	if r.Affinity == nil || !r.Affinity.Kept || r.Affinity.Why != "cache" || r.Affinity.CacheRead != 2000 || u.last() != "k1" {
		t.Fatalf("warm cache: %+v %s", r.Affinity, u.last())
	}
	// five minutes on, the vendor's cache is gone
	old := clock
	defer func() { clock = old }()
	clock = func() time.Time { return time.Now().Add(6 * time.Minute) }
	_, r = ask(t, s, "cached/m", "s-cache", turnN(3))
	if r.Affinity == nil || r.Affinity.Kept || r.Affinity.Why != "cold" {
		t.Fatalf("cold: %+v", r.Affinity)
	}
	u.mu.Lock()
	u.cached = 0
	u.mu.Unlock()
	ask(t, s, "cached/m", "s-nocache", hi)
	if _, r = ask(t, s, "cached/m", "s-nocache", turnN(2)); r.Affinity == nil || r.Affinity.Why != "no-cache" {
		t.Fatalf("no cache: %+v", r.Affinity)
	}
}

func TestAffinityGivesWayToRestingAndOtherModels(t *testing.T) {
	groupSetup(t)
	u := &keyed{}
	addKeyed(t, u, "rest", []string{"m", "small"}, k("k1"), k("k2"))
	provider.SetAffinity("rest", "session")
	provider.SetRouting("rest", provider.LeastUsed)
	s := New()
	ask(t, s, "rest/m", "s-rest", hi)
	// a title request to the small model doesn't move the main one's owner
	ask(t, s, "rest/small", "s-rest", hi)
	ask(t, s, "rest/small", "s-rest", hi)
	_, r := ask(t, s, "rest/m", "s-rest", turnN(2))
	if r.Affinity.Why != "session" || u.last() != "k1" {
		t.Fatalf("%+v %s", r.Affinity, u.last())
	}
	restingUntil.Lock()
	restingUntil.m["rest#"+provider.KeyID("k1")] = time.Now().Add(time.Minute)
	restingUntil.Unlock()
	_, r = ask(t, s, "rest/m", "s-rest", turnN(3))
	if r.Affinity.Why != "resting" || r.Affinity.Kept || u.last() != "k2" {
		t.Fatalf("%+v %s", r.Affinity, u.last())
	}
	if _, r = ask(t, s, "rest/m", "s-rest-new", hi); r.Affinity.Why != "first" {
		t.Fatalf("%+v", r.Affinity)
	}
}

func TestRotatePerTurn(t *testing.T) {
	groupSetup(t)
	u := &keyed{}
	addKeyed(t, u, "turns", []string{"m", "small"}, k("k1"), k("k2"), k("k3"))
	provider.SetRouting("turns", provider.Rotate)
	s := New()
	var got []string
	for turn := 1; turn <= 4; turn++ {
		ask(t, s, "turns/m", "s-turns", turnN(turn))
		got = append(got, u.last())
		// the tool results of the turn stay on its key, whatever else is asked
		ask(t, s, "turns/small", "s-turns", hi)
		ask(t, s, "turns/m", "s-turns", turnN(turn)[:len(turnN(turn))-1]+`,{"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"t1","content":"done"}]`)
		got = append(got, u.last())
	}
	if want := "k1 k1 k2 k2 k3 k3 k1 k1"; strings.Join(got, " ") != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

func TestGroupRotatesAcrossMembers(t *testing.T) {
	groupSetup(t)
	ua, ub := &keyed{}, &keyed{}
	addKeyed(t, ua, "ga", []string{"m"}, k("a1"))
	addKeyed(t, ub, "gb", []string{"m"}, k("b1"), k("b2"))
	provider.SaveGroup(provider.Group{ID: "pool", Members: []string{"ga/m", "gb/m"}, Routing: provider.Rotate})
	s := New()
	var got []string
	last := func() string {
		if ua.count() > 0 && ua.last() != "" {
			return ""
		}
		return ""
	}
	_ = last
	for turn := 1; turn <= 3; turn++ {
		na, nb := ua.count(), ub.count()
		ask(t, s, "group/pool", "", turnN(turn))
		if ua.count() > na {
			got = append(got, ua.last())
		} else if ub.count() > nb {
			got = append(got, ub.last())
		}
	}
	if strings.Join(got, " ") != "a1 b1 b2" {
		t.Fatalf("got %v", got)
	}
}

func TestKeysByProtocolPools(t *testing.T) {
	groupSetup(t)
	u := &keyed{fail: map[string]int{}}
	addKeyed(t, u, "pools", []string{"m"}, k("u1"), k("u2"), provider.KeyAccount{Key: "c1", Protocol: provider.Chat})
	provider.SetRouting("pools", provider.Rotate)
	s := New()
	for range 2 {
		if _, r := ask(t, s, "pools/m", "", hi); u.last() != "c1" || len(r.Order) != 3 ||
			r.Order[0].Aside || !r.Order[1].Aside || !r.Order[2].Aside {
			t.Fatalf("answered by %s: %+v", u.last(), r.Order)
		}
	}
	// the primary pool failing, the others are tried after it and answer
	u.mu.Lock()
	u.fail["c1"] = 500
	u.mu.Unlock()
	code, r := ask(t, s, "pools/m", "", hi)
	if code != 200 || u.last() != "u1" || len(r.Tries) != 2 {
		t.Fatalf("%d %s %+v", code, u.last(), r.Tries)
	}
}

// A model named for another family doesn't change the tiers when the
// provider has no key made for that family: the request's own protocol
// (Chat here) decides.
func TestKeyTiersFollowRequestProtocol(t *testing.T) {
	groupSetup(t)
	u := &keyed{fail: map[string]int{}}
	addKeyed(t, u, "tiers", []string{"claude-x"}, k("u1"), k("u2"), provider.KeyAccount{Key: "c1", Protocol: provider.Chat})
	provider.SetRouting("tiers", provider.Rotate)
	s := New()
	for range 3 {
		_, r := ask(t, s, "tiers/claude-x", "", hi)
		if u.last() != "c1" || len(r.Order) != 3 || r.Order[0].Aside || !r.Order[1].Aside || !r.Order[2].Aside || r.Order[0].Fit != 0 || r.Order[1].Fit != 1 {
			t.Fatalf("answered by %s: %+v", u.last(), r.Order)
		}
	}
}
