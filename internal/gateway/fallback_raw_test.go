package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// fbPostMessages sends a streamed Anthropic-shaped request, so a provider that
// speaks only Chat is served through the translate path.
func fbPostMessages(t *testing.T, s *Server, model string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"` + model + `","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rec, req)
	return rec
}

const fbChatStreamB = "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"from B\"}}]}\n\n" +
	"data: [DONE]\n\n"

func TestFallbackQuotaWordInRawBodyOf400Switches(t *testing.T) {
	a := &fbUpstream{code: 400, ctype: "application/json", body: `{"error":{"code":"insufficient_quota","message":"请求失败"}}`}
	b := &fbUpstream{code: 200, ctype: "application/json", body: fbOK}
	fbSetup(t, a, b, []string{"b/m2"})
	rec := fbPost(t, New(), "a/m1", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("status %d body %s, want B's reply", rec.Code, rec.Body)
	}
	if b.hits.Load() != 1 {
		t.Fatalf("B received %d requests, want 1", b.hits.Load())
	}
}

func TestFallbackLongPlainTextQuotaWordIn400Switches(t *testing.T) {
	long := strings.Repeat("x", 250) + " Quota exhausted for this key"
	a := &fbUpstream{code: 400, ctype: "text/plain", body: long}
	b := &fbUpstream{code: 200, ctype: "application/json", body: fbOK}
	fbSetup(t, a, b, []string{"b/m2"})
	rec := fbPost(t, New(), "a/m1", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("status %d body %s, want B's reply", rec.Code, rec.Body)
	}
}

func TestFallbackPlain400StillNotSwitchedWhenBodyHasNoQuotaWord(t *testing.T) {
	a := &fbUpstream{code: 400, ctype: "application/json", body: `{"error":{"message":"messages: field required"}}`}
	b := &fbUpstream{code: 200, ctype: "application/json", body: fbOK}
	fbSetup(t, a, b, []string{"b/m2"})
	rec := fbPost(t, New(), "a/m1", false)
	if rec.Code != 400 || b.hits.Load() != 0 {
		t.Fatalf("status %d, B hits %d; want 400 and no B request", rec.Code, b.hits.Load())
	}
}

func TestFallbackStreamErrorBeforeOutputSwitches(t *testing.T) {
	sse := "data: {\"error\":{\"message\":\"boom A\"}}\n\n"
	a := &fbUpstream{code: 200, ctype: "text/event-stream", body: sse}
	b := &fbUpstream{code: 200, ctype: "text/event-stream", body: fbChatStreamB}
	fbSetup(t, a, b, []string{"b/m2"})
	s := New()
	rec := fbPostMessages(t, s, "a/m1")
	if rec.Code != 200 {
		t.Fatalf("status %d body %s, want 200 from B", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "boom A") || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("body %s, want only B's content", rec.Body)
	}
	if b.hits.Load() != 1 {
		t.Fatalf("B received %d requests, want 1", b.hits.Load())
	}
	if c := s.Recent()[0]; c.Provider != "b" || !strings.HasPrefix(c.Fallback, "a: ") {
		t.Fatalf("record provider %q fallback %q, want b with an a: note", c.Provider, c.Fallback)
	}
}

func TestFallbackStreamErrorAfterOutputOnTranslatePathDoesNotSwitch(t *testing.T) {
	sse := "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial A\"}}]}\n\n" +
		"data: {\"error\":{\"message\":\"stream broke\"}}\n\n"
	a := &fbUpstream{code: 200, ctype: "text/event-stream", body: sse}
	b := &fbUpstream{code: 200, ctype: "text/event-stream", body: fbChatStreamB}
	fbSetup(t, a, b, []string{"b/m2"})
	rec := fbPostMessages(t, New(), "a/m1")
	if !strings.Contains(rec.Body.String(), "partial A") || strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("body %s, want A's partial output and no B content", rec.Body)
	}
	if b.hits.Load() != 0 {
		t.Fatalf("B received %d requests, want none", b.hits.Load())
	}
}
