package gateway

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// fbStreamOK is B's answer when it is asked to stream.
const fbStreamOK = "data: " + `{"id":"b1","object":"chat.completion.chunk","model":"m2","choices":[{"index":0,"delta":{"content":"from B"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"

func setOnHit(u *upstream, f func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.onHit = f
}

// messages sends one Anthropic Messages request through the gateway.
func messages(t *testing.T, s *Server, model string) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"model":%q,"max_tokens":16,"stream":false,"messages":[{"role":"user","content":"hi"}]}`, model)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("anthropic-version", "2023-06-01")
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// A fallback that names a model the provider does not offer is never sent,
// even though provider.Resolve would accept the name.
func TestUnofferedFallbackModelIsSkipped(t *testing.T) {
	a := &upstream{status: 429, body: fbRateLimited}
	b := &upstream{body: fbChatOK}
	s := pair(t, a, b, "b/nope", "b/m2")
	code, body := chat(t, s, "a/m1", false)
	if code != 200 || !strings.Contains(body, "from B") {
		t.Fatalf("status %d, body %s; want B's reply", code, body)
	}
	if b.count() != 1 || b.lastModel() != "m2" {
		t.Errorf("B got %d requests, last model %q; want one request for m2", b.count(), b.lastModel())
	}
}

// A client that leaves while the primary is answering must not put the
// primary into the resting table: its failure was not the provider's.
func TestDisconnectDoesNotRestProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &upstream{status: 429, body: fbRateLimited, onHit: cancel}
	b := &upstream{body: fbChatOK}
	s := pair(t, a, b, "b/m2")
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m1","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if b.count() != 0 {
		t.Errorf("B got %d requests after the client left, want none", b.count())
	}
	if _, resting := s.restNote("a"); resting {
		t.Fatal("A is resting after the client left; it must not be")
	}
	// the next request still tries A first
	setOnHit(a, nil)
	if code, body := chat(t, s, "a/m1", false); code != 200 || !strings.Contains(body, "from B") {
		t.Fatalf("status %d, body %s; want B's reply", code, body)
	}
	if a.count() != 2 {
		t.Errorf("A got %d requests, want 2: A must be tried first again", a.count())
	}
}

// Once the minute of rest is over, the provider is tried first again.
func TestRestingEndsAfterOneMinute(t *testing.T) {
	a := &upstream{status: 429, body: fbRateLimited}
	b := &upstream{body: fbChatOK}
	s := pair(t, a, b, "b/m2")
	if code, body := chat(t, s, "a/m1", false); code != 200 || !strings.Contains(body, "from B") {
		t.Fatalf("first request: status %d, body %s", code, body)
	}
	if _, resting := s.restNote("a"); !resting {
		t.Fatal("A should be resting straight after its failure")
	}
	s.mu.Lock()
	e := s.resting["a"]
	e.until = time.Now().Add(-time.Second)
	s.resting["a"] = e
	s.mu.Unlock()
	if code, body := chat(t, s, "a/m1", false); code != 200 || !strings.Contains(body, "from B") {
		t.Fatalf("second request: status %d, body %s", code, body)
	}
	if a.count() != 2 {
		t.Errorf("A got %d requests, want 2: after the rest A is tried first", a.count())
	}
}

// A provider that cannot be reached at all is a switchable failure.
func TestTransportFailureSwitches(t *testing.T) {
	a := &upstream{}
	b := &upstream{body: fbChatOK}
	s := pair(t, a, b, "b/m2")
	// re-point A at a port nothing listens on
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Models: []string{"m1"},
		Chat: "http://127.0.0.1:1/v1", Fallback: []string{"b/m2"}}); err != nil {
		t.Fatal(err)
	}
	code, body := chat(t, s, "a/m1", false)
	if code != 200 || !strings.Contains(body, "from B") {
		t.Fatalf("status %d, body %s; want B's reply", code, body)
	}
	if b.count() != 1 {
		t.Errorf("B got %d requests, want 1", b.count())
	}
	if rec := s.Recent()[0]; rec.Provider != "b" || !strings.HasPrefix(rec.Fallback, "a: 502") {
		t.Errorf("record provider %q fallback %q", rec.Provider, rec.Fallback)
	}
}

