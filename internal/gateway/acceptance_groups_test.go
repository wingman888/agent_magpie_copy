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

// accUpstream is a fake vendor that records who called it. status, when set,
// answers with its code for a call (by the key used and the request body,
// since the session header is not forwarded upstream).
type accUpstream struct {
	mu     sync.Mutex
	calls  []accCall
	status func(key, body string) int
}

type accCall struct {
	key, body string
}

const accAnthropicReply = `{"id":"m1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

func (u *accUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if key == "" {
		key = r.Header.Get("x-api-key")
	}
	c := accCall{key: key, body: string(b)}
	u.mu.Lock()
	u.calls = append(u.calls, c)
	code := 0
	if u.status != nil {
		code = u.status(c.key, c.body)
	}
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"slow down","type":"rate_limit_error"}}`)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/messages") {
		io.WriteString(w, accAnthropicReply)
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
	// resting is process-wide and keyed by key fingerprint, so start clean and
	// leave clean: a key that rested in another test would rest here too
	accClearResting()
	t.Cleanup(accClearResting)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: keys[0], Chat: up.URL + "/v1", Models: []string{"m1", "m2"}}
	for _, k := range keys {
		if strings.HasSuffix(k, ":"+string(provider.Anthropic)) {
			// a key made for Anthropic needs an Anthropic endpoint to be used
			p.Anthropic = up.URL
		}
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys[1:] {
		proto := provider.Protocol("")
		if name, pr, ok := strings.Cut(k, ":"); ok {
			k, proto = name, provider.Protocol(pr)
		}
		if err := provider.AddKey("fake", k, k, proto); err != nil {
			t.Fatal(err)
		}
	}
}

// accClearResting forgets every key that is resting.
func accClearResting() {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	for k := range restingUntil.m {
		delete(restingUntil.m, k)
	}
	for k := range restingUntil.note {
		delete(restingUntil.note, k)
	}
}

// accRoute runs one request under session (none when empty) and returns the status.
func accRoute(t *testing.T, s *Server, session, body string) int {
	t.Helper()
	code, _ := accRouteBody(t, s, session, body)
	return code
}

// accRouteBody is accRoute with the response body too.
func accRouteBody(t *testing.T, s *Server, session, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	if session != "" {
		req.Header.Set("x-session-id", session)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
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

// accDump logs the last route's candidates and tries, for diagnosis.
func accDump(t *testing.T, s *Server) {
	t.Helper()
	r := accLast(t, s)
	t.Logf("DUMP orders=%d tries=%d", len(r.Order), len(r.Tries))
	for i, w := range r.Order {
		t.Logf("DUMP order idx=%d id=%s provider=%s model=%s aside=%v fit=%d speaks=%s rest=%v", i, w.ID, w.Provider, w.Model, w.Aside, w.Fit, w.Speaks, w.Rest != nil)
	}
	for _, x := range r.Tries {
		t.Logf("DUMP try id=%s status=%d fail=%s err=%s", x.ID, x.Status, x.Fail, x.Error)
	}
	t.Logf("DUMP left=%d status=%d", len(r.Left), r.Status)
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
	accClearResting()
	t.Cleanup(accClearResting)
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
		// the first turn has no answer to keep yet
		want := "session"
		if turn == 1 {
			want = "first"
		}
		if why := accLast(t, s).Affinity.Why; why != want {
			t.Errorf("turn %d why = %s, want %s", turn, why, want)
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
	// k1 gets rate limited for a request of another conversation (marked by
	// its text), so it rests; that other conversation is answered by k2
	u.mu.Lock()
	u.status = func(key, body string) int {
		if key == "k1" && strings.Contains(body, "OTHER") {
			return 429
		}
		return 0
	}
	u.mu.Unlock()
	other := `{"model":"group/mine","messages":[{"role":"user","content":"OTHER"}]}`
	if c := accRoute(t, s, "sess-other", other); c != 200 {
		t.Fatalf("other session: %d", c)
	}
	if k := u.keys("m1"); len(k) != 3 || k[1] != "k1" || k[2] != "k2" {
		t.Fatalf("other session calls %v, want k1 (429) then k2", k)
	}
	if c := accRoute(t, s, sess, accToolBody("group/mine", 1)); c != 200 {
		t.Fatalf("resting: %d", c)
	}
	if why := accLast(t, s).Affinity.Why; why != "resting" {
		t.Errorf("why = %s, want resting", why)
	}
	if k := u.keys("m1"); len(k) != 4 || k[3] != "k2" {
		t.Errorf("request while k1 rests went to %v, want k2", k)
	}
	if c := accRoute(t, s, "sess-new", accBody("group/mine", 1)); c != 200 {
		t.Fatalf("new session: %d", c)
	}
	if why := accLast(t, s).Affinity.Why; why != "first" {
		t.Errorf("new session why = %s, want first", why)
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
		if turn == 1 {
			accDump(t, s)
		}
	}
	keys := u.keys("m1")
	if fmt.Sprint(keys) != "[k1 k2 k1]" {
		accDump(t, s)
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
	if g, ms, ok := provider.FindGroup("group/mine"); ok {
		cs, _ := s.planGroup(g, ms, provider.Chat, convo{id: sess, model: "group/mine", turns: 4})
		t.Logf("PLAN count=%d", len(cs))
		for i, c := range cs {
			t.Logf("PLAN idx=%d rest=%s aside=%v key=%s", i, c.rest, c.aside, c.p.KeyProtocol)
		}
	}
	if c, body := accRouteBody(t, s, sess, accBody("group/mine", 4)); c != 200 {
		accDump(t, s)
		t.Fatalf("with the main pool failing: %d body %s", c, body)
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
