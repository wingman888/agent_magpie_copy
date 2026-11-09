package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// s3Isolate points the config and cache at fresh directories.
func s3Isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// s3Cand is a key of provider id, serving model, as a candidate.
func s3Cand(id, key, model string) candidate {
	return candidate{p: provider.Provider{ID: id, Key: key}, model: model, rest: id + "#" + key}
}

// s3Route is the last trace route the server kept.
func s3Route(s *Server) Route {
	s.trace.mu.Lock()
	defer s.trace.mu.Unlock()
	return *s.trace.routes[len(s.trace.routes)-1]
}

// s3Ask sends one Chat request of a conversation to the server.
func s3Ask(t *testing.T, s *Server, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-session-id", "s3-sess")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code >= 500 {
		t.Fatalf("request failed: %d %s", rec.Code, rec.Body.String())
	}
}

// Members' keys are ordered as the group's routing says: in order, member by
// member; a key made for another protocol comes last.
func TestS3GroupOrderAndAsides(t *testing.T) {
	a := provider.Provider{ID: "s3a", Chat: "https://a.example/v1", Key: "s3-a1"}
	b := provider.Provider{ID: "s3b", Chat: "https://b.example/v1", Anthropic: "https://b.example", Key: "s3-b1",
		Keys: []provider.KeyAccount{{Key: "s3-b2", Protocol: provider.Anthropic}}}
	const model = "s3-m"
	ids := func(cs []candidate) string {
		s := ""
		for _, c := range cs {
			s += c.p.Key + " "
		}
		return s
	}
	s := &Server{}
	g := provider.Group{ID: "s3g", Routing: provider.Ordered}

	cs, pl := s.planGroup(g, []provider.Member{{Provider: b, Model: model}, {Provider: a, Model: model}}, provider.Chat, turnCtx{})
	if got := ids(cs); got != "s3-b1 s3-a1 s3-b2 " {
		t.Fatalf("order by member: %s", got)
	}
	if pl.group == nil || pl.group.ID != "s3g" || pl.group.Routing != provider.Ordered || len(pl.group.Members) != 2 || pl.group.Members[0] != "s3b/s3-m" {
		t.Fatalf("group trace: %+v", pl.group)
	}
	if len(pl.order) != 3 || !pl.order[2].Aside || pl.order[0].Aside {
		t.Fatalf("aside trace: %+v", pl.order)
	}

	// pooled, smart routing keeps the same tiers
	g.Routing = ""
	cs, _ = s.planGroup(g, []provider.Member{{Provider: a, Model: model}, {Provider: b, Model: model}}, provider.Chat, turnCtx{})
	if got := ids(cs); got != "s3-a1 s3-b1 s3-b2 " {
		t.Fatalf("pooled: %s", got)
	}
}

// A rotating group moves on one key per user turn; the tool results of a
// turn stay on the key it began on, and another model's requests don't move it.
func TestS3RotateByTurn(t *testing.T) {
	p := provider.Provider{ID: "s3rot", Chat: "https://s3rot.example/v1", Key: "s3-k1", Routing: provider.Rotate,
		Keys: []provider.KeyAccount{{Key: "s3-k2"}, {Key: "s3-k3"}}}
	s := &Server{}
	const conv = "s3-conv-rot"
	first := func(tc turnCtx, model string) string {
		cs, pl := s.planTurn(p, model, provider.Chat, tc)
		if len(cs) == 0 {
			t.Fatal("no candidates")
		}
		rememberSticky(conv, pl.scope, cs[0], 0)
		return cs[0].p.Key
	}
	var got []string
	got = append(got, first(turnCtx{conv: conv, turn: 1}, "s3-m"))
	got = append(got, first(turnCtx{conv: conv, turn: 1, within: true}, "s3-m"))
	s.planTurn(p, "s3-other", provider.Chat, turnCtx{conv: conv, turn: 2}) // another model in between
	got = append(got, first(turnCtx{conv: conv, turn: 2}, "s3-m"))
	got = append(got, first(turnCtx{conv: conv, turn: 3}, "s3-m"))
	got = append(got, first(turnCtx{conv: conv, turn: 4}, "s3-m"))
	want := "s3-k1 s3-k1 s3-k2 s3-k3 s3-k1"
	if strings.Join(got, " ") != want {
		t.Fatalf("turns: %v, want %s", got, want)
	}
}

