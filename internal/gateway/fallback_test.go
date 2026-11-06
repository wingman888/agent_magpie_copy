package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// scripted is an upstream that answers every request the same way and
// records what it was sent.
type scripted struct {
	mu     sync.Mutex
	code   int
	ctype  string
	body   string
	abort  bool // after writing body, drop the connection (a stream cut short)
	calls  int
	models []string
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.calls++
	s.models = append(s.models, modelOf(b))
	code, ctype, body, abort := s.code, s.ctype, s.body, s.abort
	s.mu.Unlock()
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(code)
	io.WriteString(w, body)
	if abort {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic(http.ErrAbortHandler)
	}
}

func (s *scripted) hits() (int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, append([]string(nil), s.models...)
}

const fallbackOKChat = `{"id":"x","object":"chat.completion","model":"m2","choices":[{"index":0,"message":{"role":"assistant","content":"from B"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// fallbackServer saves provider a (with the given fallback list) and provider
// b, each backed by its own scripted upstream, and returns a fresh gateway.
func fallbackServer(t *testing.T, a, b *scripted, fallback ...string) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	upA := httptest.NewServer(a)
	t.Cleanup(upA.Close)
	upB := httptest.NewServer(b)
	t.Cleanup(upB.Close)
	if err := provider.Save(provider.Provider{ID: "b", Name: "Bravo", Chat: upB.URL + "/v1", Key: "k", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "a", Name: "Alpha", Chat: upA.URL + "/v1", Key: "k", Models: []string{"m1"}, Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
	return New()
}

// chatCall sends one Chat Completions request for model through the gateway.
func chatCall(t *testing.T, srv *Server, model string, stream bool) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"model": model, "stream": stream,
		"messages": []map[string]string{{"role": "user", "content": "hi"}}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body)))
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func okB() *scripted {
	return &scripted{code: 200, ctype: "application/json", body: fallbackOKChat}
}

func TestFallbackSwitchesOnRetryableErrors(t *testing.T) {
	cases := []struct {
		name   string
		code   int
		body   string
		hasMsg bool
	}{
		{"429", 429, `{"error":{"message":"rate limited"}}`, true},
		{"402", 402, `{"error":{"message":"payment required"}}`, true},
		{"529", 529, `{"error":{"message":"overloaded"}}`, true},
		{"500", 500, `{"error":{"message":"boom"}}`, true},
		{"400 quota zh", 400, `{"error":{"message":"额度已用完"}}`, true},
		{"403 insufficient balance", 403, `{"error":{"message":"Insufficient balance"}}`, true},
		{"400 too many requests, mixed case", 400, `{"error":{"message":"Too Many Requests"}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &scripted{code: tc.code, ctype: "application/json", body: tc.body}
			b := okB()
			srv := fallbackServer(t, a, b, "b/m2")
			rec := chatCall(t, srv, "a/m1", false)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
				t.Fatalf("status %d body %s, want B's reply", rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "rate limited") || strings.Contains(rec.Body.String(), "额度") {
				t.Fatalf("A's error leaked into the reply: %s", rec.Body)
			}
			if n, models := b.hits(); n != 1 || models[0] != "m2" {
				t.Fatalf("B got %d requests with models %v, want one with m2", n, models)
			}
			if n, _ := a.hits(); n != 1 {
				t.Fatalf("A got %d requests, want 1", n)
			}
			c := srv.Recent()[0]
			if c.Provider != "b" || !strings.HasPrefix(c.Fallback, "a: ") {
				t.Fatalf("record provider %q fallback %q", c.Provider, c.Fallback)
			}
		})
	}
}

func TestPlainBadRequestDoesNotSwitch(t *testing.T) {
	a := &scripted{code: 400, ctype: "application/json", body: `{"error":{"message":"messages: field required"}}`}
	b := okB()
	srv := fallbackServer(t, a, b, "b/m2")
	rec := chatCall(t, srv, "a/m1", false)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "messages: field required") {
		t.Fatalf("status %d body %s, want A's 400", rec.Code, rec.Body)
	}
	if n, _ := b.hits(); n != 0 {
		t.Fatalf("B got %d requests, want none", n)
	}
	if f := srv.Recent()[0].Fallback; f != "" {
		t.Fatalf("fallback record %q, want empty", f)
	}
}

