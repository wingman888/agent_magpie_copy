package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func grpHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
}

// grpKeyed answers per key, Chat or Anthropic, streamed when asked: a key in
// limited is rate limited.
type grpKeyed struct {
	limited map[string]bool
	seen    []string // path and key of each request
}

func (k *grpKeyed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	k.seen = append(k.seen, r.URL.Path+" "+key)
	if k.limited[key] {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	if bytes.Contains(b, []byte(`"stream":true`)) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from-`+key+`"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			`data: [DONE]`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"from-`+key+`","choices":[]}`)
}

func grpLastRoute(t *testing.T, s *Server) Route {
	t.Helper()
	rs := s.Trace(context.Background(), 0, 0).Routes
	if len(rs) == 0 {
		t.Fatal("no route traced")
	}
	return rs[len(rs)-1]
}

func grpSend(s *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// A group of the user's, in order: its first member first, the next when
// that one is rate limited; the trace names the group.
func TestGrpGroupInOrder(t *testing.T) {
	grpHome(t)
	a := &grpKeyed{}
	b := &grpKeyed{limited: map[string]bool{"kb": true}}
	for _, x := range []struct {
		p provider.Provider
		h http.Handler
	}{
		{provider.Provider{ID: "a", Name: "A", Key: "ka", Models: []string{"m", "only-a"}, Fallback: []string{"c/x"}}, a},
		{provider.Provider{ID: "b", Name: "B", Key: "kb", Models: []string{"vendor/m"}}, b},
		{provider.Provider{ID: "c", Name: "C", Key: "kc", Models: []string{"x"}}, &grpKeyed{}},
	} {
		srv := httptest.NewServer(x.h)
		t.Cleanup(srv.Close)
		x.p.Chat = srv.URL + "/v1"
		if err := provider.Save(x.p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"b/vendor/m", "a/only-a", "b/vendor/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := grpSend(s, "/v1/chat/completions", `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from-ka") {
		t.Fatalf("%d %s", code, body)
	}
	if len(b.seen) != 1 || len(a.seen) != 1 {
		t.Fatalf("b saw %v, a saw %v", b.seen, a.seen)
	}
	r := grpLastRoute(t, s)
	if r.Group == nil || r.Group.ID != "mine" || r.Group.Name != "Mine" || r.Group.Routing != "order" || r.Group.Auto ||
		strings.Join(r.Group.Members, ",") != "b/vendor/m,a/only-a" {
		t.Fatalf("group %+v", r.Group)
	}
	// a member's own fallback is not the group's
	if len(r.Order) != 2 || r.Order[0].Provider != "b" || r.Order[0].Model != "vendor/m" || r.Order[1].Provider != "a" || r.Order[1].Model != "only-a" {
		t.Fatalf("order %+v", r.Order)
	}
	if len(r.Tries) != 2 || r.Tries[0].Status != 429 || r.Tries[1].Status != 200 {
		t.Fatalf("tries %+v", r.Tries)
	}
	if c := s.Recent()[0]; c.Provider != "a" || c.Model != "group/mine" {
		t.Fatalf("call %+v", c)
	}
	// the trace JSON carries the group as the interface says
	js, _ := json.Marshal(r)
	for _, want := range []string{`"group":{"id":"mine","name":"Mine","routing":"order","affinity":"","auto":false,"members":["b/vendor/m","a/only-a"]}`} {
		if !bytes.Contains(js, []byte(want)) {
			t.Fatalf("trace %s", js)
		}
	}

	// a group with no member ready is a 404 that says so
	if err := provider.SaveGroup(provider.Group{ID: "ghost", Members: []string{"nowhere/x"}}); err != nil {
		t.Fatal(err)
	}
	code, body = grpSend(s, "/v1/chat/completions", `{"model":"group/ghost","messages":[{"role":"user","content":"hi"}]}`)
	if code != 404 || !strings.Contains(body, "no model ready") {
		t.Fatalf("ghost: %d %s", code, body)
	}
	code, _ = grpSend(s, "/v1/chat/completions", `{"model":"group/none-such","messages":[{"role":"user","content":"hi"}]}`)
	if code != 404 {
		t.Fatalf("unknown group: %d", code)
	}
}

// A found group serves until removed, and again once shown.
func TestGrpAutoGroupRemovedAndShown(t *testing.T) {
	grpHome(t)
	a, b := &grpKeyed{}, &grpKeyed{}
	for _, x := range []struct {
		p provider.Provider
		h http.Handler
	}{
		{provider.Provider{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}}, a},
		{provider.Provider{ID: "b", Name: "B", Key: "kb", Models: []string{"vendor/m"}}, b},
	} {
		srv := httptest.NewServer(x.h)
		t.Cleanup(srv.Close)
		x.p.Chat = srv.URL + "/v1"
		if err := provider.Save(x.p); err != nil {
			t.Fatal(err)
		}
	}
	req := `{"model":"group/auto-m","messages":[{"role":"user","content":"hi"}]}`
	s := New()
	if code, body := grpSend(s, "/v1/chat/completions", req); code != 200 || !strings.Contains(body, "from-k") {
		t.Fatalf("found: %d %s", code, body)
	}
	r := grpLastRoute(t, s)
	if r.Group == nil || r.Group.ID != "auto-m" || !r.Group.Auto || len(r.Order) != 2 {
		t.Fatalf("route %+v %+v", r.Group, r.Order)
	}
	if err := provider.DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if code, _ := grpSend(s, "/v1/chat/completions", req); code != 404 {
		t.Fatalf("removed: %d", code)
	}
	if err := provider.ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if code, _ := grpSend(s, "/v1/chat/completions", req); code != 200 {
		t.Fatalf("shown: %d", code)
	}
}

// Keys made for different protocols are separate pools: routing turns
// within the pool that suits the request best, the rest tried after it,
// marked aside.
func TestGrpKeysPooledByProtocol(t *testing.T) {
	grpHome(t)
	up := &grpKeyed{limited: map[string]bool{}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	p := provider.Provider{ID: "pool", Name: "Pool", Chat: srv.URL + "/v1", Anthropic: srv.URL, Models: []string{"m"},
		Routing: provider.Rotate, Key: "k1",
		Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3", Protocol: provider.Chat}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := New()
	keyOf := func(w Weighed) string { return w.ID }
	k1, k2, k3 := "pool#"+provider.KeyID("k1"), "pool#"+provider.KeyID("k2"), "pool#"+provider.KeyID("k3")
	asides := func(r Route) string {
		var out []string
		for _, w := range r.Order {
			x := "-"
			if w.Aside {
				x = "aside"
			}
			out = append(out, keyOf(w)+":"+x)
		}
		return strings.Join(out, " ")
	}

	// Chat: the Chat key is the pool; the keys made for none follow, aside
	chat := `{"model":"pool/m","messages":[{"role":"user","content":"hi"}]}`
	for range 3 {
		up.seen = nil
		if code, body := grpSend(s, "/v1/chat/completions", chat); code != 200 || !strings.Contains(body, "from-k3") {
			t.Fatalf("chat: %d %s", code, body)
		}
		r := grpLastRoute(t, s)
		if got := asides(r); got != k3+":- "+k1+":aside "+k2+":aside" {
			t.Fatalf("chat order %s", got)
		}
	}

	// Anthropic: the keys made for none take turns; the Chat key waits last
	msgs := `{"model":"pool/m","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`
	var firsts []string
	for range 4 {
		up.seen = nil
		if code, body := grpSend(s, "/v1/messages", msgs); code != 200 {
			t.Fatalf("messages: %d %s", code, body)
		}
		r := grpLastRoute(t, s)
		if len(r.Order) != 3 || r.Order[2].ID != k3 || !r.Order[2].Aside || r.Order[0].Aside || r.Order[1].Aside {
			t.Fatalf("messages order %s", asides(r))
		}
		firsts = append(firsts, r.Order[0].ID)
		if len(up.seen) != 1 || up.seen[0] != "/v1/messages "+map[string]string{k1: "k1", k2: "k2"}[r.Order[0].ID] {
			t.Fatalf("upstream saw %v for %s", up.seen, r.Order[0].ID)
		}
	}
	for i := 1; i < len(firsts); i++ {
		if firsts[i] == firsts[i-1] || firsts[i] == k3 {
			t.Fatalf("turns %v", firsts)
		}
	}

	// the pool all failing, the key set aside still answers
	up.limited["k1"], up.limited["k2"] = true, true
	up.seen = nil
	code, body := grpSend(s, "/v1/messages", msgs)
	if code != 200 || !strings.Contains(body, "from-k3") {
		t.Fatalf("aside: %d %s (saw %v)", code, body, up.seen)
	}
	if len(up.seen) != 3 || up.seen[2] != "/v1/chat/completions k3" {
		t.Fatalf("saw %v", up.seen)
	}
}
