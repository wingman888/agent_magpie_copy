package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// routeSetup is a clean gateway over one fake upstream that every key of
// every provider in a test is pointed at.
func routeSetup(t *testing.T) (*Server, *byKey, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	clearRest()
	up := &byKey{limited: map[string]bool{}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	return New(), up, srv.URL
}

func clearRest() {
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.note = map[string]Rest{}
	restingUntil.Unlock()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// sendRoute posts body to path with the headers, and answers what it got.
func sendRoute(s *Server, path, body string, h http.Header) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	for k, v := range h {
		req.Header[k] = v
	}
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// lastRoute is the most recent route in the trace.
func lastRoute(s *Server) Route {
	st := s.Trace(context.Background(), 0, 0)
	return st.Routes[len(st.Routes)-1]
}

// seenKeys is the keys the upstream saw, in order, and forgets them.
func seenKeys(up *byKey) string {
	s := strings.Join(up.seen, " ")
	up.seen = nil
	return s
}

func TestGroupRoutesAcrossMembersAndFallsBack(t *testing.T) {
	s, up, url := routeSetup(t)
	up.limited["k-b"] = true
	must(t, provider.Save(provider.Provider{ID: "a", Name: "A", Chat: url + "/v1", Key: "k-a", Models: []string{"only-a"}}))
	must(t, provider.Save(provider.Provider{ID: "b", Name: "B", Chat: url + "/v1", Key: "k-b", Models: []string{"vendor/m"}}))
	must(t, provider.SaveGroup(provider.Group{ID: "mine", Members: []string{"b/vendor/m", "a/only-a"}, Routing: provider.Ordered}))

	code, body := sendRoute(s, "/v1/chat/completions", `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`, nil)
	if code != 200 || !strings.Contains(body, "from-k-a") {
		t.Fatalf("group: %d %s", code, body)
	}
	if got := seenKeys(up); got != "k-b k-a" {
		t.Fatalf("upstream saw %q, want members in order", got)
	}
	r := lastRoute(s)
	if r.Group == nil || r.Group.ID != "mine" || r.Group.Routing != provider.Ordered {
		t.Fatalf("group trace %+v", r.Group)
	}
	if strings.Join(r.Group.Members, " ") != "b/vendor/m a/only-a" {
		t.Fatalf("group members %v", r.Group.Members)
	}
	if len(r.Order) != 2 || r.Order[0].Provider != "b" || r.Order[1].Provider != "a" {
		t.Fatalf("order %+v", r.Order)
	}
	if len(r.Tries) != 2 || r.Tries[0].Status != 429 || r.Tries[1].Status != 200 {
		t.Fatalf("tries %+v", r.Tries)
	}
}

func TestGroupWithNoReadyMemberIsNotFound(t *testing.T) {
	s, _, _ := routeSetup(t)
	if err := provider.SaveGroup(provider.Group{ID: "empty"}); err == nil {
		t.Fatal("a group with no members was saved")
	}
	must(t, provider.SaveGroup(provider.Group{ID: "ghosts", Members: []string{"ghost/x"}}))
	code, body := sendRoute(s, "/v1/chat/completions", `{"model":"group/ghosts","messages":[{"role":"user","content":"hi"}]}`, nil)
	if code != 404 || !strings.Contains(body, "没有模型就绪") {
		t.Fatalf("no ready member: %d %s", code, body)
	}
}

// Affinity: a conversation stays on the key that answered it last while a
// tool result comes back in its turn, and the mode and rests decide the rest.
func TestAffinityKeepsAConversationOnItsAnswerer(t *testing.T) {
	s, up, url := routeSetup(t)
	up.limited["k1"] = true
	must(t, provider.Save(provider.Provider{ID: "sticky", Name: "Sticky", Chat: url + "/v1", Key: "k1", KeyName: "One",
		Keys: []provider.KeyAccount{{Name: "Two", Key: "k2"}}, Models: []string{"m1"}}))
	rest1, rest2 := "sticky#"+provider.KeyID("k1"), "sticky#"+provider.KeyID("k2")
	h := http.Header{"X-Session-Id": {"s-affinity"}}
	turn := `{"model":"sticky/m1","messages":[{"role":"user","content":"hi"}]}`
	within := `{"model":"sticky/m1","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"calling"},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`

	// k1 is limited, so k2 answers the first request
	if code, body := sendRoute(s, "/v1/chat/completions", turn, h); code != 200 {
		t.Fatalf("first: %d %s", code, body)
	}
	if got := seenKeys(up); got != "k1 k2" {
		t.Fatalf("first upstream saw %q", got)
	}
	clearRest()

	// the tool result within the turn goes back to k2, though k1 is first in order
	if code, _ := sendRoute(s, "/v1/chat/completions", within, h); code != 200 {
		t.Fatal("within")
	}
	if got := seenKeys(up); got != "k2" {
		t.Fatalf("within the turn the upstream saw %q, want k2", got)
	}
	if a := lastRoute(s).Affinity; a == nil || !a.Kept || a.Why != "turn" || a.Last != rest2 || !a.Within {
		t.Fatalf("affinity within %+v", a)
	}

	// off: the same request now goes to k1, and the trace says why
	delete(up.limited, "k1")
	must(t, provider.SetAffinity("sticky", provider.AffinityOff))
	sendRoute(s, "/v1/chat/completions", within, h)
	if got := seenKeys(up); got != "k1" {
		t.Fatalf("off upstream saw %q, want k1", got)
	}
	if a := lastRoute(s).Affinity; a == nil || a.Kept || a.Why != "off" || a.Mode != provider.AffinityOff {
		t.Fatalf("affinity off %+v", a)
	}

	// session: kept across turns, whatever the cache says
	must(t, provider.SetAffinity("sticky", provider.AffinitySession))
	sendRoute(s, "/v1/chat/completions", turn, h)
	if a := lastRoute(s).Affinity; a == nil || !a.Kept || a.Why != "session" {
		t.Fatalf("affinity session %+v", a)
	}

	// automatic: across turns the answerer is kept only with a warm cache, so
	// it is not kept here; the rotation-free order decides
	must(t, provider.SetAffinity("sticky", ""))
	sendRoute(s, "/v1/chat/completions", turn, h)
	if a := lastRoute(s).Affinity; a == nil || a.Kept || a.Why != "no-cache" {
		t.Fatalf("affinity auto across turns %+v", a)
	}
	clearRest()

	// session again, and the answerer resting: the conversation moves on
	must(t, provider.SetAffinity("sticky", provider.AffinitySession))
	sendRoute(s, "/v1/chat/completions", turn, h)
	seenKeys(up)
	restingUntil.Lock()
	restingUntil.m[rest1] = time.Now().Add(time.Hour)
	restingUntil.note[rest1] = Rest{Why: failRate, By: "cooldown"}
	restingUntil.Unlock()
	sendRoute(s, "/v1/chat/completions", turn, h)
	if a := lastRoute(s).Affinity; a == nil || a.Kept || a.Why != "resting" {
		t.Fatalf("affinity resting %+v", a)
	}
	if got := seenKeys(up); got != "k2" {
		t.Fatalf("resting upstream saw %q, want k2", got)
	}
	clearRest()

	// another conversation starts afresh
	other := http.Header{"X-Session-Id": {"s-affinity-2"}}
	sendRoute(s, "/v1/chat/completions", within, other)
	if a := lastRoute(s).Affinity; a == nil || a.Why != "first" {
		t.Fatalf("new session %+v", a)
	}
}

// Rotation by turn: the turns of one conversation go round the keys, a turn's
// tool results stay on its key, and another model's requests don't move it.
func TestRotationGoesByTurn(t *testing.T) {
	s, up, url := routeSetup(t)
	must(t, provider.Save(provider.Provider{ID: "rot", Name: "Rot", Chat: url + "/v1", Key: "k1",
		Keys: []provider.KeyAccount{{Name: "Two", Key: "k2"}, {Name: "Three", Key: "k3"}}, Models: []string{"m1"},
		Routing: provider.Rotate}))
	must(t, provider.Save(provider.Provider{ID: "aux", Name: "Aux", Chat: url + "/v1", Key: "k-aux", Models: []string{"x"}}))
	h := http.Header{"X-Session-Id": {"s-rotate"}}
	turn := func(text string) string {
		return `{"model":"rot/m1","messages":[{"role":"user","content":"` + text + `"}]}`
	}
	within := `{"model":"rot/m1","messages":[{"role":"user","content":"t1"},{"role":"assistant","content":"calling"},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`
	steps := []struct{ body, want string }{
		{turn("t1"), "k1"},
		{within, "k1"},
		{turn("t2"), "k2"},
		{`{"model":"aux/x","messages":[{"role":"user","content":"title"}]}`, "k-aux"},
		{turn("t3"), "k3"},
		{turn("t4"), "k1"},
	}
	for i, st := range steps {
		if code, body := sendRoute(s, "/v1/chat/completions", st.body, h); code != 200 {
			t.Fatalf("step %d: %d %s", i, code, body)
		}
		if got := strings.Fields(seenKeys(up))[0]; got != st.want {
			t.Fatalf("step %d went to %s, want %s", i, got, st.want)
		}
	}
}

// Keys made for another protocol are asides: the main pool is tried first, and
// they only after it has failed. The trace marks them.
func TestKeysMadeForAnotherProtocolAreAsides(t *testing.T) {
	s, up, url := routeSetup(t)
	up.limited["kchat"] = true
	must(t, provider.Save(provider.Provider{ID: "mix", Name: "Mix", Chat: url + "/v1", Key: "ku1",
		Keys: []provider.KeyAccount{{Name: "ku2", Key: "ku2"}, {Name: "chat", Key: "kchat", Protocol: provider.Chat}},
		Models: []string{"m1"}}))
	code, body := sendRoute(s, "/v1/chat/completions", `{"model":"mix/m1","messages":[{"role":"user","content":"hi"}]}`, nil)
	if code != 200 || !strings.Contains(body, "from-ku1") {
		t.Fatalf("asides: %d %s", code, body)
	}
	if got := seenKeys(up); got != "kchat ku1" {
		t.Fatalf("upstream saw %q, want the Chat key first, then the unspecified", got)
	}
	r := lastRoute(s)
	if len(r.Order) != 3 || r.Order[0].Aside || !r.Order[1].Aside || !r.Order[2].Aside {
		t.Fatalf("order %+v", r.Order)
	}
}
