package gateway_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// keyed is a vendor that says which key each request came with, and
// answers as told for each.
type keyed struct {
	mu    sync.Mutex
	tried []string
	fail  map[string]int
}

func (k *keyed) setFail(m map[string]int) {
	k.mu.Lock()
	k.fail = m
	k.mu.Unlock()
}

func (k *keyed) log() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.tried...)
}

func (k *keyed) last() string {
	l := k.log()
	if len(l) == 0 {
		return ""
	}
	return l[len(l)-1]
}

func (k *keyed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	k.mu.Lock()
	k.tried = append(k.tried, key)
	c := k.fail[key]
	k.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if c != 0 {
		w.WriteHeader(c)
		io.WriteString(w, `{"error":{"message":"slow down"}}`)
		return
	}
	io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from `+key+`"},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":3000,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2500}}}`)
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func serveOn(t *testing.T, id, key string, models []string, v http.Handler, keys ...string) {
	t.Helper()
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: strings.ToUpper(id), Key: key, Models: models, Chat: up.URL + "/v1"}
	for _, k := range keys {
		p.Keys = append(p.Keys, provider.KeyAccount{Key: k})
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func postAs(t *testing.T, s *gateway.Server, session, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	if session != "" {
		req.Header.Set("x-session-id", session)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func lastRoute(t *testing.T, s *gateway.Server) gateway.Route {
	t.Helper()
	rs := s.Trace(context.Background(), 0, 0).Routes
	if len(rs) == 0 {
		t.Fatal("no route traced")
	}
	return rs[len(rs)-1]
}

func TestAcceptGroupRouting(t *testing.T) {
	isolate(t)
	a, b := &keyed{}, &keyed{fail: map[string]int{"kxb": 429}}
	serveOn(t, "xa", "kxa", []string{"m", "only-a"}, a)
	serveOn(t, "xb", "kxb", []string{"vendor/m"}, b)
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"xb/vendor/m", "xa/only-a"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := gateway.New()
	code, body := postAs(t, s, "", `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from kxa") || strings.Join(b.log(), ",") != "kxb" {
		t.Fatalf("group: %d %s, b tried %v", code, body, b.log())
	}
	r := lastRoute(t, s)
	if r.Group == nil || r.Group.ID != "mine" || r.Group.Name != "Mine" || len(r.Group.Members) != 2 || len(r.Order) != 2 ||
		r.Order[0].Provider != "xb" || r.Order[1].Model != "only-a" {
		t.Fatalf("trace: %+v %+v", r.Group, r.Order)
	}
	if r.Group.Members[0] != "xb/vendor/m" || r.Group.Members[1] != "xa/only-a" {
		t.Fatalf("members: %v", r.Group.Members)
	}

	// the group magpie found, asked for by its id
	b.setFail(nil)
	code, _ = postAs(t, s, "", `{"model":"group/auto-m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("auto group: %d", code)
	}
	if r := lastRoute(t, s); r.Group == nil || r.Group.ID != "auto-m" || !r.Group.Auto {
		t.Fatalf("auto trace: %+v", r.Group)
	}

	// a hidden group is not served; a plain model has no group in its trace
	if err := provider.DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if code, _ := postAs(t, s, "", `{"model":"group/auto-m","messages":[{"role":"user","content":"hi"}]}`); code != 404 {
		t.Fatalf("hidden group: %d", code)
	}
	postAs(t, s, "", `{"model":"xa/m","messages":[{"role":"user","content":"hi"}]}`)
	if r := lastRoute(t, s); r.Group != nil {
		t.Fatalf("group on a plain request: %+v", r.Group)
	}
}

func TestAcceptGroupRotateSpansMembers(t *testing.T) {
	isolate(t)
	a, b := &keyed{}, &keyed{}
	serveOn(t, "ga", "ka", []string{"m"}, a)
	serveOn(t, "gb", "kb", []string{"m"}, b)
	if err := provider.SaveGroup(provider.Group{ID: "spread", Members: []string{"ga/m", "gb/m"}, Routing: provider.Rotate}); err != nil {
		t.Fatal(err)
	}
	s := gateway.New()
	msgs := `{"role":"user","content":"hi"}`
	for i := range 4 {
		if i > 0 {
			msgs += `,{"role":"assistant","content":"yo"},{"role":"user","content":"more"}`
		}
		if code, _ := postAs(t, s, "grp-spread", `{"model":"group/spread","messages":[`+msgs+`]}`); code != 200 {
			t.Fatalf("turn %d: %d", i, code)
		}
		if len(a.log())+len(b.log()) != i+1 {
			t.Fatalf("requests: %v %v", a.log(), b.log())
		}
		r := lastRoute(t, s)
		if len(r.Order) != 2 || r.Group == nil || r.Group.Routing != provider.Rotate {
			t.Fatalf("trace: %+v", r)
		}
	}
	if len(a.log()) != 2 || len(b.log()) != 2 {
		t.Fatalf("turns spread over the members' keys: a %v, b %v", a.log(), b.log())
	}
}

func TestAcceptAffinityModes(t *testing.T) {
	const (
		first = `{"model":"%s/m","messages":[{"role":"user","content":"hi"}]}`
		again = `{"model":"%s/m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"more"}]}`
		tool  = `{"model":"%s/m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"a.go"}]}`
	)
	for i, c := range []struct {
		routing, affinity string
		tool              bool
		want, why         string
	}{
		{"", "", false, "k1", "cache"},
		{provider.Rotate, "", false, "k2", "new-turn"},
		{provider.Rotate, "", true, "k1", "turn"},
		{provider.Rotate, provider.AffinityTurn, false, "k2", "new-turn"},
		{provider.Rotate, provider.AffinityTurn, true, "k1", "turn"},
		{provider.Rotate, provider.AffinitySession, false, "k1", "session"},
		{provider.Rotate, provider.AffinityOff, false, "k2", "off"},
	} {
		isolate(t)
		id := "aff" + string(rune('a'+i))
		v := &keyed{}
		serveOn(t, id, "k1", []string{"m"}, v, "k2")
		provider.SetRouting(id, c.routing)
		provider.SetAffinity(id, c.affinity)
		s := gateway.New()
		sess := "sess-" + id
		postAs(t, s, sess, strings.ReplaceAll(first, "%s", id))
		second := again
		if c.tool {
			second = tool
		}
		postAs(t, s, sess, strings.ReplaceAll(second, "%s", id))
		r := lastRoute(t, s)
		if got := v.last(); got != c.want || r.Affinity == nil || r.Affinity.Why != c.why {
			t.Errorf("%s %q tool=%v: went to %s (%+v), want %s by %s", c.routing, c.affinity, c.tool, got, r.Affinity, c.want, c.why)
		}
		if (c.why == "cache" || c.why == "session" || c.why == "turn") != (r.Affinity != nil && r.Affinity.Kept) {
			t.Errorf("%s %q: kept = %+v", c.routing, c.affinity, r.Affinity)
		}
	}
}

