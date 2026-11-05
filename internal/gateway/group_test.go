package gateway

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const groupChatReply = `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`

// A routing group with no ready member answers 404, saying so.
func TestGroupWithNoModelReady(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, ctype: "application/json", reply: groupChatReply})
	if err := provider.SaveGroup(provider.Group{ID: "empty", Members: []string{"ghost/m1"}}); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"group/empty","messages":[{"role":"user","content":"hi"}]}`)
	if code != 404 || !strings.Contains(body, "no model ready") {
		t.Errorf("%d %s", code, body)
	}
}

// A routing group with a ready member answers, and the trace says which
// group it was and who its members were.
func TestGroupRouteInTrace(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: groupChatReply}
	setup(t, provider.Chat, f)
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"fake/m1", "ghost/m2"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	st := s.Trace(context.Background(), 0, 0)
	if len(st.Routes) == 0 {
		t.Fatal("no route traced")
	}
	r := st.Routes[len(st.Routes)-1]
	if r.Group == nil || r.Group.ID != "mine" || r.Group.Name != "Mine" || r.Group.Auto {
		t.Fatalf("group %+v", r.Group)
	}
	if !slices.Equal(r.Group.Members, []string{"fake/m1"}) {
		t.Fatalf("members %v", r.Group.Members)
	}
}