// The affinity reason for each case, and whether the last answerer leads.
func TestS3AffinityWhy(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{"full": {{Used: 99}}}
	}
	k1, k2 := s3Cand("s3p", "k1", "m"), s3Cand("s3p", "k2", "m")
	full := candidate{p: provider.Provider{ID: "s3p", Account: &provider.Account{Agent: "s3agent", User: "full"}}, model: "m", rest: "s3p@full"}
	list := func() ([]candidate, []Weighed) {
		cs := []candidate{k1, k2, full}
		ws := make([]Weighed, len(cs))
		for i, c := range cs {
			ws[i] = Weighed{ID: c.rest}
		}
		return cs, ws
	}
	const scope = "s3p/m"
	cases := []struct {
		name      string
		conv      string
		mode      string
		routing   string
		answerer  candidate
		cacheRead int
		age       time.Duration
		tc        turnCtx
		sitting   bool // the answerer is resting after a failure
		why       string
		kept      bool
		leads     string // the key that leads after settling, when kept
	}{
		{name: "off", conv: "s3-w-off", mode: provider.AffinityOff, answerer: k2, why: "off"},
		{name: "first", conv: "s3-w-first", mode: "", why: "first"},
		{name: "turn", conv: "s3-w-turn", mode: "", answerer: k2, tc: turnCtx{within: true}, why: "turn", kept: true, leads: "k2"},
		{name: "cache", conv: "s3-w-cache", mode: "", answerer: k2, cacheRead: 2000, age: time.Minute, why: "cache", kept: true, leads: "k2"},
		{name: "no-cache", conv: "s3-w-nocache", mode: "", answerer: k2, cacheRead: 10, age: time.Minute, why: "no-cache"},
		{name: "cold", conv: "s3-w-cold", mode: "", answerer: k2, cacheRead: 2000, age: 10 * time.Minute, why: "cold"},
		{name: "new-turn", conv: "s3-w-newturn", mode: provider.AffinityTurn, answerer: k2, cacheRead: 2000, why: "new-turn"},
		{name: "session", conv: "s3-w-session", mode: provider.AffinitySession, answerer: k2, cacheRead: 0, age: time.Hour, why: "session", kept: true, leads: "k2"},
		{name: "rotate", conv: "s3-w-rotate", mode: "", routing: provider.Rotate, answerer: k2, cacheRead: 2000, why: "new-turn"},
		{name: "resting", conv: "s3-w-resting", mode: "", answerer: k2, tc: turnCtx{within: true}, sitting: true, why: "resting"},
		{name: "gone", conv: "s3-w-gone", mode: "", answerer: s3Cand("s3p", "k9", "m"), tc: turnCtx{within: true}, why: "gone"},
		{name: "spent", conv: "s3-w-spent", mode: "", answerer: full, tc: turnCtx{within: true}, why: "spent"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if c.sitting {
				restingUntil.Lock()
				restingUntil.m[k2.rest] = time.Now().Add(time.Hour)
				restingUntil.note[k2.rest] = Rest{Why: failRate}
				restingUntil.Unlock()
				t.Cleanup(func() {
					restingUntil.Lock()
					delete(restingUntil.m, k2.rest)
					delete(restingUntil.note, k2.rest)
					restingUntil.Unlock()
				})
			}
			if c.answerer.rest != "" {
				rememberSticky(c.conv, scope, c.answerer, c.cacheRead)
				if c.age > 0 {
					sticky.Lock()
					r := sticky.last[stickyKey(c.conv, scope)]
					r.at = time.Now().Add(-c.age)
					sticky.last[stickyKey(c.conv, scope)] = r
					sticky.Unlock()
				}
			}
			tc := c.tc
			tc.conv, tc.turn, tc.scope = c.conv, 2, scope
			cs, ws := list()
			out, _, aff := settle(cs, ws, c.mode, scope, c.routing, tc)
			if aff == nil || aff.Why != c.why || aff.Kept != c.kept {
				t.Fatalf("affinity %+v, want why %s kept %v", aff, c.why, c.kept)
			}
			if c.leads != "" {
				want := map[string]candidate{"k1": k1, "k2": k2}[c.leads]
				if !sameAnswerer(out[0], stickyRec{rest: want.rest, provider: want.p.ID, model: want.model}) {
					t.Fatalf("leader %s, want %s", out[0].rest, want.rest)
				}
			}
		})
	}
	// a single candidate is never a matter of affinity
	if _, _, aff := settle([]candidate{k1}, []Weighed{{ID: k1.rest}}, "", scope, "", turnCtx{conv: "s3-w-one"}); aff != nil {
		t.Fatalf("one candidate has affinity: %+v", aff)
	}
}

