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

// keyed answers per API key, saying it read 2500 tokens from its cache; a
// key in limited answers 429.
type keyed struct {
	mu      sync.Mutex
	limited map[string]bool
	seen    []string
}

func (k *keyed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	k.mu.Lock()
	k.seen = append(k.seen, key)
	limited := k.limited[key]
	k.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if limited {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, `{"id":"from-`+key+`","choices":[],"usage":{"prompt_tokens":3000,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2500}}}`)
}

func (k *keyed) last() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.seen) == 0 {
		return ""
	}
	return k.seen[len(k.seen)-1]
}

func fresh(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	answers.Lock()
	answers.m = map[string]answered{}
	answers.Unlock()
	routed.Lock()
	routed.turn = map[string]int{}
	routed.Unlock()
}

const (
	turn1 = `{"role":"user","content":"hi"}`
	reply = `{"role":"assistant","content":"hello"}`
	turn2 = `{"role":"user","content":"and then?"}`
	calls = `{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]}`
	tool  = `{"role":"tool","tool_call_id":"c1","content":"files"}`
)

func chatBody(model string, msgs ...string) string {
	return `{"model":"` + model + `","messages":[` + strings.Join(msgs, ",") + `]}`
}

// ask sends a chat request in a session and gives the reply and its route.
func ask1(t *testing.T, s *Server, session, body string) (*httptest.ResponseRecorder, Route) {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("x-session-id", session)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	rs := s.Trace(context.Background(), 0, 0).Routes
	return rec, rs[len(rs)-1]
}

func answerer(rec *httptest.ResponseRecorder) string {
	var v struct {
		ID string `json:"id"`
	}
	json.Unmarshal(rec.Body.Bytes(), &v)
	return strings.TrimPrefix(v.ID, "from-")
}

func saveKeys(t *testing.T, id, url, routing, affinity string, keys ...provider.KeyAccount) {
	t.Helper()
	p := provider.Provider{ID: id, Name: id, Chat: url + "/v1", Models: []string{"m", "small"}, Routing: routing, Affinity: affinity,
		Key: keys[0].Key, KeyName: keys[0].Name, KeyProtocol: keys[0].Protocol, Keys: keys[1:]}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func TestAffinityAfterAnAnswerReadFromCache(t *testing.T) {
	for _, tc := range []struct {
		name, routing, affinity string
		second                  []string
		want, why               string
	}{
		{"default stays on the cache", "", "", []string{turn1, reply, turn2}, "k1", whyCache},
		{"rotate hands the next turn on", provider.Rotate, "", []string{turn1, reply, turn2}, "k2", whyNewTurn},
		{"rotate by turn", provider.Rotate, provider.AffinityTurn, []string{turn1, reply, turn2}, "k2", whyNewTurn},
		{"rotate by session", provider.Rotate, provider.AffinitySession, []string{turn1, reply, turn2}, "k1", whySession},
		{"rotate with none", provider.Rotate, provider.AffinityOff, []string{turn1, reply, turn2}, "k2", whyOff},
		{"rotate keeps a turn together", provider.Rotate, "", []string{turn1, calls, tool}, "k1", whyTurn},
		{"turn keeps a turn together", provider.Rotate, provider.AffinityTurn, []string{turn1, calls, tool}, "k1", whyTurn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			up := &keyed{}
			srv := httptest.NewServer(up)
			defer srv.Close()
			saveKeys(t, "aff", srv.URL, tc.routing, tc.affinity, provider.KeyAccount{Key: "k1"}, provider.KeyAccount{Key: "k2"})
			s := New()
			rec, r := ask1(t, s, "s1", chatBody("aff/m", turn1))
			if got := answerer(rec); got != "k1" {
				t.Fatalf("first turn: %s %s", got, rec.Body)
			}
			if r.Affinity == nil || r.Affinity.Why != whyFirst || r.Affinity.Turn != 1 {
				t.Fatalf("first: %+v", r.Affinity)
			}
			rec, r = ask1(t, s, "s1", chatBody("aff/m", tc.second...))
			if got := answerer(rec); got != tc.want {
				t.Fatalf("second landed on %s, want %s", got, tc.want)
			}
			a := r.Affinity
			if a == nil || a.Why != tc.why || a.Last != "aff" && a.Last == "" || a.CacheRead != 2500 || a.Mode != tc.affinity {
				t.Fatalf("affinity %+v", a)
			}
			if wantTurns := map[bool]int{false: 2, true: 1}[tc.second[len(tc.second)-1] == tool]; a.Turn != wantTurns || a.Within != (wantTurns == 1) {
				t.Fatalf("turns %+v", a)
			}
		})
	}
}