func TestAllFailReturnsLastError(t *testing.T) {
	a := &scripted{code: 429, ctype: "application/json", body: `{"error":{"message":"rate limited"}}`}
	b := &scripted{code: 503, ctype: "application/json", body: `{"error":{"message":"b is down"}}`}
	srv := fallbackServer(t, a, b, "b/m2")
	rec := chatCall(t, srv, "a/m1", false)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "b is down") {
		t.Fatalf("status %d body %s, want B's 503", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "rate limited") {
		t.Fatalf("A's error should not be the reply: %s", rec.Body)
	}
}

func TestFailedProviderRestsForAMinute(t *testing.T) {
	a := &scripted{code: 429, ctype: "application/json", body: `{"error":{"message":"rate limited"}}`}
	b := okB()
	srv := fallbackServer(t, a, b, "b/m2")
	if rec := chatCall(t, srv, "a/m1", false); rec.Code != 200 {
		t.Fatalf("first request status %d", rec.Code)
	}
	rec := chatCall(t, srv, "a/m1", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("second request status %d body %s, want B", rec.Code, rec.Body)
	}
	if n, _ := a.hits(); n != 1 {
		t.Fatalf("A got %d requests, want 1: it should rest after the first failure", n)
	}
	c := srv.Recent()[0]
	if c.Provider != "b" || !strings.HasPrefix(c.Fallback, "a: ") {
		t.Fatalf("record provider %q fallback %q", c.Provider, c.Fallback)
	}
}

func TestUnknownAndRepeatedFallbacksAreSkipped(t *testing.T) {
	a := &scripted{code: 429, ctype: "application/json", body: `{"error":{"message":"rate limited"}}`}
	b := okB()
	srv := fallbackServer(t, a, b, "nowhere/x", "b/m2", "b/m2", "b/m2")
	rec := chatCall(t, srv, "a/m1", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("status %d body %s, want B", rec.Code, rec.Body)
	}
	if n, _ := b.hits(); n != 1 {
		t.Fatalf("B got %d requests, want 1", n)
	}
}

func TestNoSwitchOnceStreamHasStarted(t *testing.T) {
	a := &scripted{code: 200, ctype: "text/event-stream", abort: true,
		body: "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"from A\"}}]}\n\n"}
	b := okB()
	srv := fallbackServer(t, a, b, "b/m2")
	rec := chatCall(t, srv, "a/m1", true)
	if !strings.Contains(rec.Body.String(), "from A") {
		t.Fatalf("A's first chunk missing: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("B's reply mixed into A's stream: %s", rec.Body)
	}
	if n, _ := b.hits(); n != 0 {
		t.Fatalf("B got %d requests, want none after A started", n)
	}
	if f := srv.Recent()[0].Fallback; f != "" {
		t.Fatalf("fallback record %q, want empty", f)
	}
}

func TestNoFallbackRecordWithoutSwitch(t *testing.T) {
	a := okB()
	b := okB()
	srv := fallbackServer(t, a, b, "b/m2")
	if rec := chatCall(t, srv, "a/m1", false); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	c := srv.Recent()[0]
	if c.Provider != "a" || c.Fallback != "" {
		t.Fatalf("record provider %q fallback %q, want a and none", c.Provider, c.Fallback)
	}
	b2, _ := json.Marshal(c)
	if strings.Contains(string(b2), `"fallback"`) {
		t.Fatalf("fallback key should be omitted: %s", b2)
	}
}

func TestProviderWithoutFallbackUnchanged(t *testing.T) {
	a := &scripted{code: 429, ctype: "application/json", body: `{"error":{"message":"rate limited"}}`}
	b := okB()
	srv := fallbackServer(t, a, b)
	rec := chatCall(t, srv, "a/m1", false)
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "rate limited") {
		t.Fatalf("status %d body %s, want A's 429", rec.Code, rec.Body)
	}
	if n, _ := b.hits(); n != 0 {
		t.Fatalf("B got %d requests, want none", n)
	}
}
