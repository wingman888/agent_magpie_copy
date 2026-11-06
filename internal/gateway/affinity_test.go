package gateway

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// two keys of one provider, the first answered last; the conversation is
// "conv-<name>" as asked of model m.
func affCands(name string) (cs []candidate, rc routeCtx) {
	p := provider.Provider{ID: "x"}
	cs = []candidate{
		{p: p.WithKey(provider.KeyAccount{Key: "one"}), model: "m", rest: "x#1"},
		{p: p.WithKey(provider.KeyAccount{Key: "two"}), model: "m", rest: "x#2"},
	}
	rc = routeCtx{key: "conv-" + name + "\x00m", model: "m"}
	return cs, rc
}

// answered records that candidate c answered the conversation at at, having read cache tokens.
func answered(rc routeCtx, c candidate, cache int, at time.Time) {
	remember(rc.key, c, cache, at)
}

// The reasons affinity keeps a conversation, or not, as the trace names them.
func TestAffinityWhy(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		mode   string
		within bool
		cache  int
		age    time.Duration
		setup  func(cs []candidate, rc routeCtx)
		why    string
		kept   bool
	}{
		{name: "off", mode: provider.AffinityOff, why: "off"},
		{name: "first", mode: provider.AffinityAuto, why: "first"},
		{name: "session", mode: provider.AffinitySession, why: "session", kept: true},
		{name: "turn within", mode: provider.AffinityTurn, within: true, why: "turn", kept: true},
		{name: "turn new", mode: provider.AffinityTurn, why: "new-turn"},
		{name: "auto within", mode: provider.AffinityAuto, within: true, why: "turn", kept: true},
		{name: "auto no cache", mode: provider.AffinityAuto, cache: 10, why: "no-cache"},
		{name: "auto cold", mode: provider.AffinityAuto, cache: 2000, age: 6 * time.Minute, why: "cold"},
		{name: "auto cache", mode: provider.AffinityAuto, cache: 1024, age: time.Minute, why: "cache", kept: true},
		{name: "gone", mode: provider.AffinitySession, why: "gone",
			setup: func(cs []candidate, rc routeCtx) { remember(rc.key, candidate{rest: "x#9", model: "m"}, 0, now) }},
		{name: "resting", mode: provider.AffinitySession, why: "resting",
			setup: func(cs []candidate, rc routeCtx) {
				// the one remembered as answering is the second
				restingUntil.Lock()
				restingUntil.m[cs[1].rest] = now.Add(time.Hour)
				restingUntil.note[cs[1].rest] = Rest{Why: failRate}
				restingUntil.Unlock()
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restingUntil.Lock()
			restingUntil.m = map[string]time.Time{}
			restingUntil.note = map[string]Rest{}
			restingUntil.Unlock()
			cs, rc := affCands("why-" + tc.name)
			rc.within = tc.within
			if tc.mode != provider.AffinityOff && tc.why != "first" {
				answered(rc, cs[1], tc.cache, now.Add(-tc.age))
			}
			if tc.setup != nil {
				tc.setup(cs, rc)
			}
			out, _, af := keepAffine(cs, planned{}, tc.mode, rc, now)
			if af == nil || af.Why != tc.why || af.Kept != tc.kept {
				t.Fatalf("affinity %+v", af)
			}
			if tc.kept && out[0].rest != "x#2" {
				t.Fatalf("kept but the last answerer isn't first: %q", restsOf(out))
			}
			if !tc.kept && out[0].rest != "x#1" {
				t.Fatalf("moved though not kept: %q", restsOf(out))
			}
		})
	}
}

// With one candidate affinity doesn't come into it, and the trace has none.
func TestAffinityNotForOneCandidate(t *testing.T) {
	cs, rc := affCands("one")
	cs = cs[:1]
	if _, _, af := keepAffine(cs, planned{}, provider.AffinitySession, rc, time.Now()); af != nil {
		t.Fatalf("affinity for one candidate: %+v", af)
	}
}

