package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// e2eReset gives a test its own config and cache, and no resting candidates.
func e2eReset(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.note = map[string]Rest{}
	restingUntil.Unlock()
}

// e2eServe starts an upstream answering as h and saves p to talk to it.
func e2eServe(t *testing.T, p provider.Provider, h http.Handler) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	p.Chat = up.URL + "/v1"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// e2eMembers sets up the group of the acceptance check: mine = b/vendor/m,
// then a/only-a, in order. b is rate limited, so a answers.
func e2eMembers(t *testing.T) (b, a *fake) {
	t.Helper()
	e2eReset(t)
	b = &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"slow down"}}`}
	// a streams, as the providers magpie translates for always do
	a = &fake{t: t, reply: sse(
		`data: {"id":"e2e-a","model":"only-a","choices":[{"delta":{"role":"assistant","content":"hello from a"}}]}`,
		`data: {"id":"e2e-a","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"e2e-a","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
		`data: [DONE]`)}
	e2eServe(t, provider.Provider{ID: "b", Name: "B", Key: "k-b", Models: []string{"vendor/m"}}, b)
	e2eServe(t, provider.Provider{ID: "a", Name: "A", Key: "k-a", Models: []string{"only-a"}}, a)
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"b/vendor/m", "a/only-a"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	return b, a
}

