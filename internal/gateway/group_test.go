package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// grpUpstream is an upstream that answers by the key each request carries:
// a key answers with the status set for it, 200 unless said otherwise.
type grpUpstream struct {
	mu   sync.Mutex
	keys []string
	code map[string]int
	hits []string // the key each request carried, in order
}

func (u *grpUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	heads := fmt.Sprint(r.Header)
	u.mu.Lock()
	key := ""
	for _, k := range u.keys {
		if strings.Contains(heads, k) {
			key = k
			break
		}
	}
	u.hits = append(u.hits, key)
	code := u.code[key]
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code == 0 {
		code = 200
	}
	if code >= 400 {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"slow down"}}`)
		return
	}
	io.WriteString(w, `{"id":"`+provider.KeyID(key)+`","object":"chat.completion","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
}

func (u *grpUpstream) setCode(key string, code int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.code[key] = code
}

// grpIsolate points magpie's config and cache, and HOME, at temp dirs. Call it
// once per test: a second call would give the test a fresh, empty config.
func grpIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// grpProvider adds a plain provider with keys on, all served by one upstream.
func grpProvider(t *testing.T, id, routing string, keys ...string) *grpUpstream {
	t.Helper()
	up := &grpUpstream{keys: keys, code: map[string]int{}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: id, Key: keys[0], Chat: srv.URL + "/v1", Routing: routing}); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys[1:] {
		if err := provider.AddKey(id, "", k, ""); err != nil {
			t.Fatal(err)
		}
	}
	return up
}

// grpRest is the candidate id of one key of a provider with several on.
func grpRest(id, key string) string { return id + "#" + provider.KeyID(key) }