// A group's requests go to the members' keys in order; the second key
// answers when the first is out, and the next turn's trace says why it stuck.
func TestS3GroupRequests(t *testing.T) {
	s3Isolate(t)
	var limited atomic.Bool
	limited.Store(true)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if limited.Load() && r.Header.Get("Authorization") == "Bearer s3-k1" {
			w.WriteHeader(429)
			w.Write([]byte(`{"error":{"message":"Too many requests"}}`))
			return
		}
		w.Write([]byte(`{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "s3p", Name: "S3", Chat: up.URL + "/v1", Key: "s3-k1",
		Keys: []provider.KeyAccount{{Key: "s3-k2"}}, Models: []string{"m1"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "s3grp", Members: []string{"s3p/m1"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	user := func(extra string) string {
		return `{"model":"group/s3grp","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"out"}` + extra + `]}`
	}
	newTurn := `,{"role":"user","content":"again"}`

	// the first key is out of rate: the second answers, and it is the group's trace
	s3Ask(t, s, user(""))
	r := s3Route(s)
	if r.Group == nil || r.Group.ID != "s3grp" || r.Group.Routing != provider.Ordered || len(r.Tries) != 2 || r.Tries[1].Status != 200 {
		t.Fatalf("first route: %+v", r)
	}
	k1 := r.Tries[0].ID

	// the rest is over; the tool result of the same turn stays on the key that answered
	restingUntil.Lock()
	delete(restingUntil.m, k1)
	restingUntil.Unlock()
	s3Ask(t, s, user(""))
	r = s3Route(s)
	if r.Affinity == nil || r.Affinity.Why != "turn" || !r.Affinity.Kept || r.Tries[0].ID != r.Tries[len(r.Tries)-1].ID || r.Tries[0].ID == k1 {
		t.Fatalf("tool result route: %+v affinity %+v", r.Tries, r.Affinity)
	}

	// a new turn with no cache read to keep: back to the order
	s3Ask(t, s, user(newTurn))
	r = s3Route(s)
	if r.Affinity == nil || r.Affinity.Why != "no-cache" || r.Affinity.Kept || r.Tries[0].ID != k1 {
		t.Fatalf("new turn route: %+v affinity %+v", r.Tries, r.Affinity)
	}

	// a group with no member ready is a 404 saying so
	if err := provider.SaveGroup(provider.Group{ID: "s3empty", Members: []string{"s3nope/m1"}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"group/s3empty","messages":[{"role":"user","content":"hi"}]}`))
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "该组没有模型就绪") {
		t.Fatalf("empty group: %d %s", rec.Code, rec.Body.String())
	}
}
