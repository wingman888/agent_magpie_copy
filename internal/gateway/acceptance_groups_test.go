package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// accUpstream is a Chat upstream that records who called it, and answers
// with status (per Authorization and session header) when set.
type accUpstream struct {
	mu     sync.Mutex
	calls  []accCall
	status func(key, session string) int
}

type accCall struct {
	key, session, body string
}

func (u *accUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c := accCall{key: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), session: r.Header.Get("x-session-id"), body: string(b)}
	u.mu.Lock()
	u.calls = append(u.calls, c)
	code := 0
	if u.status != nil {
		code = u.status(c.key, c.session)
	}
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"slow down","type":"rate_limit_error"}}`)
		return
	}
	io.WriteString(w, groupChatReply)
}

// keys lists the key each call used, in order, for calls whose body names model.
func (u *accUpstream) keys(model string) []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []string
	for _, c := range u.calls {
		if strings.Contains(c.body, fmt.Sprintf(`"model":%q`, model)) {
			out = append(out, c.key)
		}
	}
	return out
}

// accSetup saves provider "fake" (models m1, m2) with the keys given, the
// first as its own key and the rest added; the ones after the first take
// the protocol of their "proto:key" prefix when given.
func accSetup(t *testing.T, u *accUpstream, keys ...string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: keys[0], Chat: up.URL + "/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys[1:] {
		proto := provider.Protocol("")
		if name, p, ok := strings.Cut(k, ":"); ok {
			k, proto = name, provider.Protocol(p)
		}
		if err := provider.AddKey("fake", k, k, proto); err != nil {
			t.Fatal(err)
		}
	}
}

// accRoute runs one request under session (none when empty) and returns the status.
func accRoute(t *testing.T, s *Server, session, body string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	if session != "" {
		req.Header.Set("x-session-id", session)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec.Code
}

// accLast is the route the last request traced.
func accLast(t *testing.T, s *Server) (r Route) {
	t.Helper()
	st := s.Trace(context.Background(), 0, 0)
	if len(st.Routes) == 0 {
		t.Fatal("no route traced")
	}
	return st.Routes[len(st.Routes)-1]
}

// accBody is a request in group "mine" whose conversation has n user turns.
func accBody(model string, turns int) string {
	var msgs []string
	for i := 1; i <= turns; i++ {
		if i > 1 {
			msgs = append(msgs, fmt.Sprintf(`{"role":"assistant","content":"a%d"}`, i))
		}
		msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"u%d"}`, i))
	}
	return fmt.Sprintf(`{"model":%q,"messages":[%s]}`, model, strings.Join(msgs, ","))
}

// accToolBody continues the last turn of accBody(model, turns) with a tool result.
func accToolBody(model string, turns int) string {
	var msgs []string
	for i := 1; i <= turns; i++ {
		if i > 1 {
			msgs = append(msgs, fmt.Sprintf(`{"role":"assistant","content":"a%d"}`, i))
		}
		msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"u%d"}`, i))
	}
	msgs = append(msgs, `{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"c1","content":"result"}`)
	return fmt.Sprintf(`{"model":%q,"messages":[%s]}`, model, strings.Join(msgs, ","))
}

// (2) group Mine: b first (its 429 sends the request on), then a. The trace
// names group mine, and its candidates follow the member order.
func TestAccGroupMembersInOrder(t *testing.T) {
	bUp := &accUpstream{status: func(string, string) int { return 429 }}
	aUp := &accUpstream{}
	bSrv := httptest.NewServer(bUp)
	defer bSrv.Close()
	aSrv := httptest.NewServer(aUp)
	defer aSrv.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "ka", Chat: aSrv.URL + "/v1", Models: []string{"only-a"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "kb", Chat: bSrv.URL + "/v1", Models: []string{"vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"b/vendor/m", "a/only-a"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code := accRoute(t, s, "", `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(bUp.calls) == 0 || len(aUp.calls) != 1 {
		t.Fatalf("b calls %d, a calls %d", len(bUp.calls), len(aUp.calls))
	}
	r := accLast(t, s)
	if r.Group == nil || r.Group.ID != "mine" {
		t.Fatalf("group %+v", r.Group)
	}
	if len(r.Order) < 2 || r.Order[0].Provider != "b" || r.Order[1].Provider != "a" {
		var ps []string
		for _, w := range r.Order {
			ps = append(ps, w.Provider)
		}
		t.Fatalf("candidate order %v", ps)
	}
}