// grpChat sends a Chat request for model in a session, and returns the status.
func grpChat(t *testing.T, h http.Handler, model, session string, msgs ...string) int {
	t.Helper()
	body := `{"model":"` + model + `","messages":[` + strings.Join(msgs, ",") + `]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	if session != "" {
		req.Header.Set("x-session-id", session)
	}
	h.ServeHTTP(rec, req)
	return rec.Code
}

// grpLast is the newest routing trace, and the candidate that answered it.
func grpLast(t *testing.T, s *Server) (Route, string) {
	t.Helper()
	rs := s.Trace(context.Background(), 0, 0).Routes
	if len(rs) == 0 {
		t.Fatal("no routing trace")
	}
	r := rs[len(rs)-1]
	if len(r.Tries) == 0 {
		t.Fatal("trace has no tries")
	}
	return r, r.Tries[len(r.Tries)-1].ID
}

// grpRestFor makes a candidate rest as if it had just failed.
func grpRestFor(id string, d time.Duration) {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	restingUntil.m[id] = time.Now().Add(d)
	restingUntil.note[id] = Rest{Why: failRate, Status: 429, By: "cooldown", Until: time.Now().Add(d)}
}

const (
	grpUser1   = `{"role":"user","content":"first"}`
	grpUser2   = `{"role":"user","content":"second"}`
	grpUser3   = `{"role":"user","content":"third"}`
	grpUser4   = `{"role":"user","content":"fourth"}`
	grpCall1   = `{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}`
	grpResult1 = `{"role":"tool","tool_call_id":"c1","content":"ok"}`
)

// Scenario a: a group in order tries its first member, and falls to the
// second when the first answers 429.
func TestGroupInOrderFallsToSecondMember(t *testing.T) {
	grpIsolate(t)
	gb := grpProvider(t, "gb", "", "sk-gb")
	ga := grpProvider(t, "ga", "", "sk-ga")
	gb.setCode("sk-gb", 429)
	if err := provider.SaveGroup(provider.Group{ID: "mine", Members: []string{"gb/vendor/m", "ga/only-a"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code := grpChat(t, s.Handler(), "group/mine", "grp-sess-a", grpUser1); code != 200 {
		t.Fatalf("status %d", code)
	}
	r, answered := grpLast(t, s)
	if r.Group == nil || r.Group.ID != "mine" {
		t.Fatalf("group trace: %+v", r.Group)
	}
	if strings.Join(r.Group.Members, " ") != "gb/vendor/m ga/only-a" {
		t.Errorf("members: %v", r.Group.Members)
	}
	if len(r.Order) != 2 || r.Order[0].ID != "gb" || r.Order[1].ID != "ga" {
		t.Errorf("order: %+v", r.Order)
	}
	if answered != "ga" || len(r.Tries) != 2 || r.Tries[0].ID != "gb" || r.Tries[0].Status != 429 {
		t.Errorf("tries: %+v", r.Tries)
	}
	if len(gb.hits) != 1 || len(ga.hits) != 1 {
		t.Errorf("hits gb=%v ga=%v", gb.hits, ga.hits)
	}
}

// Scenario b: within a turn, a tool-result request stays on the key the turn
// began on, whatever the affinity mode; across turns the modes differ.
func TestAffinityKeepsConversationAsConfigured(t *testing.T) {
	grpIsolate(t)
	grpProvider(t, "af-def", provider.Rotate, "sk-1", "sk-2")
	grpProvider(t, "af-sess", provider.Rotate, "sk-1", "sk-2", "sk-3")
	grpProvider(t, "af-off", provider.Rotate, "sk-1", "sk-2")
	if err := provider.SetAffinity("af-sess", "session"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAffinity("af-off", "off"); err != nil {
		t.Fatal(err)
	}
	s := New()
	h := s.Handler()

	// default: turn 1 twice (second is the tool result), then turn 2
	if code := grpChat(t, h, "af-def/m", "grp-def", grpUser1); code != 200 {
		t.Fatalf("status %d", code)
	}
	k1 := grpRest("af-def", "sk-1")
	if r, who := grpLast(t, s); who != k1 || r.Affinity == nil || r.Affinity.Why != "first" {
		t.Fatalf("first turn: answered %s, affinity %+v", who, r.Affinity)
	}
	grpChat(t, h, "af-def/m", "grp-def", grpUser1, grpCall1, grpResult1)
	if r, who := grpLast(t, s); who != k1 || r.Affinity == nil || !r.Affinity.Kept || r.Affinity.Why != "turn" || !r.Affinity.Within {
		t.Fatalf("tool result in turn: answered %s, affinity %+v", who, r.Affinity)
	}
	if code := grpChat(t, h, "af-def/m", "grp-def", grpUser1, grpCall1, grpResult1, grpUser2); code != 200 {
		t.Fatalf("status %d", code)
	}
	if r, who := grpLast(t, s); who != grpRest("af-def", "sk-2") || r.Affinity.Why != "no-cache" {
		t.Errorf("next turn, no cache read: answered %s, affinity %+v", who, r.Affinity)
	}

	// session: the conversation stays on its first key across turns
	grpChat(t, h, "af-sess/m", "grp-sess", grpUser1)
	grpChat(t, h, "af-sess/m", "grp-sess", grpUser1, grpCall1, grpResult1, grpUser2)
	if r, who := grpLast(t, s); who != grpRest("af-sess", "sk-1") || !r.Affinity.Kept || r.Affinity.Why != "session" {
		t.Errorf("session: answered %s, affinity %+v", who, r.Affinity)
	}

	// off: rotation alone decides, every turn
	grpChat(t, h, "af-off/m", "grp-off", grpUser1)
	grpChat(t, h, "af-off/m", "grp-off", grpUser1, grpCall1, grpResult1, grpUser2)
	if r, who := grpLast(t, s); who != grpRest("af-off", "sk-2") || r.Affinity.Kept || r.Affinity.Why != "off" {
		t.Errorf("off: answered %s, affinity %+v", who, r.Affinity)
	}
}

// Scenario c: a conversation's last answerer that is resting is not kept; a
// new session id has no memory and starts afresh.
func TestAffinityLeavesRestingKey(t *testing.T) {
	grpIsolate(t)
	grpProvider(t, "rs", provider.Ordered, "sk-r1", "sk-r2")
	s := New()
	h := s.Handler()
	k1, k2 := grpRest("rs", "sk-r1"), grpRest("rs", "sk-r2")

	if code := grpChat(t, h, "rs/m", "grp-rest", grpUser1); code != 200 {
		t.Fatalf("status %d", code)
	}
	if _, who := grpLast(t, s); who != k1 {
		t.Fatalf("first answer by %s", who)
	}
	grpRestFor(k1, time.Minute)
	grpChat(t, h, "rs/m", "grp-rest", grpUser1, grpCall1, grpResult1)
	if r, who := grpLast(t, s); who != k2 || r.Affinity.Why != "resting" || r.Affinity.Kept {
		t.Errorf("resting: answered %s, affinity %+v", who, r.Affinity)
	}
	grpChat(t, h, "rs/m", "grp-rest-2", grpUser1)
	if r, who := grpLast(t, s); who != k2 || r.Affinity.Why != "first" {
		t.Errorf("new session: answered %s, affinity %+v", who, r.Affinity)
	}
}

// Scenario d: rotation moves by turn; tool results and a request to another
// model in between leave the turns where they were.
func TestRotationByTurn(t *testing.T) {
	grpIsolate(t)
	grpProvider(t, "rot", provider.Rotate, "sk-1", "sk-2", "sk-3")
	s := New()
	h := s.Handler()
	steps := []struct {
		model string
		msgs  []string
		want  string
	}{
		{"rot/m", []string{grpUser1}, "sk-1"},
		{"rot/m", []string{grpUser1, grpCall1, grpResult1}, "sk-1"},
		{"rot/other", []string{grpUser1}, ""}, // a helper request, its own rotation
		{"rot/m", []string{grpUser1, grpCall1, grpResult1}, "sk-1"},
		{"rot/m", []string{grpUser1, grpCall1, grpResult1, grpUser2}, "sk-2"},
		{"rot/m", []string{grpUser1, grpCall1, grpResult1, grpUser2, grpCall1, grpResult1}, "sk-2"},
		{"rot/m", []string{grpUser1, grpCall1, grpResult1, grpUser2, grpCall1, grpResult1, grpUser3}, "sk-3"},
		{"rot/m", []string{grpUser1, grpCall1, grpResult1, grpUser2, grpCall1, grpResult1, grpUser3, grpUser4}, "sk-1"},
	}
	for i, st := range steps {
		if code := grpChat(t, h, st.model, "grp-rot", st.msgs...); code != 200 {
			t.Fatalf("step %d: status %d", i, code)
		}
		r, who := grpLast(t, s)
		if st.want == "" {
			continue
		}
		if who != grpRest("rot", st.want) {
			t.Errorf("step %d: answered by %s, want %s (affinity %+v)", i, who, st.want, r.Affinity)
		}
	}
}

// Scenario e: a group none of whose models can be used answers 404.
func TestGroupWithoutReadyModelIs404(t *testing.T) {
	grpIsolate(t)
	if err := provider.SaveGroup(provider.Group{ID: "dead", Members: []string{"ghost/m"}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"group/dead","messages":[]}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "没有模型就绪") || !strings.Contains(rec.Body.String(), "dead") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// Scenario f: affinity keeps the conversation on its key, but a failing key
// still hands the request on to the rest.
func TestAffinityFallsBackWhenKeyFails(t *testing.T) {
	grpIsolate(t)
	up := grpProvider(t, "fb", provider.Rotate, "sk-1", "sk-2")
	if err := provider.SetAffinity("fb", "session"); err != nil {
		t.Fatal(err)
	}
	s := New()
	h := s.Handler()
	if code := grpChat(t, h, "fb/m", "grp-fb", grpUser1); code != 200 {
		t.Fatalf("status %d", code)
	}
	up.setCode("sk-1", 500)
	if code := grpChat(t, h, "fb/m", "grp-fb", grpUser1, grpCall1, grpResult1, grpUser2); code != 200 {
		t.Fatalf("status %d after the kept key failed", code)
	}
	r, who := grpLast(t, s)
	if who != grpRest("fb", "sk-2") || len(r.Tries) != 2 || r.Tries[0].ID != grpRest("fb", "sk-1") || r.Tries[0].Status != 500 {
		t.Errorf("tries: %+v", r.Tries)
	}
	if r.Affinity == nil || !r.Affinity.Kept || r.Affinity.Why != "session" {
		t.Errorf("affinity: %+v", r.Affinity)
	}
}

// Scenario g: a single candidate is not a question of affinity.
func TestSingleCandidateInvolvesNoAffinity(t *testing.T) {
	grpIsolate(t)
	grpProvider(t, "one", "", "sk-only")
	s := New()
	if code := grpChat(t, s.Handler(), "one/m", "grp-one", grpUser1); code != 200 {
		t.Fatalf("status %d", code)
	}
	if r, _ := grpLast(t, s); r.Affinity != nil {
		t.Errorf("affinity on one candidate: %+v", r.Affinity)
	}
}

func TestTurnsOf(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		turns  int
		within bool
	}{
		{"chat tool result", `{"messages":[` + grpUser1 + `,` + grpCall1 + `,` + grpResult1 + `]}`, 1, true},
		{"chat next turn", `{"messages":[` + grpUser1 + `,` + grpResult1 + `,` + grpUser2 + `]}`, 2, false},
		{"anthropic tool result", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"x"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"ok"}]}]}`, 1, true},
		{"responses tool output", `{"input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"x","output":"ok"}]}`, 1, true},
		{"responses text", `{"input":"hi"}`, 0, false},
		{"gemini function response", `{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"functionCall":{}}]},{"role":"user","parts":[{"functionResponse":{}}]}]}`, 1, true},
	}
	for _, c := range cases {
		turns, within := turnsOf([]byte(c.body))
		if turns != c.turns || within != c.within {
			t.Errorf("%s: got %d turns within=%v, want %d within=%v", c.name, turns, within, c.turns, c.within)
		}
	}
}

