package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyLog is an upstream answering every Chat request with chatOK, and keeping
// the Authorization header of each, in order.
type affKeyLog struct {
	mu   sync.Mutex
	auth []string
}

func (l *affKeyLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	l.mu.Lock()
	l.auth = append(l.auth, r.Header.Get("Authorization"))
	l.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, affChatOK)
}

// take is the key, of those given, that each request since the last take was
// sent with, in order ("" for none of them).
func (l *affKeyLog) take(keys ...string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, a := range l.auth {
		name := ""
		for _, k := range keys {
			if strings.Contains(a, k) {
				name = strings.TrimPrefix(k, "sk-")
			}
		}
		out = append(out, name)
	}
	l.auth = nil
	return out
}

var affAffinityKeys = []string{"sk-k1", "sk-k2", "sk-k3", "sk-o"}

// affinityServer saves the providers, each answering from one upstream that
// keeps the keys it was sent, and returns that upstream and a handler.
func affAffinityServer(t *testing.T, ps ...provider.Provider) (*affKeyLog, *Server, http.Handler) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := &affKeyLog{}
	ts := httptest.NewServer(up)
	t.Cleanup(ts.Close)
	for _, p := range ps {
		p.Chat = ts.URL + "/v1"
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	return up, s, s.Handler()
}

// twoKeys is a provider on two keys, serving model m.
func affTwoKeys(id, affinity string) provider.Provider {
	return provider.Provider{ID: id, Name: id, Key: "sk-k1", Keys: []provider.KeyAccount{{Key: "sk-k2"}},
		Models: []string{"m"}, Affinity: affinity}
}

// setRest makes a candidate rest after a failure for d, and clearRest lets it go.
func affSetRest(id string, d time.Duration) {
	restingUntil.Lock()
	restingUntil.m[id] = time.Now().Add(d)
	restingUntil.note[id] = Rest{Why: failRate, Status: 429, By: "test"}
	restingUntil.Unlock()
}

func affClearRest(id string) {
	restingUntil.Lock()
	delete(restingUntil.m, id)
	delete(restingUntil.note, id)
	restingUntil.Unlock()
}

