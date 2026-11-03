package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A routing group over two providers: group/mine tries b first, then a.
func TestF09GroupTriesMembersInOrderAndFallsThrough(t *testing.T) {
	f09RouteEnv(t)
	upA := &byKey{limited: map[string]bool{}}
	srvA := httptest.NewServer(upA)
	defer srvA.Close()
	upB := &byKey{limited: map[string]bool{"kb": true}}
	srvB := httptest.NewServer(upB)
	defer srvB.Close()
	f09KeysProvider(t, srvA.URL, "a", "", "", []string{"only-a"}, "ka")
	f09KeysProvider(t, srvB.URL, "b", "", "", []string{"vendor/m"}, "kb")
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Routing: "order",
		Members: []string{"b/vendor/m", "a/only-a"}}); err != nil {
		t.Fatal(err)
	}

	s := New()
	code, body := f09SendTo(t, s.Handler(), f09ReqBody("group/mine", f09MsgUser("hi")), nil)
	if code != 200 || !strings.Contains(body, "from-ka") {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Join(upB.seen, ",") != "kb" || strings.Join(upA.seen, ",") != "ka" {
		t.Fatalf("b saw %v, a saw %v", upB.seen, upA.seen)
	}
	rt := f09LastRoute(t, s)
	if rt.Group == nil || rt.Group.ID != "mine" || rt.Group.Routing != "order" {
		t.Fatalf("group trace: %+v", rt.Group)
	}
	if strings.Join(rt.Group.Members, ",") != "b/vendor/m,a/only-a" {
		t.Fatalf("members: %v", rt.Group.Members)
	}
	if len(rt.Order) != 2 || rt.Order[0].Provider != "b" || rt.Order[1].Provider != "a" {
		t.Fatalf("order: %+v", rt.Order)
	}
	if len(rt.Tries) != 2 || rt.Tries[0].ID != "b" || rt.Tries[0].Status != 429 || rt.Tries[1].ID != "a" {
		t.Fatalf("tries: %+v", rt.Tries)
	}
}

// A group none of whose members is ready answers 404.
func TestF09GroupWithNoReadyMemberIs404(t *testing.T) {
	f09RouteEnv(t)
	if err := provider.SaveGroup(provider.Group{ID: "idle", Members: []string{"nowhere/m"}}); err != nil {
		t.Fatal(err)
	}
	code, body := f09SendTo(t, New().Handler(), f09ReqBody("group/idle", f09MsgUser("hi")), nil)
	if code != 404 || !strings.Contains(body, "没有模型就绪") {
		t.Fatalf("%d %s", code, body)
	}
}