func TestRotateHandsOutTurns(t *testing.T) {
	fresh(t)
	up := &keyed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	saveKeys(t, "aff", srv.URL, provider.Rotate, "", provider.KeyAccount{Key: "k1"}, provider.KeyAccount{Key: "k2"}, provider.KeyAccount{Key: "k3"})
	s := New()
	var msgs []string
	for i, want := range []string{"k1", "k2", "k3", "k1"} {
		msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"turn %d"}`, i))
		rec, _ := ask1(t, s, "s1", chatBody("aff/m", msgs...))
		if got := answerer(rec); got != want {
			t.Fatalf("turn %d: %s, want %s", i+1, got, want)
		}
		// a side request for a small model, and the agent's tool round
		if rec, _ := ask1(t, s, "s1", chatBody("aff/small", `{"role":"user","content":"title?"}`)); rec.Code != 200 {
			t.Fatalf("small: %d", rec.Code)
		}
		rec, r := ask1(t, s, "s1", chatBody("aff/m", append(msgs, calls, tool)...))
		if got := answerer(rec); got != want || r.Affinity.Why != whyTurn {
			t.Fatalf("tool round of turn %d: %s %+v", i+1, got, r.Affinity)
		}
		msgs = append(msgs, reply)
	}
}

func TestAffinityLeavesAnAnswererThatRests(t *testing.T) {
	fresh(t)
	up := &keyed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	saveKeys(t, "aff", srv.URL, "", provider.AffinitySession, provider.KeyAccount{Key: "k1"}, provider.KeyAccount{Key: "k2"})
	s := New()
	if rec, _ := ask1(t, s, "s1", chatBody("aff/m", turn1)); answerer(rec) != "k1" {
		t.Fatal("first")
	}
	rec, r := ask1(t, s, "s1", chatBody("aff/m", turn1, reply, turn2))
	if answerer(rec) != "k1" || r.Affinity.Why != whySession || !r.Affinity.Kept {
		t.Fatalf("session: %s %+v", answerer(rec), r.Affinity)
	}
	up.mu.Lock()
	up.limited["k1"] = true
	up.mu.Unlock()
	// k1 fails the next one, and rests
	ask1(t, s, "s1", chatBody("aff/m", turn1, reply, turn2, reply, turn2))
	up.mu.Lock()
	up.limited["k1"] = false
	up.mu.Unlock()
	rec, r = ask1(t, s, "s1", chatBody("aff/m", turn1, reply, turn2))
	if got := answerer(rec); got != "k2" {
		t.Fatalf("after a rest: %s", got)
	}
	if r.Affinity.Why != whyResting && r.Affinity.Why != whySession {
		t.Fatalf("why %+v", r.Affinity)
	}
	// k2 answered; it rests k1 until later, and a fresh session starts over
	_, r = ask1(t, s, "other", chatBody("aff/m", turn1))
	if r.Affinity.Why != whyFirst {
		t.Fatalf("new session: %+v", r.Affinity)
	}
}

// k1 resting when the conversation's last answerer: why says so.
func TestAffinityWhyResting(t *testing.T) {
	fresh(t)
	up := &keyed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	saveKeys(t, "aff", srv.URL, "", provider.AffinitySession, provider.KeyAccount{Key: "k1"}, provider.KeyAccount{Key: "k2"})
	s := New()
	ask1(t, s, "s1", chatBody("aff/m", turn1))
	restingUntil.Lock()
	restingUntil.m["aff#"+provider.KeyID("k1")] = time.Now().Add(time.Minute)
	restingUntil.Unlock()
	rec, r := ask1(t, s, "s1", chatBody("aff/m", turn1, reply, turn2))
	if answerer(rec) != "k2" || r.Affinity.Why != whyResting || r.Affinity.Kept {
		t.Fatalf("%s %+v", answerer(rec), r.Affinity)
	}
}

func TestKeysOfOtherProtocolsWaitBehind(t *testing.T) {
	fresh(t)
	up := &keyed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	saveKeys(t, "aff", srv.URL, provider.Rotate, "",
		provider.KeyAccount{Key: "k1", Protocol: provider.Chat}, provider.KeyAccount{Key: "k2"}, provider.KeyAccount{Key: "k3", Protocol: provider.Chat})
	s := New()
	var got []string
	var r Route
	var rec *httptest.ResponseRecorder
	for i := range 4 {
		rec, r = ask1(t, s, "s1", chatBody("aff/m", fmt.Sprintf(`{"role":"user","content":"%d"}`, i)))
		got = append(got, answerer(rec))
	}
	if strings.Join(got, ",") != "k1,k3,k1,k3" {
		t.Fatalf("rotation %v", got)
	}
	n := len(r.Order)
	if n != 3 || !r.Order[n-1].Aside || r.Order[n-1].Who == "" || r.Order[0].Aside {
		t.Fatalf("order %+v", r.Order)
	}
	// when the pool fails, the other protocol's key takes it
	up.mu.Lock()
	up.limited["k1"], up.limited["k3"] = true, true
	up.mu.Unlock()
	rec, _ = ask1(t, s, "s2", chatBody("aff/m", turn1))
	if answerer(rec) != "k2" || rec.Code != 200 {
		t.Fatalf("aside: %d %s", rec.Code, rec.Body)
	}
}

func TestGroupRoutesOverItsMembers(t *testing.T) {
	fresh(t)
	up := &keyed{limited: map[string]bool{"kb": true}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Chat: srv.URL + "/v1", Models: []string{"m", "only-a"}, Key: "ka"},
		{ID: "b", Name: "B", Chat: srv.URL + "/v1", Models: []string{"vendor/m"}, Key: "kb"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Routing: provider.Ordered, Members: []string{"b/vendor/m", "a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec, r := ask1(t, s, "s1", chatBody("group/mine", turn1))
	if rec.Code != 200 || answerer(rec) != "ka" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	tried := 0
	for _, k := range up.seen {
		if k == "kb" {
			tried++
		}
	}
	if tried != 1 || len(up.seen) != 2 {
		t.Fatalf("tries %v", up.seen)
	}
	if r.Group == nil || r.Group.ID != "mine" || len(r.Group.Members) != 2 || len(r.Order) != 2 ||
		r.Order[0].Provider != "b" || r.Order[1].Model != "only-a" || r.Affinity == nil {
		t.Fatalf("route %+v", r)
	}

	// unified routing over every member's keys
	if err := provider.SaveGroup(provider.Group{ID: "all", Routing: provider.Rotate, Members: []string{"a/m", "b/vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	up.mu.Lock()
	up.limited = map[string]bool{}
	up.mu.Unlock()
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	var got []string
	for i := range 4 {
		rec, _ = ask1(t, s, "g", chatBody("group/all", fmt.Sprintf(`{"role":"user","content":"%d"}`, i)))
		got = append(got, answerer(rec))
	}
	if strings.Join(got, ",") != "ka,kb,ka,kb" {
		t.Fatalf("group rotation %v", got)
	}
}

func TestGroupWithNoMemberReady(t *testing.T) {
	fresh(t)
	if err := provider.SaveGroup(provider.Group{ID: "ghosts", Members: []string{"nobody/m"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		req := httptest.NewRequest("POST", path, strings.NewReader(chatBody("group/ghosts", turn1)))
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), "none of group/ghosts's models is ready") {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}