// A 200 whose first event is an error is judged by that error, before any of
// it reaches the client.
func TestErrorAtHeadOfStreamSwitches(t *testing.T) {
	cases := []struct{ name, body string }{
		{"overloaded chat error", "data: {\"error\":{\"message\":\"Overloaded\",\"type\":\"overloaded_error\"}}\n\n"},
		{"overloaded named event", "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"},
		{"rate limit by code", "data: {\"error\":{\"message\":\"slow down\",\"code\":\"rate_limit_exceeded\"}}\n\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &upstream{ctype: "text/event-stream", body: c.body}
			b := &upstream{body: fbChatOK}
			s := pair(t, a, b, "b/m2")
			code, body := chat(t, s, "a/m1", true)
			if code != 200 || !strings.Contains(body, "from B") {
				t.Fatalf("status %d, body %s; want B's reply", code, body)
			}
			if strings.Contains(body, "Overloaded") || strings.Contains(body, "slow down") {
				t.Errorf("reply carries A's error: %s", body)
			}
			if a.count() != 1 || b.count() != 1 {
				t.Errorf("hits: A=%d B=%d, want 1 and 1", a.count(), b.count())
			}
			if rec := s.Recent()[0]; rec.Provider != "b" || !strings.HasPrefix(rec.Fallback, "a: ") {
				t.Errorf("record provider %q fallback %q", rec.Provider, rec.Fallback)
			}
		})
	}
}

// Once a success has begun, an error later in its stream stays where it is:
// no switch, and nothing of B's reply is mixed in.
func TestErrorAfterContentDoesNotSwitch(t *testing.T) {
	first := "data: " + `{"id":"a1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial"}}]}` + "\n\n"
	a := &upstream{ctype: "text/event-stream", body: first + "data: {\"error\":{\"message\":\"Overloaded\",\"type\":\"overloaded_error\"}}\n\n"}
	b := &upstream{body: fbChatOK}
	s := pair(t, a, b, "b/m2")
	code, body := chat(t, s, "a/m1", true)
	if code != 200 || !strings.Contains(body, "partial") {
		t.Fatalf("status %d, body %s; want A's partial reply", code, body)
	}
	if strings.Contains(body, "from B") {
		t.Errorf("B's reply was mixed into the stream: %s", body)
	}
	if b.count() != 0 {
		t.Errorf("B got %d requests, want none", b.count())
	}
}

// A non-streamed reply that carries an error in-band switches only when the
// error's own kind is retryable; a bad request is answered as it always was.
func TestNonStreamInbandErrorKindDecidesSwitch(t *testing.T) {
	cases := []struct {
		name, errData string
		switches      bool
	}{
		{"invalid request stays", `{"error":{"message":"messages: field required","type":"invalid_request_error"}}`, false},
		{"overloaded switches", `{"error":{"message":"Overloaded","type":"overloaded_error"}}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &upstream{ctype: "text/event-stream", body: "data: " + c.errData + "\n\n"}
			b := &upstream{ctype: "text/event-stream", body: fbStreamOK}
			s := pair(t, a, b, "b/m2")
			code, body := messages(t, s, "a/m1")
			if c.switches {
				if code != 200 || !strings.Contains(body, "from B") {
					t.Fatalf("status %d, body %s; want B's reply", code, body)
				}
				if b.count() != 1 {
					t.Errorf("B got %d requests, want 1", b.count())
				}
				return
			}
			if code != 502 || !strings.Contains(body, "messages: field required") {
				t.Fatalf("status %d, body %s; want the error as it was", code, body)
			}
			if b.count() != 0 {
				t.Errorf("B got %d requests, want none", b.count())
			}
		})
	}
}

func TestErrorStatusFromKind(t *testing.T) {
	cases := map[string]int{
		"overloaded_error":       529,
		"rate_limit_exceeded":    429,
		"insufficient_quota":     402,
		"invalid_request_error":  400,
		"authentication_error":   401,
		"permission_error":       403,
		"api_error":              500,
		"something unrecognised": 0,
	}
	for kind, want := range cases {
		if got := errorStatus(kind); got != want {
			t.Errorf("errorStatus(%q) = %d, want %d", kind, got, want)
		}
	}
}

func TestInbandStatusReadsFirstEvent(t *testing.T) {
	if st, msg := inbandStatus([]byte(`data: {"id":"a1","choices":[]}` + "\n\n")); st != 0 || msg != "" {
		t.Errorf("a content event read as error: %d %q", st, msg)
	}
	st, msg := inbandStatus([]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"))
	if st != 529 || msg != "Overloaded" {
		t.Errorf("overloaded event read as %d %q, want 529 Overloaded", st, msg)
	}
}
