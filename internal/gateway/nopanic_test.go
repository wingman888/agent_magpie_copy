package gateway

import (
	"net/http"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Odd or empty inputs must not panic in the affinity, rotation and routing
// helpers, nor in the gateway's group path.

func TestNoPanicConversationHelpers(t *testing.T) {
	bodies := []string{
		"", "null", "{}", "[]", `{"messages":[]}`, `{"messages":[null]}`,
		`{"messages":[{"role":"user","content":5}]}`,
		`{"messages":[{"role":"user","content":[{"type":"tool_result"}]}]}`,
		`{"input":"hello"}`, `{"input":[]}`, `{"input":5}`, `{"contents":[{"role":"user","parts":null}]}`,
	}
	for i, b := range bodies {
		turns, within := turnsOf([]byte(b))
		cv := conversationOf(http.Header{}, []byte(b), "")
		if turns < 0 || cv.turns != turns || cv.within != within {
			t.Fatalf("case %d (%q): turns %d within %v, convo %+v", i, b, turns, within, cv)
		}
	}
	if turns, _ := turnsOf(nil); turns != 0 {
		t.Fatalf("nil body: %d turns", turns)
	}
}

func TestNoPanicAffinityAndRotation(t *testing.T) {
	// no candidates: rotation must not divide by zero, with or without a session
	if got := rotationStart("group/x", 0, convo{}); got != 0 {
		t.Fatalf("rotationStart with no candidates = %d", got)
	}
	if got := rotationStart("group/x", 0, convo{id: "s", model: "m"}); got != 0 {
		t.Fatalf("rotationStart with session and no candidates = %d", got)
	}
	if got := rotationStart("group/x", -1, convo{}); got != 0 {
		t.Fatalf("rotationStart with negative n = %d", got)
	}
	// keeping with no candidates, and with an empty conversation, is not kept
	if k, aff := keepLast("", convo{}, nil); k != -1 || aff.Why == "" {
		t.Fatalf("keepLast on empty input = %d, %+v", k, aff)
	}
	if k, _ := keepLast("session", convo{id: "s"}, []candidate{{}}); k != -1 {
		t.Fatalf("keepLast with a zero candidate kept %d", k)
	}
	remember(convo{}, candidate{}, 0) // no session: a no-op
	if m := affinityMode(provider.Group{}, nil); m != "" {
		t.Fatalf("affinityMode of nothing = %q", m)
	}
}

func TestNoPanicGroupRequests(t *testing.T) {
	accSetup(t, &accUpstream{}, "k1")
	s := New()
	for _, model := range []string{"group/", "group/nope", "group/ "} {
		code, _ := accRouteBody(t, s, "", `{"model":"`+model+`","messages":[]}`)
		if code != http.StatusNotFound {
			t.Fatalf("model %q: status %d", model, code)
		}
	}
	// a request with no messages, no session and odd content is handled without panic
	for _, body := range []string{`{"model":"fake/m1","messages":[]}`, `{"model":"fake/m1","messages":[{"role":"user","content":5}]}`} {
		_, _ = accRouteBody(t, s, "", body)
	}
}