// e2ePost sends one request to path through s, in the conversation session.
func e2ePost(s *Server, path, session, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("x-session-id", session)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// e2eLast is the newest route the trace holds.
func e2eLast(t *testing.T, s *Server) Route {
	t.Helper()
	st := s.Trace(context.Background(), 0, 0)
	if len(st.Routes) == 0 {
		t.Fatal("no route traced")
	}
	return st.Routes[len(st.Routes)-1]
}

// E2E: every client API that takes a model name sends a routing group to the
// group's routing. The first member is rate limited, the second answers, and
// the trace names the group.
func TestE2EGroupAnswersThroughEveryEntry(t *testing.T) {
	cases := []struct {
		name, path, body string
	}{
		{"anthropic messages", "/v1/messages", `{"model":"group/mine","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
		{"anthropic messages without v1", "/messages", `{"model":"group/mine","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
		{"chat completions", "/v1/chat/completions", `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`},
		{"chat completions without v1", "/chat/completions", `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`},
		{"responses", "/v1/responses", `{"model":"group/mine","input":"hi"}`},
		{"responses without v1", "/responses", `{"model":"group/mine","input":"hi"}`},
		{"gemini generateContent", "/v1beta/models/group/mine:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, a := e2eMembers(t)
			s := New()
			code, body := e2ePost(s, c.path, "", c.body)
			if code != 200 {
				t.Fatalf("%s: %d %s", c.path, code, body)
			}
			if b.got == nil || a.got == nil {
				t.Fatalf("%s: members tried b=%v a=%v", c.path, b.got != nil, a.got != nil)
			}
			r := e2eLast(t, s)
			if r.Group == nil || r.Group.ID != "mine" || r.Status != 200 {
				t.Fatalf("%s: route %+v", c.path, r)
			}
			if len(r.Tries) != 2 || r.Tries[0].ID != "b" || r.Tries[0].Status != 429 || r.Tries[1].ID != "a" || r.Tries[1].Status != 200 {
				t.Fatalf("%s: tries %+v", c.path, r.Tries)
			}
			if len(r.Order) != 2 || r.Order[0].Provider != "b" || r.Order[1].Provider != "a" {
				t.Fatalf("%s: order %+v", c.path, r.Order)
			}
		})
	}
}

// E2E: the trace JSON carries the group and affinity blocks under the keys the
// Gateway view reads, and an aside key only where a candidate is one.
func TestE2EGroupTraceKeys(t *testing.T) {
	e2eMembers(t)
	s := New()
	if code, body := e2ePost(s, "/v1/messages", "e2e-session-1", `{"model":"group/mine","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	r := e2eLast(t, s)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	g := e2eObject(t, doc, "group")
	for _, k := range []string{"id", "name", "routing", "affinity", "auto", "members"} {
		if _, ok := g[k]; !ok {
			t.Fatalf("group has no %q: %v", k, g)
		}
	}
	if g["id"] != "mine" || g["name"] != "Mine" || g["routing"] != provider.Ordered || g["auto"] != false {
		t.Fatalf("group %v", g)
	}
	if ms, _ := g["members"].([]any); len(ms) != 2 || ms[0] != "b/vendor/m" || ms[1] != "a/only-a" {
		t.Fatalf("group members %v", g["members"])
	}
	af := e2eObject(t, doc, "affinity")
	for _, k := range []string{"mode", "turn", "within", "cacheRead", "kept", "why"} {
		if _, ok := af[k]; !ok {
			t.Fatalf("affinity has no %q: %v", k, af)
		}
	}
	if af["mode"] != "auto" || af["turn"] != float64(1) || af["within"] != false || af["kept"] != false || af["why"] != "first" {
		t.Fatalf("affinity %v", af)
	}
	if _, ok := af["last"]; ok {
		t.Fatalf("a first answer has no last: %v", af)
	}
	// the main pool carries no aside
	for _, o := range e2eList(t, doc, "order") {
		if _, ok := o["aside"]; ok {
			t.Fatalf("main pool candidate marked aside: %v", o)
		}
	}

	// the next turn: the first member rests after its 429, the other answers
	// again, and affinity says why it did not stick to the last answerer
	if code, body := e2ePost(s, "/v1/messages", "e2e-session-1", `{"model":"group/mine","max_tokens":16,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"user","content":"again"}]}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	r = e2eLast(t, s)
	if r.Affinity == nil || r.Affinity.Turn != 2 || r.Affinity.Last != "a" || r.Affinity.Why == "" {
		t.Fatalf("affinity on the next turn: %+v", r.Affinity)
	}
}

// E2E: a routing group that resolves to no usable member answers 404 saying
// the group has no model ready, on every entry that takes a model name.
func TestE2EGroupNotReadyAnswers404(t *testing.T) {
	e2eMembers(t)
	s := New()
	for _, c := range []struct{ path, body string }{
		{"/v1/messages", `{"model":"group/nobody","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/chat/completions", `{"model":"group/nobody","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"model":"group/nobody","input":"hi"}`},
		{"/v1/messages/count_tokens", `{"model":"group/nobody","messages":[{"role":"user","content":"hi"}]}`},
		{"/backend-api/codex/responses", `{"model":"group/nobody","input":"hi"}`},
	} {
		code, body := e2ePost(s, c.path, "", c.body)
		if code != 404 || !strings.Contains(body, "has no model ready") {
			t.Fatalf("%s: %d %s", c.path, code, body)
		}
	}
}

// E2E: the model lists name the group group/<id>, and GET /v1/models/group/<id>
// finds it, whether or not the lookup has a slash in it.
func TestE2EGroupModelIDs(t *testing.T) {
	e2eMembers(t)
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var found map[string]any
	for _, m := range list.Data {
		if m["id"] == "group/mine" {
			found = m
		}
	}
	if found == nil || found["owned_by"] != "b" {
		t.Fatalf("group/mine in /v1/models: %v", found)
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/group/mine", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"group/mine"`) {
		t.Fatalf("GET /v1/models/group/mine: %d %s", rec.Code, rec.Body.String())
	}

	// a group with no usable member is not listed, and its lookup is 404
	if err := provider.DeleteGroup("mine"); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/group/mine", nil))
	if rec.Code != 404 {
		t.Fatalf("deleted group lookup: %d %s", rec.Code, rec.Body.String())
	}
}

// E2E: FindGroup's members name themselves as provider/model.
func TestE2EGroupMemberRef(t *testing.T) {
	e2eMembers(t)
	_, ms, ok := provider.FindGroup("group/mine")
	if !ok || len(ms) != 2 {
		t.Fatalf("FindGroup: %v %d", ok, len(ms))
	}
	if ms[0].Ref() != "b/vendor/m" || ms[0].Provider.ID != "b" || ms[0].Model != "vendor/m" {
		t.Fatalf("first member %+v", ms[0])
	}
	if ms[1].Ref() != "a/only-a" {
		t.Fatalf("second member %+v", ms[1])
	}
}

// e2eObject is the JSON object under key, or a test failure.
func e2eObject(t *testing.T, doc map[string]any, key string) map[string]any {
	t.Helper()
	o, ok := doc[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object: %v", key, doc[key])
	}
	return o
}

// e2eList is the JSON objects of the list under key.
func e2eList(t *testing.T, doc map[string]any, key string) []map[string]any {
	t.Helper()
	xs, _ := doc[key].([]any)
	var out []map[string]any
	for _, x := range xs {
		if o, ok := x.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}
