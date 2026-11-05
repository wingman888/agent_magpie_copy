package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// xrKeyedUp is an upstream that tells apart the keys it is given: it answers
// each, and rate limits the ones marked limited.
type xrKeyedUp struct {
	mu      sync.Mutex
	limited map[string]bool
	seen    []string // the key each request came with, in order
}

func (k *xrKeyedUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	k.mu.Lock()
	k.seen = append(k.seen, key)
	limited := k.limited[key]
	k.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if limited {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"slow down"}}`))
		return
	}
	w.Write([]byte(`{"id":"ok","choices":[]}`))
}

func (k *xrKeyedUp) limit(key string, on bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.limited[key] = on
}

func (k *xrKeyedUp) answers() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.seen...)
}

// xrGrpUpstream saves a provider with the given keys (the first is its own),
// serving model from an upstream of its own.
func xrGrpUpstream(t *testing.T, id, model string, keys ...string) *xrKeyedUp {
	t.Helper()
	k := &xrKeyedUp{limited: map[string]bool{}}
	up := httptest.NewServer(k)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: id, Key: keys[0], Chat: up.URL + "/v1", Models: []string{model}}
	for _, key := range keys[1:] {
		p.Keys = append(p.Keys, provider.KeyAccount{Key: key})
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return k
}

// xrGrpReset gives each test its own config and cache, and no resting keys.
func xrGrpReset(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.note = map[string]Rest{}
	restingUntil.Unlock()
	groupMemory.Lock()
	groupMemory.answers = map[string]groupAnswer{}
	groupMemory.turns = map[string]groupTurn{}
	groupMemory.Unlock()
	t.Cleanup(func() { clock = time.Now })
}

// xrGrpRecover makes a key answer again: un-limited, and no longer resting.
func xrGrpRecover(k *xrKeyedUp, key string) {
	k.limit(key, false)
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
}

// xrGrpRest is the id a key of a provider is routed and traced by.
func xrGrpRest(id, key string) string { return id + "#" + provider.KeyID(key) }

func xrGrpChat(t *testing.T, h http.Handler, session, body string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	if session != "" {
		req.Header.Set("x-session-id", session)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// xrGrpLast is the route the gateway traced last.
func xrGrpLast(t *testing.T, srv *Server) *Route {
	t.Helper()
	srv.trace.mu.Lock()
	defer srv.trace.mu.Unlock()
	if len(srv.trace.routes) == 0 {
		t.Fatal("no route traced")
	}
	return srv.trace.routes[len(srv.trace.routes)-1]
}

const (
	xrGrpFirst = `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`
	xrGrpNext  = `{"model":"group/mine","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"user","content":"again"}]}`
	xrGrpTool  = `{"model":"group/mine","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"out"}]}`
)