// Auto affinity: within a turn, the tool results come back to the key that
// answered last, even though the first key is back and would be tried first.
func TestF09AffinityKeepsToolResultsOnTheAnsweringKey(t *testing.T) {
	f09RouteEnv(t)
	up := &byKey{limited: map[string]bool{"k1": true}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	f09KeysProvider(t, srv.URL, "pa", "", "", []string{"m1"}, "k1", "k2")
	s := New()
	h := s.Handler()

	if code, body := f09SendTo(t, h, f09ReqBody("pa/m1", f09MsgUser("a")), nil); code != 200 || !strings.Contains(body, "from-k2") {
		t.Fatalf("first: %d %s", code, body)
	}
	f09ExpireRests()
	up.seen = nil
	code, body := f09SendTo(t, h, f09ReqBody("pa/m1", f09MsgUser("a"), f09MsgCall("c1"), f09MsgResult("c1")), nil)
	if code != 200 || !strings.Contains(body, "from-k2") {
		t.Fatalf("tool results: %d %s", code, body)
	}
	if strings.Join(up.seen, ",") != "k2" {
		t.Fatalf("tool results tried %v, want only k2", up.seen)
	}
	rt := f09LastRoute(t, s)
	if rt.Affinity == nil || rt.Affinity.Why != "turn" || !rt.Affinity.Kept || !rt.Affinity.Within ||
		rt.Affinity.Last != f09KeyRest("pa", "k2") || rt.Affinity.Turn != 1 {
		t.Fatalf("affinity: %+v", rt.Affinity)
	}
}

// Session affinity holds across turns too.
func TestF09SessionAffinityHoldsAcrossTurns(t *testing.T) {
	f09RouteEnv(t)
	up := &byKey{limited: map[string]bool{"k1": true}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	f09KeysProvider(t, srv.URL, "ps", "session", "", []string{"m1"}, "k1", "k2")
	s := New()
	h := s.Handler()
	f09SendTo(t, h, f09ReqBody("ps/m1", f09MsgUser("a")), nil)
	f09ExpireRests()
	up.seen = nil
	code, _ := f09SendTo(t, h, f09ReqBody("ps/m1", f09MsgUser("a"), f09MsgCall("c1"), f09MsgResult("c1"), f09MsgUser("b")), nil)
	if code != 200 || strings.Join(up.seen, ",") != "k2" {
		t.Fatalf("%d, tried %v", code, up.seen)
	}
	if rt := f09LastRoute(t, s); rt.Affinity == nil || rt.Affinity.Why != "session" || !rt.Affinity.Kept {
		t.Fatalf("affinity: %+v", rt.Affinity)
	}
}

// Affinity off: the next request is routed afresh, so the rate limited key
// is tried first again.
func TestF09AffinityOffRoutesAfresh(t *testing.T) {
	f09RouteEnv(t)
	up := &byKey{limited: map[string]bool{"k1": true}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	f09KeysProvider(t, srv.URL, "po", "off", "", []string{"m1"}, "k1", "k2")
	s := New()
	h := s.Handler()
	f09SendTo(t, h, f09ReqBody("po/m1", f09MsgUser("a")), nil)
	f09ExpireRests()
	up.seen = nil
	f09SendTo(t, h, f09ReqBody("po/m1", f09MsgUser("a"), f09MsgCall("c1"), f09MsgResult("c1")), nil)
	if strings.Join(up.seen, ",") != "k1,k2" {
		t.Fatalf("tried %v, want k1 then k2", up.seen)
	}
	if rt := f09LastRoute(t, s); rt.Affinity == nil || rt.Affinity.Why != "off" || rt.Affinity.Kept {
		t.Fatalf("affinity: %+v", rt.Affinity)
	}
}

// When the key that answered last is resting, the conversation moves on, and
// a new conversation is first routed afresh.
func TestF09AffinityGivesWayToResting(t *testing.T) {
	f09RouteEnv(t)
	up := &byKey{limited: map[string]bool{}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	f09KeysProvider(t, srv.URL, "pr", "", "", []string{"m1"}, "k1", "k2")
	s := New()
	h := s.Handler()
	if code, _ := f09SendTo(t, h, f09ReqBody("pr/m1", f09MsgUser("a")), nil); code != 200 {
		t.Fatalf("first: %d", code)
	}
	f09RestFor(f09KeyRest("pr", "k1"))
	up.seen = nil
	f09SendTo(t, h, f09ReqBody("pr/m1", f09MsgUser("a"), f09MsgCall("c1"), f09MsgResult("c1")), nil)
	if strings.Join(up.seen, ",") != "k2" {
		t.Fatalf("tried %v, want only k2", up.seen)
	}
	if rt := f09LastRoute(t, s); rt.Affinity == nil || rt.Affinity.Why != "resting" || rt.Affinity.Kept {
		t.Fatalf("affinity: %+v", rt.Affinity)
	}

	// another conversation, named by its own session id, has no memory
	up.seen = nil
	f09SendTo(t, h, f09ReqBody("pr/m1", f09MsgUser("z")), map[string]string{"x-session-id": "another"})
	if rt := f09LastRoute(t, s); rt.Affinity == nil || rt.Affinity.Why != "first" {
		t.Fatalf("affinity: %+v", rt.Affinity)
	}
}

// Rotation goes turn by turn: k1 → k2 → k3 → k1 across turns, the tool
// results of a turn stay on its key, and another model's requests in between
// don't move the order.
func TestF09RotationIsPerTurn(t *testing.T) {
	f09RouteEnv(t)
	up := &byKey{limited: map[string]bool{}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	f09KeysProvider(t, srv.URL, "rt", "", provider.Rotate, []string{"m1", "m2"}, "k1", "k2", "k3")
	h := New().Handler()
	hdr := map[string]string{"x-session-id": "rotate-me"}
	// step sends one request and checks which key answered it alone
	step := func(what, model, want string, msgs ...string) {
		t.Helper()
		up.seen = nil
		if code, _ := f09SendTo(t, h, f09ReqBody(model, msgs...), hdr); code != 200 {
			t.Fatalf("%s: %d", what, code)
		}
		if got := strings.Join(up.seen, ","); got != want {
			t.Fatalf("%s: tried %s, want %s", what, got, want)
		}
	}
	a, c1, r1 := f09MsgUser("a"), f09MsgCall("c1"), f09MsgResult("c1")
	b, c2, r2 := f09MsgUser("b"), f09MsgCall("c2"), f09MsgResult("c2")
	cc, c3 := f09MsgUser("c"), f09MsgUser("d")
	step("turn 1", "rt/m1", "k1", a)
	step("turn 1 tool results", "rt/m1", "k1", a, c1, r1)
	step("m2 in between", "rt/m2", "k1", a)
	step("turn 2", "rt/m1", "k2", a, c1, r1, b)
	step("turn 2 tool results", "rt/m1", "k2", a, c1, r1, b, c2, r2)
	step("m2 again", "rt/m2", "k2", a)
	step("turn 3", "rt/m1", "k3", a, c1, r1, b, c2, r2, cc)
	step("turn 4", "rt/m1", "k1", a, c1, r1, b, c2, r2, cc, c3)
}

// Units: why affinity keeps or lets go, as the trace says it.
func TestF09AffinityWhy(t *testing.T) {
	f09RouteEnv(t)
	a := candidate{rest: "p#a", model: "m"}
	b := candidate{rest: "p#b", model: "m"}
	cs := []candidate{a, b}
	now := time.Now()
	ans := func(c candidate, cache int, at time.Time) affAnswer {
		return affAnswer{key: candKey(c), rest: c.rest, at: at, cacheRead: cache}
	}
	fresh := now.Add(-time.Minute)
	cases := []struct {
		name     string
		mode     string
		cv       convo
		last     affAnswer
		known    bool
		lastIdx  int // the candidate the last answer is; -1 when it is not among them
		rotating bool
		lefts    map[string]left
		why      string
		kept     bool
	}{
		{"off", "off", convo{within: true}, ans(a, 0, fresh), true, 0, false, nil, "off", false},
		{"first", "auto", convo{within: true}, affAnswer{}, false, 0, false, nil, "first", false},
		{"gone", "auto", convo{within: true}, ans(candidate{rest: "p#z", model: "m"}, 0, fresh), true, -1, false, nil, "gone", false},
		{"spent", "auto", convo{within: true}, ans(a, 0, fresh), true, 0, false, map[string]left{candKey(a): {used: 99}}, "spent", false},
		{"session", "session", convo{}, ans(a, 0, fresh), true, 0, false, nil, "session", true},
		{"turn within", "turn", convo{within: true}, ans(a, 0, fresh), true, 0, false, nil, "turn", true},
		{"turn new", "turn", convo{}, ans(a, 0, fresh), true, 0, false, nil, "new-turn", false},
		{"auto within", "auto", convo{within: true}, ans(a, 0, fresh), true, 0, false, nil, "turn", true},
		{"auto rotating", "auto", convo{}, ans(a, 4096, fresh), true, 0, true, nil, "new-turn", false},
		{"auto no cache", "auto", convo{}, ans(a, 512, fresh), true, 0, false, nil, "no-cache", false},
		{"auto cold", "auto", convo{}, ans(a, 4096, now.Add(-6*time.Minute)), true, 0, false, nil, "cold", false},
		{"auto cache", "auto", convo{}, ans(a, 4096, fresh), true, 0, false, nil, "cache", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			why := affinityWhy(tc.mode, cs, tc.lastIdx, tc.known, tc.last, tc.lefts, tc.cv, tc.rotating, now)
			if why != tc.why {
				t.Fatalf("why %q, want %q", why, tc.why)
			}
			kept := why == "session" || why == "turn" || why == "cache"
			if kept != tc.kept {
				t.Fatalf("kept %v for why %q", kept, why)
			}
		})
	}
}

// Turns are the user's messages that say something; a request ending in tool
// results is within a turn.
func TestF09TurnsOfConversation(t *testing.T) {
	msgs := func(parts ...string) []Message {
		req, err := parseChat([]byte(f09ReqBody("m", parts...)))
		if err != nil {
			t.Fatal(err)
		}
		return req.Messages
	}
	if turn, within := turnsOf(msgs(f09MsgUser("a"), f09MsgCall("c1"), f09MsgResult("c1"))); turn != 1 || !within {
		t.Fatalf("tool results: turn %d within %v", turn, within)
	}
	if turn, within := turnsOf(msgs(f09MsgUser("a"), f09MsgCall("c1"), f09MsgResult("c1"), f09MsgUser("b"))); turn != 2 || within {
		t.Fatalf("new turn: turn %d within %v", turn, within)
	}
}

// A user message carrying tool results is part of the turn that asked for
// them, even when the same message also has text.
func TestF09ToolResultsWithTextStayInTurn(t *testing.T) {
	if startsTurn(Message{Role: "user", Parts: []Part{{Kind: ToolResult, Text: "ok"}, {Kind: Text, Text: "thanks"}}}) {
		t.Fatal("a user message with tool results and text opened a turn")
	}
	if startsTurn(Message{Role: "user", Parts: []Part{{Kind: ToolResult, Text: "ok"}, {Kind: Image, URL: "https://x.example/i.png"}}}) {
		t.Fatal("a user message with tool results and an image opened a turn")
	}
	msgs := []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "a"}}},
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "c1", Name: "f"}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, ID: "c1", Text: "ok"}, {Kind: Text, Text: "and then?"}}},
	}
	if turn, within := turnsOf(msgs); turn != 1 || !within {
		t.Fatalf("tool results with text: turn %d within %v, want turn 1 within", turn, within)
	}
}