// ask sends a Chat request for model in session (none when ""), and returns
// its trace and the key it was answered with.
func affAsk(t *testing.T, up *affKeyLog, s *Server, h http.Handler, model, session, messages string) (Route, string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"`+model+`","messages":`+messages+`}`))
	if session != "" {
		req.Header.Set("x-session-id", session)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d: %s", model, rec.Code, rec.Body.String())
	}
	var keys []string
	for _, k := range up.take(affAffinityKeys...) {
		if k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) != 1 {
		t.Fatalf("%s answered by %v, want one key", model, keys)
	}
	return affLastRoute(t, s), keys[0]
}

// why is the affinity a route was kept for, or "" with no affinity at all.
func affWhy(r Route) string {
	if r.Affinity == nil {
		return ""
	}
	return r.Affinity.Why
}

const (
	affMsgFirst = `[{"role":"user","content":"first"}]`
	affMsgNext  = `[{"role":"user","content":"first"},{"role":"assistant","content":"ok"},{"role":"user","content":"next"}]`
	affMsgTool  = `[{"role":"user","content":"next"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"done"}]`
)

func TestAffSessionOfReadsHeadersInOrder(t *testing.T) {
	for _, c := range []struct {
		set  [][2]string
		want string
	}{
		{nil, ""},
		{[][2]string{{"x-claude-code-session-id", "f"}}, "f"},
		{[][2]string{{"session-id", "e"}, {"x-claude-code-session-id", "f"}}, "e"},
		{[][2]string{{"session_id", "d"}, {"session-id", "e"}}, "d"},
		{[][2]string{{"x-session-id", "c"}, {"session_id", "d"}}, "c"},
		{[][2]string{{"x-session-affinity", "b"}, {"x-session-id", "c"}}, "b"},
		{[][2]string{{"x-opencode-session", "a"}, {"x-session-affinity", "b"}}, "a"},
		{[][2]string{{"x-opencode-session", "  "}, {"x-session-affinity", "b"}}, "b"},
	} {
		h := http.Header{}
		for _, kv := range c.set {
			h.Set(kv[0], kv[1])
		}
		if got := sessionOf(h); got != c.want {
			t.Errorf("sessionOf(%v) = %q, want %q", c.set, got, c.want)
		}
	}
}

func TestAffTurnsOf(t *testing.T) {
	anthropic := `{"model":"m","messages":[{"role":"user","content":"a"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`
	for _, c := range []struct {
		from   provider.Protocol
		body   string
		turns  int
		within bool
	}{
		{provider.Chat, `{"model":"m","messages":` + affMsgFirst + `}`, 1, false},
		{provider.Chat, `{"model":"m","messages":` + affMsgNext + `}`, 2, false},
		{provider.Chat, `{"model":"m","messages":` + affMsgTool + `}`, 1, true},
		{provider.Anthropic, anthropic, 1, true},
	} {
		turns, within := turnsOf(c.from, []byte(c.body))
		if turns != c.turns || within != c.within {
			t.Errorf("turnsOf(%s) = %d, %v; want %d, %v", c.body, turns, within, c.turns, c.within)
		}
	}
}

// TestKeepTo is the decision alone: the candidates, their order, and why.
func TestAffKeepTo(t *testing.T) {
	now := time.Now()
	cs := []candidate{{rest: "p#a", model: "m"}, {rest: "p#b", model: "m"}, {rest: "p#c", model: "m"}}
	order := func() planned {
		var pl planned
		for _, c := range cs {
			pl.order = append(pl.order, Weighed{ID: c.rest, Model: c.model})
		}
		return pl
	}
	ids := func(cs []candidate) string {
		s := ""
		for _, c := range cs {
			s += c.rest + " "
		}
		return s
	}
	memory := func(rest string, cacheRead int, at time.Time) *convo {
		cv := &convo{turn: map[string]int{}}
		if rest != "" {
			cv.last = &answer{rest: rest, model: "m", at: at, cacheRead: cacheRead}
		}
		return cv
	}
	recent := now.Add(-time.Minute)
	const plain, moved = "p#a p#b p#c ", "p#b p#a p#c " // the order, unkept and kept
	for _, c := range []struct {
		name     string
		mode     string
		rotating bool
		cv       *convo
		within   bool
		why      string
		kept     bool
	}{
		{"no memory", "", false, memory("", 0, now), false, "first", false},
		{"off", provider.AffinityOff, false, memory("p#b", 2000, recent), true, "off", false},
		{"auto within keeps", "", false, memory("p#b", 0, recent), true, "turn", true},
		{"auto cache keeps", "", false, memory("p#b", 2000, recent), false, "cache", true},
		{"auto cache under 1024", "", false, memory("p#b", 1023, recent), false, "no-cache", false},
		{"auto cold", "", false, memory("p#b", 2000, now.Add(-6*time.Minute)), false, "cold", false},
		{"turn within keeps", provider.AffinityTurn, false, memory("p#b", 2000, recent), true, "turn", true},
		{"turn new turn", provider.AffinityTurn, false, memory("p#b", 2000, recent), false, "new-turn", false},
		{"session new turn", provider.AffinitySession, false, memory("p#b", 0, recent), false, "session", true},
		{"session cold still keeps", provider.AffinitySession, false, memory("p#b", 0, now.Add(-time.Hour)), false, "session", true},
		{"rotating new turn moves on", "", true, memory("p#b", 2000, recent), false, "new-turn", false},
		{"rotating session keeps", provider.AffinitySession, true, memory("p#b", 0, recent), false, "session", true},
		{"last gone", provider.AffinitySession, false, memory("p#z", 0, recent), false, "gone", false},
	} {
		got, pl, aff := keepTo(cs, order(), c.mode, c.rotating, c.cv, 2, c.within, now)
		if aff == nil || aff.Why != c.why || aff.Kept != c.kept {
			t.Errorf("%s: %+v, want why %s kept %v", c.name, aff, c.why, c.kept)
			continue
		}
		want := plain
		if c.kept {
			want = moved
		}
		if ids(got) != want {
			t.Errorf("%s: order %q, want %q", c.name, ids(got), want)
		}
		if len(pl.order) != len(got) || pl.order[0].ID != got[0].rest {
			t.Errorf("%s: trace not aligned with the candidates", c.name)
		}
		if aff.Mode == "" {
			t.Errorf("%s: mode not written", c.name)
		}
	}
}

// TestKeepToRestingAndSpent: a last answerer that is resting, or whose account
// is all but used up, is not kept; the order stays as routing put it.
func TestAffKeepToRestingAndSpent(t *testing.T) {
	now := time.Now()
	cs := []candidate{{rest: "p#a", model: "m"}, {rest: "p#b", model: "m"}}
	var pl planned
	for _, c := range cs {
		pl.order = append(pl.order, Weighed{ID: c.rest, Model: c.model})
	}
	cv := &convo{turn: map[string]int{}, last: &answer{rest: "p#b", model: "m", at: now, cacheRead: 0}}
	affSetRest("p#b", time.Hour)
	defer affClearRest("p#b")
	if _, _, aff := keepTo(cs, pl, provider.AffinitySession, false, cv, 1, true, now); aff.Why != "resting" || aff.Kept {
		t.Fatalf("resting: %+v", aff)
	}
	affClearRest("p#b")

	old := allowances
	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{"u": {{Used: 99}}}
	}
	defer func() { allowances = old }()
	cs[1].p = provider.Provider{ID: "p", Account: &provider.Account{Agent: "codex", User: "u"}}
	if _, _, aff := keepTo(cs, pl, provider.AffinitySession, false, cv, 1, true, now); aff.Why != "spent" || aff.Kept {
		t.Fatalf("spent: %+v", aff)
	}
}

// TestAffinityAuto: inside a turn the last answerer keeps the request, whatever
// routing would have put first; across turns it keeps only with a warm cache.
func TestAffAffinityAuto(t *testing.T) {
	p := affTwoKeys("aff-auto", "")
	up, s, h := affAffinityServer(t, p)
	k1 := p.ID + "#" + provider.KeyID("sk-k1")
	affSetRest(k1, time.Hour)
	defer affClearRest(k1)

	r, key := affAsk(t, up, s, h, "m", "auto-1", affMsgFirst)
	if key != "k2" || affWhy(r) != "first" {
		t.Fatalf("first: key %s, why %q", key, affWhy(r))
	}
	affClearRest(k1) // k1 is first again, but the conversation's last answerer is k2
	r, key = affAsk(t, up, s, h, "m", "auto-1", affMsgTool)
	if key != "k2" || affWhy(r) != "turn" || !r.Affinity.Kept || r.Affinity.Last != p.ID+"#"+provider.KeyID("sk-k2") {
		t.Fatalf("within turn: key %s, %+v", key, r.Affinity)
	}
	// a new turn with no cache read to keep: the routing goes back to k1
	r, key = affAsk(t, up, s, h, "m", "auto-1", affMsgNext)
	if key != "k1" || affWhy(r) != "no-cache" {
		t.Fatalf("new turn, no cache: key %s, why %q", key, affWhy(r))
	}
}

// TestAffinitySession: the whole session keeps to its last answerer.
func TestAffAffinitySession(t *testing.T) {
	p := affTwoKeys("aff-session", provider.AffinitySession)
	up, s, h := affAffinityServer(t, p)
	k1 := p.ID + "#" + provider.KeyID("sk-k1")
	affSetRest(k1, time.Hour)
	defer affClearRest(k1)
	if _, key := affAsk(t, up, s, h, "m", "sess-1", affMsgFirst); key != "k2" {
		t.Fatalf("first: %s", key)
	}
	affClearRest(k1)
	for _, msgs := range []string{affMsgNext, affMsgTool} {
		r, key := affAsk(t, up, s, h, "m", "sess-1", msgs)
		if key != "k2" || affWhy(r) != "session" {
			t.Fatalf("session: key %s, why %q", key, affWhy(r))
		}
	}
}

// TestAffinityOff: nothing is kept.
func TestAffAffinityOff(t *testing.T) {
	p := affTwoKeys("aff-off", provider.AffinityOff)
	up, s, h := affAffinityServer(t, p)
	k1 := p.ID + "#" + provider.KeyID("sk-k1")
	affSetRest(k1, time.Hour)
	defer affClearRest(k1)
	affAsk(t, up, s, h, "m", "off-1", affMsgFirst)
	affClearRest(k1)
	r, key := affAsk(t, up, s, h, "m", "off-1", affMsgTool)
	if key != "k1" || affWhy(r) != "off" || r.Affinity.Kept {
		t.Fatalf("off: key %s, %+v", key, r.Affinity)
	}
}

// TestAffinityTurn: a turn keeps its answerer within it, and a new one may move.
func TestAffAffinityTurn(t *testing.T) {
	p := affTwoKeys("aff-turn", provider.AffinityTurn)
	up, s, h := affAffinityServer(t, p)
	k1 := p.ID + "#" + provider.KeyID("sk-k1")
	affSetRest(k1, time.Hour)
	defer affClearRest(k1)
	affAsk(t, up, s, h, "m", "turn-1", affMsgFirst)
	affClearRest(k1)
	if r, key := affAsk(t, up, s, h, "m", "turn-1", affMsgTool); key != "k2" || affWhy(r) != "turn" {
		t.Fatalf("within: key %s, why %q", key, affWhy(r))
	}
	if r, key := affAsk(t, up, s, h, "m", "turn-1", affMsgNext); key != "k1" || affWhy(r) != "new-turn" {
		t.Fatalf("new turn: key %s, why %q", key, affWhy(r))
	}
}

// TestAffinityRestingAndFirst: an answerer that is resting is not kept, and a
// session with no record is first; without a session there is no affinity.
func TestAffAffinityRestingAndFirst(t *testing.T) {
	p := affTwoKeys("aff-rest", "")
	up, s, h := affAffinityServer(t, p)
	k1 := p.ID + "#" + provider.KeyID("sk-k1")
	k2 := p.ID + "#" + provider.KeyID("sk-k2")
	affSetRest(k1, time.Hour)
	defer affClearRest(k1)
	defer affClearRest(k2)
	affAsk(t, up, s, h, "m", "rest-1", affMsgFirst)
	affClearRest(k1)
	affSetRest(k2, time.Hour)
	r, key := affAsk(t, up, s, h, "m", "rest-1", affMsgTool)
	if key != "k1" || affWhy(r) != "resting" || r.Affinity.Last != k2 {
		t.Fatalf("resting: key %s, %+v", key, r.Affinity)
	}
	affClearRest(k2)
	if r, _ := affAsk(t, up, s, h, "m", "rest-2", affMsgTool); affWhy(r) != "first" {
		t.Fatalf("another session: why %q", affWhy(r))
	}
	if r, _ := affAsk(t, up, s, h, "m", "", affMsgTool); r.Affinity != nil {
		t.Fatalf("no session: affinity %+v", r.Affinity)
	}
}

// TestRotateByTurn: a rotation moves a key on at each new turn, the tool
// results within a turn stay on its key, and another model's requests in the
// same session don't move it.
func TestAffRotateByTurn(t *testing.T) {
	m := provider.Provider{ID: "aff-rot", Name: "R", Key: "sk-k1", Keys: []provider.KeyAccount{{Key: "sk-k2"}, {Key: "sk-k3"}},
		Models: []string{"m"}, Routing: provider.Rotate}
	o := provider.Provider{ID: "aff-rot-o", Name: "O", Key: "sk-o", Models: []string{"other-m"}}
	up, s, h := affAffinityServer(t, m, o)

	var got []string
	turn := func(msgs string) {
		_, key := affAsk(t, up, s, h, "m", "rot-1", msgs)
		got = append(got, key)
	}
	turn(affMsgFirst)                                    // turn 1: k1
	turn(affMsgTool)                                     // within: still k1
	affAsk(t, up, s, h, "other-m", "rot-1", affMsgFirst) // another model in between
	turn(affMsgNext)                                     // turn 2: k2
	turn(affMsgTool)                                     // within: k2
	turn(affMsgNext)                                     // turn 3: k3
	affAsk(t, up, s, h, "other-m", "rot-1", affMsgTool)
	turn(affMsgTool) // within: k3
	turn(affMsgNext) // turn 4: k1
	want := "k1 k1 k2 k2 k3 k3 k1 "
	if strings.Join(got, " ")+" " != want {
		t.Fatalf("by turn: %v, want %s", got, want)
	}

	// with no session every request is a turn of its own: plain rotation
	var plain []string
	for range 4 {
		_, key := affAsk(t, up, s, h, "m", "", affMsgFirst)
		plain = append(plain, key)
	}
	if strings.Join(plain, " ") != "k1 k2 k3 k1" {
		t.Fatalf("no session: %v", plain)
	}
}

const affChatOK = `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// lastRoute is the newest route the server traced.
func affLastRoute(t *testing.T, s *Server) Route {
	t.Helper()
	st := s.Trace(context.Background(), 0, 0)
	if len(st.Routes) == 0 {
		t.Fatal("no route traced")
	}
	return st.Routes[len(st.Routes)-1]
}