// xrGrpSetup is a group "mine" of one provider "kp" with keys k1, k2 — k1
// rate limited — and the given affinity and routing.
func xrGrpSetup(t *testing.T, affinity, routing string) (*xrKeyedUp, http.Handler, *Server) {
	t.Helper()
	xrGrpReset(t)
	up := xrGrpUpstream(t, "kp", "m", "k1", "k2")
	up.limit("k1", true)
	if err := provider.SaveGroup(provider.Group{ID: "mine", Members: []string{"kp/m"}, Routing: routing, Affinity: affinity}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	return up, srv.Handler(), srv
}

// A group's order keeps the members in order and the trace says so; a
// member rate limited hands the request on.
func TestXRGroupOrderTraceFollowsMembers(t *testing.T) {
	xrGrpReset(t)
	b := xrGrpUpstream(t, "b", "m", "kb")
	a := xrGrpUpstream(t, "a", "m", "ka")
	b.limit("kb", true)
	if err := provider.SaveGroup(provider.Group{ID: "mine", Members: []string{"b/m", "a/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	xrGrpChat(t, srv.Handler(), "", xrGrpFirst)
	if got := b.answers(); len(got) != 1 || got[0] != "kb" {
		t.Fatalf("b was not tried first: %v", got)
	}
	if got := a.answers(); len(got) != 1 || got[0] != "ka" {
		t.Fatalf("a did not answer: %v", got)
	}
	r := xrGrpLast(t, srv)
	if r.Group == nil || r.Group.ID != "mine" {
		t.Fatalf("group in trace: %+v", r.Group)
	}
	if len(r.Order) != 2 || r.Order[0].Provider != "b" || r.Order[1].Provider != "a" {
		t.Fatalf("candidates out of member order: %+v", r.Order)
	}
	if r.Affinity != nil {
		t.Fatalf("a single request in no session has no affinity: %+v", r.Affinity)
	}
}

// By default a turn stays on the key that answered it: the tool result
// coming back goes to the same key, though the first key is up again.
func TestXRGroupAffinityAutoKeepsTurn(t *testing.T) {
	up, h, srv := xrGrpSetup(t, "", "")
	xrGrpChat(t, h, "S", xrGrpFirst) // k1 is limited: k2 answers
	xrGrpRecover(up, "k1")
	xrGrpChat(t, h, "S", xrGrpTool)
	if got := up.answers(); len(got) != 3 || got[1] != "k2" || got[2] != "k2" {
		t.Fatalf("keys used: %v", got)
	}
	r := xrGrpLast(t, srv)
	a := r.Affinity
	if a == nil || a.Why != "turn" || !a.Kept || !a.Within || a.Turn != 1 || a.Last != xrGrpRest("kp", "k2") {
		t.Fatalf("affinity: %+v", a)
	}
}

// session keeps the whole conversation on its key, a new user turn too.
func TestXRGroupAffinitySessionAlwaysSameKey(t *testing.T) {
	up, h, srv := xrGrpSetup(t, provider.AffinitySession, "")
	xrGrpChat(t, h, "S", xrGrpFirst)
	xrGrpRecover(up, "k1")
	xrGrpChat(t, h, "S", xrGrpNext)
	if got := up.answers(); got[len(got)-1] != "k2" {
		t.Fatalf("keys used: %v", got)
	}
	if a := xrGrpLast(t, srv).Affinity; a == nil || a.Why != "session" || !a.Kept {
		t.Fatalf("affinity: %+v", a)
	}
}

// off keeps nothing: the tool result goes back to the first key again.
func TestXRGroupAffinityOffKeepsNothing(t *testing.T) {
	up, h, srv := xrGrpSetup(t, provider.AffinityOff, "")
	xrGrpChat(t, h, "S", xrGrpFirst)
	xrGrpRecover(up, "k1")
	xrGrpChat(t, h, "S", xrGrpTool)
	if got := up.answers(); got[len(got)-1] != "k1" {
		t.Fatalf("keys used: %v", got)
	}
	if a := xrGrpLast(t, srv).Affinity; a == nil || a.Why != "off" || a.Kept {
		t.Fatalf("affinity: %+v", a)
	}
}

// A key that answered last but is resting moves the conversation on, and
// the trace says why; a new session starts over with why first.
func TestXRGroupAffinityRestingAndFirst(t *testing.T) {
	up, h, srv := xrGrpSetup(t, "", "")
	xrGrpChat(t, h, "S", xrGrpFirst) // k2 answers
	xrGrpRecover(up, "k1")
	restingUntil.Lock()
	restingUntil.m[xrGrpRest("kp", "k2")] = time.Now().Add(time.Hour)
	restingUntil.note[xrGrpRest("kp", "k2")] = Rest{Why: failRate, Status: 429, Until: time.Now().Add(time.Hour), By: "retry-after"}
	restingUntil.Unlock()

	xrGrpChat(t, h, "S", xrGrpTool)
	if got := up.answers(); got[len(got)-1] != "k1" {
		t.Fatalf("keys used: %v", got)
	}
	if a := xrGrpLast(t, srv).Affinity; a == nil || a.Why != "resting" || a.Kept {
		t.Fatalf("affinity: %+v", a)
	}

	xrGrpChat(t, h, "T", xrGrpFirst)
	if a := xrGrpLast(t, srv).Affinity; a == nil || a.Why != "first" {
		t.Fatalf("new session: %+v", a)
	}
}

// rotate turns over by user turn: a turn's tool results stay on its first
// key, another model's request in between moves nothing, and k1→k2→k3→k1.
func TestXRGroupRotatesByTurn(t *testing.T) {
	xrGrpReset(t)
	up := xrGrpUpstream(t, "kp", "m", "k1", "k2", "k3")
	other := xrGrpUpstream(t, "other", "other", "ko")
	if err := provider.SaveGroup(provider.Group{ID: "mine", Members: []string{"kp/m"}, Routing: provider.Rotate}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	const title = `{"model":"other","messages":[{"role":"user","content":"title this"}]}`
	xrGrpChat(t, h, "S", xrGrpFirst) // turn 1: k1
	xrGrpChat(t, h, "S", xrGrpTool)  // k1
	xrGrpChat(t, h, "S", title)      // another model: not the group's
	xrGrpChat(t, h, "S", xrGrpTool)  // k1 still
	xrGrpChat(t, h, "S", xrGrpNext)  // turn 2: k2
	xrGrpChat(t, h, "S", xrGrpTool)  // k2
	xrGrpChat(t, h, "S", xrGrpNext)  // turn 3: k3
	xrGrpChat(t, h, "S", xrGrpNext)  // turn 4: k1
	want := []string{"k1", "k1", "k1", "k2", "k2", "k3", "k1"}
	got := up.answers()
	if len(got) != len(want) {
		t.Fatalf("keys used: %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keys used: %v, want %v", got, want)
		}
	}
	if n := len(other.answers()); n != 1 {
		t.Fatalf("other model answered %d times", n)
	}
}

// Keys made for another protocol come after the pool the request suits,
// marked aside.
func TestXRGroupKeysOfAnotherProtocolComeLast(t *testing.T) {
	xrGrpReset(t)
	s := &Server{}
	p := provider.Provider{ID: "gp", Name: "GP", Chat: "http://127.0.0.1:1/v1", Anthropic: "http://127.0.0.1:1",
		Key: "k-plain", Keys: []provider.KeyAccount{
			{Key: "k-ant", Protocol: provider.Anthropic},
			{Key: "k-chat", Protocol: provider.Chat},
		}}
	g := provider.Group{ID: "x"}
	ms := []provider.Member{{Provider: p, Model: "gpt-5.5"}}
	cs, pl, aff := s.planGroup(g, ms, provider.Chat, &groupCall{group: "x"})
	if aff != nil {
		t.Fatalf("no session, no affinity: %+v", aff)
	}
	var keys []string
	for _, c := range cs {
		keys = append(keys, c.p.Key)
	}
	want := []string{"k-chat", "k-plain", "k-ant"}
	if !xrEqualRoute(keys, want) {
		t.Fatalf("order %v, want %v", keys, want)
	}
	if pl.order[0].Aside || !pl.order[1].Aside || !pl.order[2].Aside {
		t.Fatalf("aside flags: %+v", pl.order)
	}
}

// Across turns, affinity keeps the conversation to the key that answered
// only when the vendor's cache served it and not long ago; within a turn it
// always does.
func TestXRAffinityAcrossTurnsNeedsCache(t *testing.T) {
	xrGrpReset(t)
	s := &Server{}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	clock = func() time.Time { return now }
	main := []scored{{c: candidate{rest: "kp#a", model: "m"}}, {c: candidate{rest: "kp#b", model: "m"}}}
	gc := &groupCall{group: "mine", session: "S"}
	g := provider.Group{ID: "mine"}
	answer := func(cacheRead int, at time.Time) {
		groupMemory.Lock()
		groupMemory.answers[gc.key()] = groupAnswer{rest: "kp#b", model: "m", at: at, cacheRead: cacheRead}
		groupMemory.Unlock()
	}
	check := func(name, why string, kept int) {
		t.Helper()
		aff, i := s.decideAffinity(gc, g, main, now)
		if aff.Why != why || i != kept {
			t.Fatalf("%s: why %q kept at %d, want %q at %d", name, aff.Why, i, why, kept)
		}
	}
	answer(2000, now.Add(-time.Minute))
	check("cache read and recent", "cache", 1)
	answer(10, now.Add(-time.Minute))
	check("cache too small", "no-cache", -1)
	answer(2000, now.Add(-10*time.Minute))
	check("too old", "cold", -1)
	gc.within = true
	answer(0, now.Add(-time.Hour))
	check("within the turn", "turn", 1)
}

func xrEqualRoute(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
