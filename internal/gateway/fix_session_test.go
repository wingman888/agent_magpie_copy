package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// FIX: the session id for affinity and rotation comes from the headers only,
// in the order sessionHeaders lists them.
func TestFIXSessionOfHeadersOnly(t *testing.T) {
	if got := sessionOf(http.Header{}); got != "" {
		t.Fatalf("no header: %q, want empty", got)
	}
	h := http.Header{}
	h.Set("Session-Id", "c")
	h.Set("X-Session-Id", "b")
	if got := sessionOf(h); got != "b" {
		t.Fatalf("x-session-id should lead session-id: %q", got)
	}
}

// FIX: a request with no session header has no conversation for affinity
// or rotation, even though its body would hash to one.
func TestFIXNoSessionNoConversation(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	if tn := turnOf(r, provider.Chat, body, "m"); tn.conv != "" || tn.turn != 1 {
		t.Fatalf("headerless turn: conv=%q turn=%d, want \"\" and 1", tn.conv, tn.turn)
	}
	r.Header.Set("x-session-id", "ses_fix")
	if tn := turnOf(r, provider.Chat, body, "m"); tn.conv != "ses_fix" {
		t.Fatalf("header turn: conv=%q", tn.conv)
	}
}