// A subscription about to run out of its allowance is not kept.
func TestAffinitySpentIsNotKept(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{"u": {{Used: 99}}}
	}
	acc := &provider.Account{Agent: "claude", User: "u"}
	cs := []candidate{
		{p: provider.Provider{ID: "x", Account: acc}, model: "m", rest: "x"},
		{p: provider.Provider{ID: "y"}, model: "m", rest: "y"},
	}
	rc := routeCtx{key: "conv-spent\x00m", model: "m"}
	answered(rc, cs[0], 0, time.Now())
	if _, _, af := keepAffine(cs, planned{}, provider.AffinitySession, rc, time.Now()); af == nil || af.Why != "spent" || af.Kept {
		t.Fatalf("affinity %+v", af)
	}
}

// Memory is per requested model, and forgotten after a day unasked.
func TestAffinityScopeAndDay(t *testing.T) {
	cs, rc := affCands("scope")
	answered(rc, cs[1], 0, time.Now())
	other := rc
	other.key = "conv-scope\x00other"
	if _, _, af := keepAffine(cs, planned{}, provider.AffinitySession, other, time.Now()); af == nil || af.Why != "first" {
		t.Fatalf("another model's conversation: %+v", af)
	}
	affinities.Lock()
	affinities.m[rc.key].seen = time.Now().Add(-25 * time.Hour)
	pruneLocked(time.Now())
	affinities.Unlock()
	if _, ok := recall(rc.key); ok {
		t.Fatal("remembered after a day")
	}
}

// Turns: a user message that says something is a turn; one that only sends
// tool results back is within the turn it belongs to.
func TestTurnsOf(t *testing.T) {
	text := func(s string) Message { return Message{Role: "user", Parts: []Part{{Kind: Text, Text: s}}} }
	result := Message{Role: "user", Parts: []Part{{Kind: ToolResult, Text: "ok"}}}
	reply := Message{Role: "assistant", Parts: []Part{{Kind: Text, Text: "ok"}}}
	for _, tc := range []struct {
		name   string
		ms     []Message
		turns  int
		within bool
	}{
		{"one", []Message{text("hi")}, 1, false},
		{"within", []Message{text("hi"), reply, result}, 1, true},
		{"new turn", []Message{text("hi"), reply, result, reply, text("again")}, 2, false},
		{"none", nil, 0, false},
	} {
		if turns, within := turnsOf(tc.ms); turns != tc.turns || within != tc.within {
			t.Fatalf("%s: turns %d within %v", tc.name, turns, within)
		}
	}
}

// The session id is the first non-empty of the headers agents send, in order.
func TestSessionHeaderOrder(t *testing.T) {
	for i := range sessionHeaders {
		h := http.Header{}
		for _, name := range sessionHeaders[i:] {
			h.Set(name, "v-"+name)
		}
		if got := conversationID(h, []byte(`{}`)); got != "v-"+sessionHeaders[i] {
			t.Fatalf("from %s on: %q", sessionHeaders[i], got)
		}
	}
	h := http.Header{}
	h.Set("x-opencode-session", "  ")
	h.Set("x-session-affinity", " aff ")
	if got := conversationID(h, []byte(`{}`)); got != "aff" {
		t.Fatalf("blank header not skipped: %q", got)
	}
}

// The trace names the group and the affinity in the fields agents read.
func TestTraceFieldNames(t *testing.T) {
	r := Route{Group: &GroupTrace{ID: "mine", Name: "Mine", Routing: "order", Affinity: "", Auto: true, Members: []string{"b/m"}},
		Affinity: &AffinityTrace{Mode: "auto", Turn: 2, Within: true, Last: "x#1", CacheRead: 5, Kept: true, Why: "turn"},
		Order:    []Weighed{{ID: "x#1", Aside: true}}}
	b, _ := json.Marshal(r)
	var m map[string]any
	json.Unmarshal(b, &m)
	g, _ := m["group"].(map[string]any)
	a, _ := m["affinity"].(map[string]any)
	for _, k := range []string{"id", "name", "routing", "affinity", "auto", "members"} {
		if _, ok := g[k]; !ok {
			t.Fatalf("group has no %s: %s", k, b)
		}
	}
	for _, k := range []string{"mode", "turn", "within", "last", "cacheRead", "kept", "why"} {
		if _, ok := a[k]; !ok {
			t.Fatalf("affinity has no %s: %s", k, b)
		}
	}
	if o := m["order"].([]any)[0].(map[string]any); o["aside"] != true {
		t.Fatalf("candidate has no aside: %s", b)
	}
}