// (5) default mode: a tool result within the turn stays on the same key;
// a new turn with no vendor cache moves on (rotate).
func TestAccAffinityDefault(t *testing.T) {
	u := &accUpstream{}
	accSetup(t, u, "k1", "k2")
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"fake/m1"}, Routing: provider.Rotate}); err != nil {
		t.Fatal(err)
	}
	s := New()
	const sess = "sess-default"
	if c := accRoute(t, s, sess, accBody("group/mine", 1)); c != 200 {
		t.Fatalf("turn 1: %d", c)
	}
	if c := accRoute(t, s, sess, accToolBody("group/mine", 1)); c != 200 {
		t.Fatalf("tool: %d", c)
	}
	keys := u.keys("m1")
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("within the turn the keys were %v, want the same key twice", keys)
	}
	if why := accLast(t, s).Affinity.Why; why != "turn" {
		t.Errorf("tool request why = %q, want turn", why)
	}
	if c := accRoute(t, s, sess, accBody("group/mine", 2)); c != 200 {
		t.Fatalf("turn 2: %d", c)
	}
	keys = u.keys("m1")
	if len(keys) != 3 || keys[2] == keys[1] {
		t.Errorf("new turn without cache kept key: %v", keys)
	}
	if why := accLast(t, s).Affinity.Why; why != "no-cache" {
		t.Errorf("new turn why = %q, want no-cache", why)
	}
}

// (5) session mode holds the whole conversation to one key, even across turns.
func TestAccAffinitySession(t *testing.T) {
	u := &accUpstream{}
	accSetup(t, u, "k1", "k2")
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"fake/m1"}, Routing: provider.Rotate, Affinity: "session"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	const sess = "sess-session"
	for turn := 1; turn <= 3; turn++ {
		if c := accRoute(t, s, sess, accBody("group/mine", turn)); c != 200 {
			t.Fatalf("turn %d: %d", turn, c)
		}
		if why := accLast(t, s).Affinity.Why; why != "session" {
			t.Errorf("turn %d why = %q", turn, why)
		}
	}
	keys := u.keys("m1")
	if len(keys) != 3 || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Errorf("session mode keys %v, want one key", keys)
	}
}

// (5) off: a new turn is free to move on; the trace says off.
func TestAccAffinityOff(t *testing.T) {
	u := &accUpstream{}
	accSetup(t, u, "k1", "k2")
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"fake/m1"}, Routing: provider.Rotate, Affinity: "off"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	const sess = "sess-off"
	accRoute(t, s, sess, accBody("group/mine", 1))
	accRoute(t, s, sess, accBody("group/mine", 2))
	keys := u.keys("m1")
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Errorf("off mode keys %v, want the rotation to move on", keys)
	}
	if why := accLast(t, s).Affinity.Why; why != "off" {
		t.Errorf("why = %q, want off", why)
	}
}