// The auto mode keeps across turns only when the last reply read at least
// 1024 cached tokens and came within five minutes.
func TestAffinityAutoAcrossTurns(t *testing.T) {
	cv := &convo{session: "grp-unit", model: "m", turn: 2}
	cands := func() []candidate { return []candidate{{rest: "y"}, {rest: "x"}} }
	cases := []struct {
		name string
		rec  answer
		mode string
		kept bool
		why  string
	}{
		{"cache", answer{rest: "x", turn: 1, cacheRead: 2000, at: time.Now()}, "", true, "cache"},
		{"no cache", answer{rest: "x", turn: 1, cacheRead: 100, at: time.Now()}, "", false, "no-cache"},
		{"cold", answer{rest: "x", turn: 1, cacheRead: 2000, at: time.Now().Add(-10 * time.Minute)}, "", false, "cold"},
		{"same turn", answer{rest: "x", turn: 2, cacheRead: 0, at: time.Now()}, "", true, "turn"},
		{"turn mode new turn", answer{rest: "x", turn: 1, cacheRead: 2000, at: time.Now()}, "turn", false, "new-turn"},
		{"session across turns", answer{rest: "x", turn: 1, cacheRead: 0, at: time.Now().Add(-time.Hour)}, "session", true, "session"},
		{"off", answer{rest: "x", turn: 2, cacheRead: 2000, at: time.Now()}, "off", false, "off"},
	}
	for _, c := range cases {
		affinityMem.Lock()
		affinityMem.m[cv.affinityKey()] = c.rec
		affinityMem.Unlock()
		pl := planned{order: []Weighed{{ID: "y"}, {ID: "x"}}}
		out := applyAffinity(cands(), &pl, cv, c.mode)
		if pl.affinity == nil || pl.affinity.Kept != c.kept || pl.affinity.Why != c.why {
			t.Errorf("%s: %+v", c.name, pl.affinity)
		}
		if c.kept && out[0].rest != "x" {
			t.Errorf("%s: kept but not first: %s", c.name, out[0].rest)
		}
		if len(pl.order) != len(out) || (c.kept && pl.order[0].ID != "x") {
			t.Errorf("%s: trace order out of step: %+v", c.name, pl.order)
		}
	}
}

// Memory of a conversation lasts 24 hours and no longer.
func TestAffinityMemoryLasts24Hours(t *testing.T) {
	cv := &convo{session: "grp-old", model: "m", turn: 1}
	affinityMem.Lock()
	affinityMem.m[cv.affinityKey()] = answer{rest: "x", turn: 1, at: time.Now().Add(-25 * time.Hour)}
	affinityMem.Unlock()
	if _, ok := lastAnswer(cv); ok {
		t.Fatal("a conversation's answer was remembered past 24 hours")
	}
}

func TestRoutingTraceJSON(t *testing.T) {
	r := Route{Group: &GroupTrace{ID: "mine", Name: "Mine", Members: []string{"b/vendor/m"}},
		Affinity: &AffinityTrace{Mode: "auto", Turn: 2, Within: true, Last: "b", CacheRead: 1500, Kept: true, Why: "turn"}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"group":{"id":"mine"`, `"members":["b/vendor/m"]`, `"affinity":{"mode":"auto"`, `"within":true`, `"cacheRead":1500`, `"kept":true`, `"why":"turn"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("trace JSON lacks %s: %s", want, b)
		}
	}
}