func TestAcceptRotateByTurn(t *testing.T) {
	isolate(t)
	v := &keyed{}
	serveOn(t, "rot", "k1", []string{"m", "small"}, v, "k2", "k3")
	provider.SetRouting("rot", provider.Rotate)
	s := gateway.New()
	var turns []string
	msgs := `{"role":"user","content":"hi"}`
	for i := range 4 {
		if i > 0 {
			msgs += `,{"role":"assistant","content":"yo"},{"role":"user","content":"more"}`
		}
		postAs(t, s, "rot-s", `{"model":"rot/m","messages":[`+msgs+`]}`)
		turns = append(turns, v.last())
		postAs(t, s, "rot-s", `{"model":"rot/small","messages":[{"role":"user","content":"title?"}]}`)
		postAs(t, s, "rot-s", `{"model":"rot/m","messages":[`+msgs+`,{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"a.go"}]}`)
		if got := v.last(); got != turns[i] {
			t.Fatalf("turn %d's tool result went to %s, not %s", i, got, turns[i])
		}
	}
	if strings.Join(turns, ",") != "k1,k2,k3,k1" {
		t.Fatalf("turns went %v", turns)
	}
}

func TestAcceptSessionAffinityResting(t *testing.T) {
	isolate(t)
	const (
		first = `{"model":"rest/m","messages":[{"role":"user","content":"hi"}]}`
		again = `{"model":"rest/m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"more"}]}`
	)
	v := &keyed{}
	serveOn(t, "rest", "k1", []string{"m"}, v, "k2")
	provider.SetAffinity("rest", provider.AffinitySession)
	s := gateway.New()
	postAs(t, s, "rs1", first)
	if v.last() != "k1" {
		t.Fatalf("first went to %v", v.log())
	}
	// another conversation finds k1 failing, so it rests
	v.setFail(map[string]int{"k1": 429})
	if code, _ := postAs(t, s, "rs-other", first); code != 200 {
		t.Fatalf("other: %d", code)
	}
	v.setFail(nil)
	postAs(t, s, "rs1", again)
	r := lastRoute(t, s)
	if v.last() != "k2" || r.Affinity == nil || r.Affinity.Why != "resting" || r.Affinity.Kept {
		t.Fatalf("resting: %v %+v", v.log(), r.Affinity)
	}
	postAs(t, s, "rs-new", first)
	if r := lastRoute(t, s); r.Affinity == nil || r.Affinity.Why != "first" {
		t.Fatalf("new session: %+v", r.Affinity)
	}
}

func TestAcceptKeyPoolsByProtocol(t *testing.T) {
	isolate(t)
	v := &keyed{}
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "mixpool", Name: "MIX", Key: "k1", KeyProtocol: provider.Chat, Models: []string{"m"}, Chat: up.URL + "/v1",
		Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3", Protocol: provider.Chat}}, Routing: provider.Rotate}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := gateway.New()
	for range 4 {
		postAs(t, s, "", `{"model":"mixpool/m","messages":[{"role":"user","content":"hi"}]}`)
	}
	if got := strings.Join(v.log(), ","); got != "k1,k3,k1,k3" {
		t.Fatalf("in turn went %s", got)
	}
	r := lastRoute(t, s)
	if last := r.Order[len(r.Order)-1]; !last.Aside || last.Who == "" || r.Order[0].Aside {
		t.Fatalf("trace: %+v", r.Order)
	}
	v.setFail(map[string]int{"k1": 429, "k3": 429})
	code, body := postAs(t, s, "", `{"model":"mixpool/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from k2") {
		t.Fatalf("aside key: %d %s, tried %v", code, body, v.log())
	}
}