// (6) rotate moves once per user turn: k1, k2, k3, k1, with tool results and
// another model's request in between not disturbing the order.
func TestAccRotatePerTurn(t *testing.T) {
	u := &accUpstream{}
	accSetup(t, u, "k1", "k2", "k3")
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"fake/m1"}, Routing: provider.Rotate}); err != nil {
		t.Fatal(err)
	}
	s := New()
	const sess = "sess-rotate"
	for turn := 1; turn <= 4; turn++ {
		if c := accRoute(t, s, sess, accBody("group/mine", turn)); c != 200 {
			t.Fatalf("turn %d: %d", turn, c)
		}
		if c := accRoute(t, s, sess, accToolBody("group/mine", turn)); c != 200 {
			t.Fatalf("tool %d: %d", turn, c)
		}
		// an interleaved request for another model in the same conversation
		if c := accRoute(t, s, sess, accBody("fake/m2", turn)); c != 200 {
			t.Fatalf("other model %d: %d", turn, c)
		}
	}
	keys := u.keys("m1")
	want := []string{"k1", "k1", "k2", "k2", "k3", "k3", "k1", "k1"}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Errorf("rotation keys %v, want %v", keys, want)
	}
}

// (7) the last answerer is resting: the request goes to another key with why
// resting; a new session id says first.
func TestAccRestingAndFirst(t *testing.T) {
	u := &accUpstream{}
	accSetup(t, u, "k1", "k2")
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"fake/m1"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetRouting("fake", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	s := New()
	const sess = "sess-rest"
	if c := accRoute(t, s, sess, accBody("group/mine", 1)); c != 200 {
		t.Fatalf("first: %d", c)
	}
	if k := u.keys("m1"); len(k) != 1 || k[0] != "k1" {
		t.Fatalf("first request used %v, want k1", k)
	}
	// k1 gets rate limited in another session, so it rests
	u.mu.Lock()
	u.status = func(key, session string) int {
		if key == "k1" && session == "sess-other" {
			return 429
		}
		return 0
	}
	u.mu.Unlock()
	if c := accRoute(t, s, "sess-other", accBody("group/mine", 1)); c != 200 {
		t.Fatalf("other session: %d", c)
	}
	if c := accRoute(t, s, sess, accToolBody("group/mine", 1)); c != 200 {
		t.Fatalf("resting: %d", c)
	}
	if why := accLast(t, s).Affinity.Why; why != "resting" {
		t.Errorf("why = %q, want resting", why)
	}
	if k := u.keys("m1"); k[len(k)-1] != "k2" {
		t.Errorf("request while k1 rests went to %v", k)
	}
	if c := accRoute(t, s, "sess-new", accBody("group/mine", 1)); c != 200 {
		t.Fatalf("new session: %d", c)
	}
	if why := accLast(t, s).Affinity.Why; why != "first" {
		t.Errorf("new session why = %q, want first", why)
	}
}

// (9) with rotate, the Chat requests go round the keys that fit them
// (unspecified), and the key made for another protocol is last, aside; it
// answers when the main pool has failed.
func TestAccProtocolTiers(t *testing.T) {
	u := &accUpstream{}
	accSetup(t, u, "k1", "k2", "kanth:"+string(provider.Anthropic))
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"fake/m1"}, Routing: provider.Rotate}); err != nil {
		t.Fatal(err)
	}
	s := New()
	const sess = "sess-tiers"
	for turn := 1; turn <= 3; turn++ {
		if c := accRoute(t, s, sess, accBody("group/mine", turn)); c != 200 {
			t.Fatalf("turn %d: %d", turn, c)
		}
	}
	keys := u.keys("m1")
	if fmt.Sprint(keys) != "[k1 k2 k1]" {
		t.Errorf("chat requests went to %v, want k1 k2 k1 only", keys)
	}
	// main pool fails: the aside key answers
	u.mu.Lock()
	u.status = func(key, _ string) int {
		if key == "kanth" {
			return 0
		}
		return 429
	}
	u.mu.Unlock()
	if c := accRoute(t, s, sess, accBody("group/mine", 4)); c != 200 {
		t.Fatalf("with the main pool failing: %d", c)
	}
	r := accLast(t, s)
	last := r.Order[len(r.Order)-1]
	if !last.Aside {
		t.Errorf("last candidate not aside: %+v", last)
	}
	if k := u.keys("m1"); k[len(k)-1] != "kanth" {
		t.Errorf("aside key not used last: %v", k)
	}
}
