package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// scripted is an upstream answering with a fixed status and body, counting
// the requests it got.
type grpScripted struct {
	code int
	hits int
}

const grpChatOK = `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

func (f *grpScripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.hits++
	io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	if f.code != 0 && f.code != http.StatusOK {
		w.WriteHeader(f.code)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, grpChatOK)
}

// groupFixture configures two Chat providers, each on its own fake upstream,
// and returns the upstreams and a handler of one server.
func grpGroupFixture(t *testing.T, prefix string, bCode int) (*grpScripted, *grpScripted, *Server, http.Handler) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fa, fb := &grpScripted{}, &grpScripted{code: bCode}
	ua := httptest.NewServer(fa)
	ub := httptest.NewServer(fb)
	t.Cleanup(ua.Close)
	t.Cleanup(ub.Close)
	if err := provider.Save(provider.Provider{ID: prefix + "-a", Name: "A", Key: "ka", Models: []string{"only-a"}, Chat: ua.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: prefix + "-b", Name: "B", Key: "kb", Models: []string{"vendor/m"}, Chat: ub.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	return fa, fb, s, s.Handler()
}

// chatCall sends a Chat request for model through the handler.
func grpChatCall(h http.Handler, model string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// lastRoute is the newest route the server traced.
func grpLastRoute(t *testing.T, s *Server) Route {
	t.Helper()
	st := s.Trace(context.Background(), 0, 0)
	if len(st.Routes) == 0 {
		t.Fatal("no route traced")
	}
	return st.Routes[len(st.Routes)-1]
}

func grpOrderIDs(ws []Weighed) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.ID)
	}
	return out
}

// An order group tries its members in their order: b first, and when b
// answers 429, a answers.
func TestGrpGroupOrderFallsBackOn429(t *testing.T) {
	fa, fb, s, h := grpGroupFixture(t, "gx", http.StatusTooManyRequests)
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Routing: provider.Ordered,
		Members: []string{"gx-b/vendor/m", "gx-a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	code, body := grpChatCall(h, "group/mine")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if fb.hits != 1 || fa.hits != 1 {
		t.Fatalf("hits: b=%d a=%d", fb.hits, fa.hits)
	}
	rt := grpLastRoute(t, s)
	if rt.Group == nil || rt.Group.ID != "mine" || rt.Group.Name != "Mine" || rt.Group.Routing != provider.Ordered || rt.Group.Auto {
		t.Fatalf("group trace: %+v", rt.Group)
	}
	if got := strings.Join(rt.Group.Members, ","); got != "gx-b/vendor/m,gx-a/only-a" {
		t.Errorf("members: %s", got)
	}
	if got := strings.Join(grpOrderIDs(rt.Order), ","); got != "gx-b,gx-a" {
		t.Errorf("order: %s", got)
	}
	if len(rt.Tries) != 2 || rt.Tries[0].ID != "gx-b" || rt.Tries[0].Status != 429 || rt.Tries[0].Fail != failRate ||
		rt.Tries[1].ID != "gx-a" || rt.Tries[1].Status != 200 {
		t.Errorf("tries: %+v", rt.Tries)
	}

	// b now rests after its 429, so the next request goes to a first
	if code, body := grpChatCall(h, "group/mine"); code != 200 {
		t.Fatalf("second status %d: %s", code, body)
	}
	rt = grpLastRoute(t, s)
	if got := strings.Join(grpOrderIDs(rt.Order), ","); got != "gx-a,gx-b" {
		t.Errorf("order while b rests: %s", got)
	}
	if rt.Order[1].Rest == nil {
		t.Errorf("b should show as resting: %+v", rt.Order[1])
	}
	if fb.hits != 1 {
		t.Errorf("resting b was tried again: hits=%d", fb.hits)
	}
}

// A group none of whose members is usable answers 404 saying so.
func TestGrpGroupWithNoReadyMemberIs404(t *testing.T) {
	_, _, _, h := grpGroupFixture(t, "gy", 0)
	if err := provider.SaveGroup(provider.Group{ID: "gone", Members: []string{"gy-nobody/nothing"}}); err != nil {
		t.Fatal(err)
	}
	code, body := grpChatCall(h, "group/gone")
	if code != http.StatusNotFound {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "没有模型就绪") || !strings.Contains(body, "group/gone") {
		t.Errorf("body: %s", body)
	}
}

// A group id that names no group is an unknown model, as any other.
func TestGrpUnknownGroupIs404(t *testing.T) {
	_, _, _, h := grpGroupFixture(t, "gz", 0)
	if code, body := grpChatCall(h, "group/nope"); code != http.StatusNotFound {
		t.Fatalf("status %d: %s", code, body)
	}
}

// A group inside a group is ignored, and a member listed twice counts once.
func TestGrpGroupIgnoresNestedGroupAndDuplicates(t *testing.T) {
	fa, fb, s, h := grpGroupFixture(t, "gn", 0)
	if err := provider.SaveGroup(provider.Group{ID: "nest", Routing: provider.Ordered,
		Members: []string{"group/other", "gn-a/only-a", "gn-a/only-a", "gn-b/vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	if code, body := grpChatCall(h, "group/nest"); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	rt := grpLastRoute(t, s)
	if got := strings.Join(rt.Group.Members, ","); got != "gn-a/only-a,gn-b/vendor/m" {
		t.Errorf("members: %s", got)
	}
	if got := strings.Join(grpOrderIDs(rt.Order), ","); got != "gn-a,gn-b" {
		t.Errorf("order: %s", got)
	}
	if fa.hits != 1 || fb.hits != 0 {
		t.Errorf("hits: a=%d b=%d", fa.hits, fb.hits)
	}
}

// A group is answered by its first usable member as a model.
func TestGrpGroupModelAnswersAsFirstMember(t *testing.T) {
	_, _, _, h := grpGroupFixture(t, "gm", 0)
	if err := provider.SaveGroup(provider.Group{ID: "first", Name: "First", Routing: provider.Ordered,
		Members: []string{"gm-b/vendor/m", "gm-a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/"+url.PathEscape("group/first"), nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["id"] != "group/first" || m["owned_by"] != "gm-b" || m["display_name"] != "First" {
		t.Errorf("model: %v", m)
	}
}
