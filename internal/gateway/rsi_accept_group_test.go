package gateway_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// keyed is a vendor that says which key each request came with.
type keyed struct {
	mu    sync.Mutex
	tried []string
	fail  map[string]int
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

func (k *keyed) last() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.tried) == 0 {
		return ""
	}
	return k.tried[len(k.tried)-1]
}

func (k *keyed) setFail(m map[string]int) {
	k.mu.Lock()
	k.fail = m
	k.mu.Unlock()
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
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
		t.Fatalf("save %s: %q", id, err.Error())
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

// lastRoute is the newest route of the trace, as generic JSON.
func lastRoute(t *testing.T, s *gateway.Server) map[string]any {
	t.Helper()
	b, err := json.Marshal(s.Trace(t.Context(), 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Routes []map[string]any `json:"routes"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Routes) == 0 {
		t.Fatal("no routes traced")
	}
	return st.Routes[len(st.Routes)-1]
}

func sub(m map[string]any, k string) map[string]any {
	v, _ := m[k].(map[string]any)
	return v
}

func order(r map[string]any) []map[string]any {
	var out []map[string]any
	l, _ := r["order"].([]any)
	for _, x := range l {
		out = append(out, x.(map[string]any))
	}
	return out
}

func why(r map[string]any) string {
	a := sub(r, "affinity")
	s, _ := a["why"].(string)
	return s
}

func catalogIDs() []string {
	var ids []string
	for _, e := range provider.Catalog() {
		if e.Provider.Account == nil {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

func TestAcceptAutoGroupInCatalog(t *testing.T) {
	isolate(t)
	serveOn(t, "a", "ka", []string{"m", "only-a"}, &keyed{})
	serveOn(t, "b", "kb", []string{"vendor/m"}, &keyed{})
	if got := strings.Join(catalogIDs(), " "); got != "a/m a/only-a b/vendor/m group/auto-m" {
		t.Fatalf("catalog: %s", got)
	}
	g, ms, ok := provider.FindGroup("group/auto-m")
	if !ok || len(ms) != 2 || ms[0].Provider.ID != "a" || ms[1].Provider.ID != "b" {
		t.Fatalf("find: %+v %+v %v", g, ms, ok)
	}
	if p, _, ok := provider.Resolve("group/auto-m"); !ok || p.ID != "a" {
		t.Fatalf("resolve: %v %s", ok, p.ID)
	}
	// different names never group
	serveOn(t, "c", "kc", []string{"other"}, &keyed{})
	for _, id := range catalogIDs() {
		if strings.HasPrefix(id, "group/") && id != "group/auto-m" {
			t.Fatalf("unexpected group %s", id)
		}
	}
}

func TestAcceptUserGroupOrdered(t *testing.T) {
	isolate(t)
	a, b := &keyed{}, &keyed{fail: map[string]int{"kb": 429}}
	serveOn(t, "a", "ka", []string{"m", "only-a"}, a)
	serveOn(t, "b", "kb", []string{"vendor/m"}, b)
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"b/vendor/m", "a/only-a"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	s := gateway.New()
	code, body := postAs(t, s, "", `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from ka") || strings.Join(b.tried, ",") != "kb" {
		t.Fatalf("group: %d %s, b tried %v", code, body, b.tried)
	}
	r := lastRoute(t, s)
	g := sub(r, "group")
	if g == nil || g["id"] != "mine" || g["name"] != "Mine" || g["routing"] != "order" {
		t.Fatalf("group trace: %+v", g)
	}
	if ms, _ := g["members"].([]any); len(ms) != 2 || ms[0] != "b/vendor/m" || ms[1] != "a/only-a" {
		t.Fatalf("members: %+v", g["members"])
	}
	o := order(r)
	if len(o) != 2 || o[0]["provider"] != "b" || o[1]["provider"] != "a" || o[1]["model"] != "only-a" {
		t.Fatalf("order: %+v", o)
	}
	// the group is first in the catalog's group section, after providers
	ids := catalogIDs()
	if ids[len(ids)-1] != "group/mine" && ids[len(ids)-2] != "group/mine" {
		t.Fatalf("catalog: %v", ids)
	}
}

func TestAcceptDeleteShowAutoGroup(t *testing.T) {
	isolate(t)
	serveOn(t, "a", "ka", []string{"m"}, &keyed{})
	serveOn(t, "b", "kb", []string{"vendor/m"}, &keyed{})
	s := gateway.New()
	req := `{"model":"group/auto-m","messages":[{"role":"user","content":"hi"}]}`
	if code, body := postAs(t, s, "", req); code != 200 {
		t.Fatalf("auto group: %d %s", code, body)
	}
	if err := provider.DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := provider.FindGroup("group/auto-m"); ok {
		t.Fatal("hidden group still found")
	}
	if code, _ := postAs(t, s, "", req); code == 200 {
		t.Fatal("hidden group still served")
	}
	if err := provider.ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if code, body := postAs(t, s, "", req); code != 200 {
		t.Fatalf("shown group: %d %s", code, body)
	}
	if err := provider.DeleteGroup("does-not-exist"); err == nil {
		t.Fatal("deleting a missing group should fail")
	}
	// a user's own group is removed outright
	provider.SaveGroup(provider.Group{ID: "mine", Members: []string{"a/m"}})
	if err := provider.DeleteGroup("mine"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := provider.FindGroup("group/mine"); ok {
		t.Fatal("deleted group found")
	}
}

func TestAcceptGroupSaveRules(t *testing.T) {
	isolate(t)
	serveOn(t, "a", "ka", []string{"m", "x"}, &keyed{})
	if err := provider.SaveGroup(provider.Group{Name: "Empty"}); err == nil {
		t.Fatal("empty group saved")
	}
	if err := provider.Save(provider.Provider{ID: "group", Name: "G", Key: "k", Models: []string{"m"}, Chat: "http://127.0.0.1:1/v1"}); err == nil {
		t.Fatal("provider id group accepted")
	}
	// id from the name, name from the id, bad values fall back
	if err := provider.SaveGroup(provider.Group{Name: "My Fine Group", Members: []string{"a/m"}, Routing: "bogus", Affinity: "bogus"}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := provider.FindGroup("group/my-fine-group")
	if !ok || g.Name != "My Fine Group" || g.Routing != "" || g.Affinity != "" || len(ms) != 1 {
		t.Fatalf("group: %+v %v %v", g, ms, ok)
	}
	if err := provider.SaveGroup(provider.Group{ID: "bare", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if g, _, ok := provider.FindGroup("group/bare"); !ok || g.Name != "bare" {
		t.Fatalf("name default: %+v", g)
	}
	// a group inside a group is ignored; duplicates count once
	if err := provider.SaveGroup(provider.Group{ID: "outer", Members: []string{"group/bare", "a/m", "a/m", "a/x"}}); err != nil {
		t.Fatal(err)
	}
	_, ms, ok = provider.FindGroup("group/outer")
	if !ok || len(ms) != 2 {
		t.Fatalf("members: %+v", ms)
	}
	// a group with no ready member is a 404
	if err := provider.SaveGroup(provider.Group{ID: "ghost", Members: []string{"nobody/zip"}}); err != nil {
		t.Fatal(err)
	}
	s := gateway.New()
	if code, body := postAs(t, s, "", `{"model":"group/ghost","messages":[{"role":"user","content":"hi"}]}`); code != 404 {
		t.Fatalf("ghost: %d %s", code, body)
	}
}

func TestAcceptAffinityModes(t *testing.T) {
	const (
		first = `{"model":"aff/m","messages":[{"role":"user","content":"hi"}]}`
		again = `{"model":"aff/m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"more"}]}`
		tool  = `{"model":"aff/m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"a.go"}]}`
	)
	for i, c := range []struct {
		routing, affinity, body, want, why string
	}{
		{"", "", again, "k1", "cache"},
		{"", "", tool, "k1", "turn"},
		{"rotate", "", again, "k2", "new-turn"},
		{"rotate", "", tool, "k1", "turn"},
		{"rotate", "turn", again, "k2", "new-turn"},
		{"rotate", "turn", tool, "k1", "turn"},
		{"rotate", "session", again, "k1", "session"},
		{"rotate", "off", again, "k2", "off"},
	} {
		isolate(t)
		id := fmt.Sprintf("aff%d", i)
		v := &keyed{}
		serveOn(t, id, "k1", []string{"m"}, v, "k2")
		provider.SetRouting(id, c.routing)
		if err := provider.SetAffinity(id, c.affinity); err != nil {
			t.Fatal(err)
		}
		s := gateway.New()
		sess := "sess-" + id
		rq := func(b string) { postAs(t, s, sess, strings.ReplaceAll(b, "aff/m", id+"/m")) }
		rq(first)
		rq(c.body)
		r := lastRoute(t, s)
		if got := v.last(); got != c.want || why(r) != c.why {
			t.Errorf("%d %s %q: went to %s (%+v), want %s by %s", i, c.routing, c.affinity, got, sub(r, "affinity"), c.want, c.why)
		}
		if a := sub(r, "affinity"); c.why == "turn" || c.why == "session" || c.why == "cache" {
			if a["kept"] != true {
				t.Errorf("%d: not marked kept: %+v", i, a)
			}
		}
	}
}

func TestAcceptRotateByTurn(t *testing.T) {
	isolate(t)
	v := &keyed{}
	serveOn(t, "rot", "k1", []string{"m", "small"}, v, "k2", "k3")
	provider.SetRouting("rot", "rotate")
	s := gateway.New()
	var turns []string
	msgs := `{"role":"user","content":"hi"}`
	for i := range 4 {
		if i > 0 {
			msgs += `,{"role":"assistant","content":"yo"},{"role":"user","content":"more"}`
		}
		postAs(t, s, "s1", `{"model":"rot/m","messages":[`+msgs+`]}`)
		turns = append(turns, v.last())
		postAs(t, s, "s1", `{"model":"rot/small","messages":[{"role":"user","content":"title?"}]}`)
		postAs(t, s, "s1", `{"model":"rot/m","messages":[`+msgs+`,{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"a.go"}]}`)
		if got := v.last(); got != turns[i] {
			t.Fatalf("turn %d's tool result went to %s, not %s", i, got, turns[i])
		}
	}
	next := map[string]string{"k1": "k2", "k2": "k3", "k3": "k1"}
	for i := 1; i < len(turns); i++ {
		if turns[i] != next[turns[i-1]] {
			t.Fatalf("turns went %v", turns)
		}
	}
}

func TestAcceptAffinityRestingAndNewSession(t *testing.T) {
	isolate(t)
	const (
		first = `{"model":"rest/m","messages":[{"role":"user","content":"hi"}]}`
		again = `{"model":"rest/m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"more"}]}`
	)
	v := &keyed{}
	serveOn(t, "rest", "k1", []string{"m"}, v, "k2")
	provider.SetRouting("rest", "order")
	provider.SetAffinity("rest", "session")
	s := gateway.New()
	postAs(t, s, "s1", first)
	if v.last() != "k1" {
		t.Fatalf("first went to %s", v.last())
	}
	// make k1 fail on another session so it rests
	v.setFail(map[string]int{"k1": 429})
	postAs(t, s, "other", first)
	v.setFail(nil)
	v.mu.Lock()
	n := len(v.tried)
	v.mu.Unlock()
	postAs(t, s, "s1", again)
	r := lastRoute(t, s)
	v.mu.Lock()
	got := v.tried[n:]
	v.mu.Unlock()
	if len(got) == 0 || got[0] != "k2" || why(r) != "resting" {
		t.Fatalf("resting: %v %+v", got, sub(r, "affinity"))
	}
	postAs(t, s, "s2", first)
	if r := lastRoute(t, s); why(r) != "first" {
		t.Fatalf("new session: %+v", sub(r, "affinity"))
	}
}

func TestAcceptSameModelAutoGroup(t *testing.T) {
	isolate(t)
	serveOn(t, "orx", "k1", []string{"anthropic/claude-opus-5.5", "x:batch", "v1.beta"}, &keyed{})
	serveOn(t, "cpx", "k2", []string{"Claude-Opus-5.5", "x", "v1-beta"}, &keyed{})
	serveOn(t, "clx", "k3", []string{"claude-opus-5-5", "plain"}, &keyed{})
	serveOn(t, "dated", "k4", []string{"claude-opus-5-5-20260801"}, &keyed{})
	var groups []provider.Entry
	for _, e := range provider.Catalog() {
		if strings.HasPrefix(e.ID, "group/") {
			groups = append(groups, e)
		}
	}
	ids := []string{}
	for _, g := range groups {
		ids = append(ids, g.ID)
	}
	if len(groups) != 1 || groups[0].ID != "group/auto-claude-opus-5-5" {
		t.Fatalf("groups: %v", ids)
	}
	_, ms, ok := provider.FindGroup("group/auto-claude-opus-5-5")
	if !ok || len(ms) != 4 {
		t.Fatalf("members: %+v", ms)
	}
	for i, p := range []string{"orx", "cpx", "claude", "dated"} {
		if ms[i].Provider.ID != p {
			t.Fatalf("member order: %+v", ms)
		}
	}
	// variants are untouched: x:batch and x are different models, as are v1.beta and v1-beta
	for _, id := range ids {
		if strings.Contains(id, "batch") || strings.Contains(id, "beta") {
			t.Fatalf("variant grouped: %s", id)
		}
	}
}

func TestAcceptKeyPoolsByProtocol(t *testing.T) {
	isolate(t)
	v := &keyed{}
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "mix", Name: "MIX", Key: "k1", KeyProtocol: provider.Chat, Models: []string{"m"}, Chat: up.URL + "/v1",
		Keys:    []provider.KeyAccount{{Key: "k2"}, {Key: "k3", Protocol: provider.Chat}},
		Routing: "rotate"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := gateway.New()
	for range 4 {
		postAs(t, s, "", `{"model":"mix/m","messages":[{"role":"user","content":"hi"}]}`)
	}
	if got := strings.Join(v.tried, ","); got != "k1,k3,k1,k3" && got != "k3,k1,k3,k1" {
		t.Fatalf("rotation went %s", got)
	}
	o := order(lastRoute(t, s))
	if len(o) != 3 || o[2]["aside"] != true || o[0]["aside"] == true || o[1]["aside"] == true {
		t.Fatalf("order: %+v", o)
	}
	v.tried = nil
	v.setFail(map[string]int{"k1": 429, "k3": 429})
	code, body := postAs(t, s, "", `{"model":"mix/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from k2") {
		t.Fatalf("aside key: %d %s, tried %v", code, body, v.tried)
	}
}
