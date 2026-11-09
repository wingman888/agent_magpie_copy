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

// R2 acceptance tests for routing groups over HTTP, conversation affinity,
// rotation by turn, and protocol pools. Helpers are prefixed r2.

func r2Reset(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.note = map[string]Rest{}
	restingUntil.Unlock()
}

// r2Upstream answers per API key; a key listed in limited gets a 429.
type r2Upstream struct {
	limited map[string]bool
	seen    []string // the key each request carried, in order
}

func (u *r2Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u.seen = append(u.seen, key)
	w.Header().Set("Content-Type", "application/json")
	if u.limited[key] {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, `{"id":"from-`+key+`","choices":[]}`)
}

// r2Serve starts an upstream for h and saves p to talk to it over Chat.
func r2Serve(t *testing.T, p provider.Provider, h http.Handler) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	p.Chat = up.URL + "/v1"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return up
}

// r2Post sends one chat request through s, in the conversation session.
func r2Post(s *Server, session, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	if session != "" {
		req.Header.Set("x-session-id", session)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// r2Last is the newest route the trace holds.
func r2Last(t *testing.T, s *Server) Route {
	t.Helper()
	st := s.Trace(context.Background(), 0, 0)
	if len(st.Routes) == 0 {
		t.Fatal("no route traced")
	}
	return st.Routes[len(st.Routes)-1]
}

const (
	r2User   = `{"role":"user","content":"hi"}`
	r2Call   = `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"ls","arguments":"{}"}}]}`
	r2Result = `{"role":"tool","tool_call_id":"call_1","content":"ok"}`
	r2Done   = `{"role":"assistant","content":"done"}`
	r2Next   = `{"role":"user","content":"next"}`
)

func r2Body(model string, msgs ...string) string {
	return `{"model":"` + model + `","messages":[` + strings.Join(msgs, ",") + `]}`
}

// Acceptance 2: a saved group in order tries b first; b's 429 falls to a; the
// trace names group mine and lists the candidates in member order.
func TestR2GroupOrderFallsToSecondMember(t *testing.T) {
	r2Reset(t)
	b := &r2Upstream{limited: map[string]bool{"r2-kb": true}}
	a := &r2Upstream{}
	r2Serve(t, provider.Provider{ID: "b", Name: "B", Key: "r2-kb", Models: []string{"vendor/m"}}, b)
	r2Serve(t, provider.Provider{ID: "a", Name: "A", Key: "r2-ka", Models: []string{"only-a"}}, a)
	if err := provider.SaveGroup(provider.Group{ID: "mine", Members: []string{"b/vendor/m", "a/only-a"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := r2Post(s, "", r2Body("group/mine", r2User))
	if code != 200 || !strings.Contains(body, "from-r2-ka") {
		t.Fatalf("%d %s", code, body)
	}
	if len(b.seen) != 1 || len(a.seen) != 1 {
		t.Fatalf("b tried %d times, a %d; want 1 each", len(b.seen), len(a.seen))
	}
	r := r2Last(t, s)
	if r.Group == nil || r.Group.ID != "mine" {
		t.Fatalf("group in trace: %+v", r.Group)
	}
	if len(r.Group.Members) != 2 || r.Group.Members[0] != "b/vendor/m" || r.Group.Members[1] != "a/only-a" {
		t.Fatalf("group members in trace: %v", r.Group.Members)
	}
	if len(r.Order) < 2 || r.Order[0].Provider != "b" || r.Order[1].Provider != "a" {
		t.Fatalf("candidate order: %+v", r.Order)
	}
}

// Acceptance 3: a discovered group deleted from the config answers 404; once
// shown again it routes.
func TestR2DeletedAutoGroupUnavailable(t *testing.T) {
	r2Reset(t)
	r2Serve(t, provider.Provider{ID: "a", Name: "A", Key: "r2-ka", Models: []string{"m"}}, &r2Upstream{})
	r2Serve(t, provider.Provider{ID: "b", Name: "B", Key: "r2-kb", Models: []string{"vendor/m"}}, &r2Upstream{})
	s := New()
	if code, body := r2Post(s, "", r2Body("group/auto-m", r2User)); code != 200 {
		t.Fatalf("auto group before delete: %d %s", code, body)
	}
	if err := provider.DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if code, body := r2Post(s, "", r2Body("group/auto-m", r2User)); code != 404 {
		t.Fatalf("deleted group: %d %s, want 404", code, body)
	}
	if err := provider.ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if code, body := r2Post(s, "", r2Body("group/auto-m", r2User)); code != 200 {
		t.Fatalf("shown group: %d %s", code, body)
	}
}

// Acceptance 5: the default keeps a conversation on its key within a turn and
// not across turns without a vendor cache read; session keeps it always; off
// never does. The trace says why.
func TestR2AffinityModesAndWhy(t *testing.T) {
	r2Reset(t)
	up := &r2Upstream{}
	r2Serve(t, provider.Provider{ID: "aff", Name: "Aff", Key: "r2-a1", Keys: []provider.KeyAccount{{Key: "r2-a2"}}, Models: []string{"m"}}, up)
	s := New()
	send := func(session string, msgs ...string) (string, Route) {
		t.Helper()
		code, body := r2Post(s, session, r2Body("aff/m", msgs...))
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		return up.seen[len(up.seen)-1], r2Last(t, s)
	}

	k1, _ := send("r2-aff-auto", r2User)
	k, r := send("r2-aff-auto", r2User, r2Call, r2Result)
	if k != k1 || r.Affinity == nil || !r.Affinity.Kept || r.Affinity.Why != "turn" || !r.Affinity.Within || r.Affinity.Turn != 1 {
		t.Fatalf("auto within a turn: key %s want %s, %+v", k, k1, r.Affinity)
	}
	_, r = send("r2-aff-auto", r2User, r2Call, r2Result, r2Done, r2Next)
	if r.Affinity == nil || r.Affinity.Kept || r.Affinity.Why != "no-cache" || r.Affinity.Turn != 2 || r.Affinity.Within {
		t.Fatalf("auto across turns: %+v", r.Affinity)
	}

	if err := provider.SetAffinity("aff", provider.AffinitySession); err != nil {
		t.Fatal(err)
	}
	ks, _ := send("r2-aff-session", r2User)
	k, r = send("r2-aff-session", r2User, r2Call, r2Result, r2Done, r2Next)
	if k != ks || r.Affinity == nil || !r.Affinity.Kept || r.Affinity.Why != "session" || r.Affinity.Mode != "session" {
		t.Fatalf("session: key %s want %s, %+v", k, ks, r.Affinity)
	}

	if err := provider.SetAffinity("aff", provider.AffinityOff); err != nil {
		t.Fatal(err)
	}
	send("r2-aff-off", r2User)
	_, r = send("r2-aff-off", r2User, r2Call, r2Result)
	if r.Affinity == nil || r.Affinity.Kept || r.Affinity.Why != "off" {
		t.Fatalf("off: %+v", r.Affinity)
	}
}

// Acceptance 6: rotate moves on one key per user turn, k1, k2, k3, k1; tool
// results inside a turn stay put, and a request for another model in the same
// conversation moves no turn of this one.
func TestR2RotateTurnsByUserTurn(t *testing.T) {
	r2Reset(t)
	up := &r2Upstream{}
	r2Serve(t, provider.Provider{ID: "rot", Name: "Rot", Key: "r2-k1", Keys: []provider.KeyAccount{{Key: "r2-k2"}, {Key: "r2-k3"}},
		Models: []string{"m", "x"}, Routing: provider.Rotate}, up)
	s := New()
	const sess = "r2-rot-conv"
	send := func(model string, msgs []string) string {
		t.Helper()
		code, body := r2Post(s, sess, r2Body(model, msgs...))
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		return up.seen[len(up.seen)-1]
	}
	hist := []string{r2User}
	steps := []struct {
		add   []string
		model string
		want  string
	}{
		{nil, "rot/m", "r2-k1"},
		{[]string{r2Call, r2Result}, "rot/m", "r2-k1"},
		{nil, "rot/x", ""}, // another model: its own rotation, no turn of m moves
		{[]string{r2Done, r2Next}, "rot/m", "r2-k2"},
		{[]string{r2Call, r2Result}, "rot/m", "r2-k2"},
		{[]string{r2Done, r2Next}, "rot/m", "r2-k3"},
		{[]string{r2Call, r2Result}, "rot/m", "r2-k3"},
		{[]string{r2Done, r2Next}, "rot/m", "r2-k1"},
	}
	for i, st := range steps {
		hist = append(hist, st.add...)
		got := send(st.model, hist)
		if st.want != "" && got != st.want {
			t.Fatalf("step %d (%s): key %s, want %s (seen %v)", i, st.model, got, st.want, up.seen)
		}
	}
}

// Acceptance 7: when the key that answered a conversation is resting, the
// conversation moves to another key with why "resting"; a new conversation
// reads "first".
func TestR2AffinityRestingMovesOn(t *testing.T) {
	r2Reset(t)
	up := &r2Upstream{}
	r2Serve(t, provider.Provider{ID: "r2rest", Name: "R2 rest", Key: "r2-q1", Keys: []provider.KeyAccount{{Key: "r2-q2"}}, Models: []string{"m"}}, up)
	s := New()
	send := func(session string, msgs ...string) Route {
		t.Helper()
		code, body := r2Post(s, session, r2Body("r2rest/m", msgs...))
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		return r2Last(t, s)
	}
	send("r2-rest-1", r2User)
	if up.seen[len(up.seen)-1] != "r2-q1" {
		t.Fatalf("first answer by %v", up.seen)
	}
	restingUntil.Lock()
	restingUntil.m["r2rest#"+provider.KeyID("r2-q1")] = time.Now().Add(time.Hour)
	restingUntil.Unlock()

	r := send("r2-rest-1", r2User, r2Call, r2Result)
	if up.seen[len(up.seen)-1] != "r2-q2" || r.Affinity == nil || r.Affinity.Why != "resting" || r.Affinity.Kept {
		t.Fatalf("resting: by %s, %+v", up.seen[len(up.seen)-1], r.Affinity)
	}
	r = send("r2-rest-2", r2User)
	if r.Affinity == nil || r.Affinity.Why != "first" {
		t.Fatalf("new conversation: %+v", r.Affinity)
	}
}

// Acceptance 9: keys split by protocol. For a Chat request the Chat keys form
// the pool and rotate; the unspecified keys come next, then the Anthropic key,
// all marked aside. When the pool fails, an aside key still answers.
func TestR2ProtocolPoolsAsideLast(t *testing.T) {
	r2Reset(t)
	up := &r2Upstream{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: "r2p", Name: "R2 pool", Key: "r2-u1", Chat: srv.URL + "/v1", Anthropic: srv.URL + "/v1",
		Models: []string{"m"}, Routing: provider.Rotate,
		Keys: []provider.KeyAccount{
			{Key: "r2-u2"},
			{Key: "r2-c1", Protocol: provider.Chat},
			{Key: "r2-c2", Protocol: provider.Chat},
			{Key: "r2-x", Protocol: provider.Anthropic},
		}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := New()
	const sess = "r2-pool-conv"
	hist := []string{r2User}
	send := func() Route {
		t.Helper()
		code, body := r2Post(s, sess, r2Body("r2p/m", hist...))
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		return r2Last(t, s)
	}

	r := send()
	if got := up.seen[len(up.seen)-1]; got != "r2-c1" {
		t.Fatalf("first chat turn by %s, want r2-c1 (seen %v)", got, up.seen)
	}
	// the trace: the pool first, the rest aside, in the order unspecified then another protocol
	var asides []string
	seenAside := false
	for _, w := range r.Order {
		if w.Aside {
			seenAside = true
			asides = append(asides, string(w.Speaks))
			continue
		}
		if seenAside {
			t.Fatalf("a pool key after an aside key: %+v", r.Order)
		}
		if w.Speaks != provider.Chat {
			t.Fatalf("pool key not Chat-only: %+v", w)
		}
	}
	if len(asides) != 3 || asides[0] != "" || asides[1] != "" || asides[2] != string(provider.Anthropic) {
		t.Fatalf("aside order %v, want [unspecified unspecified anthropic]", asides)
	}

	hist = append(hist, r2Done, r2Next)
	send()
	if got := up.seen[len(up.seen)-1]; got != "r2-c2" {
		t.Fatalf("second chat turn by %s, want r2-c2", got)
	}
	hist = append(hist, r2Done, r2Next)
	send()
	if got := up.seen[len(up.seen)-1]; got != "r2-c1" {
		t.Fatalf("third chat turn by %s, want r2-c1", got)
	}

	// the pool fails: an aside key answers rather than the request failing
	up.limited = map[string]bool{"r2-c1": true, "r2-c2": true}
	hist = append(hist, r2Done, r2Next)
	code, body := r2Post(s, sess, r2Body("r2p/m", hist...))
	if code != 200 || !strings.Contains(body, "from-r2-u1") {
		t.Fatalf("with the pool down: %d %s (seen %v)", code, body, up.seen)
	}
}
