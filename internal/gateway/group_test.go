package gateway

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// groupProviders saves one provider per fake upstream, each with its own key
// and endpoint, and clears what earlier tests left resting.
func groupProviders(t *testing.T, fakes map[string]*fake) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.note = map[string]Rest{}
	restingUntil.Unlock()
	for id, f := range fakes {
		up := httptest.NewServer(f)
		t.Cleanup(up.Close)
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: up.URL + "/v1", Models: []string{"only-a"}}); err != nil {
			t.Fatal(err)
		}
	}
}

func sendTo(s *Server, model string) (int, string) {
	rec := httptest.NewRecorder()
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func TestRoutingGroupTriesMembersInOrder(t *testing.T) {
	b := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"slow down"}}`}
	a := &fake{t: t, ctype: "application/json", reply: `{"id":"from-a","choices":[]}`}
	groupProviders(t, map[string]*fake{"a": a, "b": b})
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Routing: provider.Ordered,
		Members: []string{"b/vendor/m", "a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := sendTo(s, "group/mine")
	if code != 200 || !strings.Contains(body, "from-a") {
		t.Fatalf("%d %s", code, body)
	}
	if b.got == nil || !strings.Contains(string(a.got), `"model":"only-a"`) {
		t.Fatalf("b got %s, a got %s", b.got, a.got)
	}
	routes := s.Trace(context.Background(), 0, 0).Routes
	r := routes[len(routes)-1]
	if r.Group == nil || r.Group.ID != "mine" || r.Group.Routing != provider.Ordered || r.Group.Auto {
		t.Fatalf("group trace = %+v", r.Group)
	}
	if strings.Join(r.Group.Members, ",") != "b/vendor/m,a/only-a" {
		t.Fatalf("members = %v", r.Group.Members)
	}
	if len(r.Order) != 2 || r.Order[0].Provider != "b" || r.Order[1].Provider != "a" {
		t.Fatalf("order = %+v", r.Order)
	}
	if len(r.Tries) != 2 || r.Tries[0].ID != "b" || r.Tries[1].ID != "a" {
		t.Fatalf("tries = %+v", r.Tries)
	}
}

func TestDiscoveredGroupCanBeHiddenAndShown(t *testing.T) {
	a := &fake{t: t, ctype: "application/json", reply: `{"id":"from-a","choices":[]}`}
	b := &fake{t: t, ctype: "application/json", reply: `{"id":"from-b","choices":[]}`}
	groupProviders(t, map[string]*fake{"a": a, "b": b})
	// the vendors' lists are what makes the group: a and b both offer m
	for id, m := range map[string]string{"a": "m", "b": "vendor/m"} {
		if err := catalog.SaveLive(id, "", []catalog.Model{{ID: m, Name: m}}); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	if code, body := sendTo(s, "group/auto-m"); code != 200 {
		t.Fatalf("auto group: %d %s", code, body)
	}
	if err := provider.DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if code, _ := sendTo(s, "group/auto-m"); code != 404 {
		t.Fatalf("hidden group answered with %d", code)
	}
	if err := provider.ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if code, body := sendTo(s, "group/auto-m"); code != 200 {
		t.Fatalf("shown group: %d %s", code, body)
	}
}

func TestRoutingGroupWithNoMemberReady(t *testing.T) {
	a := &fake{t: t, ctype: "application/json", reply: `{"id":"from-a","choices":[]}`}
	groupProviders(t, map[string]*fake{"a": a})
	if err := provider.SaveGroup(provider.Group{ID: "gone", Members: []string{"nowhere/x"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := sendTo(s, "group/gone")
	if code != 404 || !strings.Contains(body, "no model ready") {
		t.Fatalf("%d %s", code, body)
	}
}
