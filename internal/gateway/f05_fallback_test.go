package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// f05Upstream is a fake provider that answers every request with one fixed
// status and body, and counts what it was sent.
type f05Upstream struct {
	status int
	ctype  string
	body   string

	mu    sync.Mutex
	calls int
	model string
}

func (u *f05Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.calls++
	u.model = modelOf(raw)
	u.mu.Unlock()
	w.Header().Set("Content-Type", u.ctype)
	w.WriteHeader(u.status)
	io.WriteString(w, u.body)
}

func (u *f05Upstream) hits() (int, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls, u.model
}

const f05OKB = `{"id":"b1","object":"chat.completion","model":"m2","choices":[{"index":0,"message":{"role":"assistant","content":"from B"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

// f05Pair starts two fake upstreams and saves provider f05a (the main one,
// with the given fallback list) and f05b (the backup), both speaking Chat.
func f05Pair(t *testing.T, a, b *f05Upstream, fallback ...string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ua := httptest.NewServer(a)
	t.Cleanup(ua.Close)
	ub := httptest.NewServer(b)
	t.Cleanup(ub.Close)
	if err := provider.Save(provider.Provider{ID: "f05b", Name: "F05 B", Key: "k", Models: []string{"m2"},
		Chat: ub.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "f05a", Name: "F05 A", Key: "k", Models: []string{"m1"},
		Chat: ua.URL + "/v1", Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
}

// f05Send posts a Chat Completions request for f05a/m1 to the gateway.
func f05Send(srv *Server, body string) *httptest.ResponseRecorder {
	return f05SendCtx(context.Background(), srv, body)
}

func f05SendCtx(ctx context.Context, srv *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

const f05PlainBody = `{"model":"f05a/m1","messages":[{"role":"user","content":"hi"}]}`
const f05StreamBody = `{"model":"f05a/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func TestF05FallbackSwitchesOnRetryableStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"429", 429, `{"error":{"message":"rate limited"}}`},
		{"402", 402, `{"error":{"message":"payment required"}}`},
		{"408", 408, `{"error":{"message":"timeout"}}`},
		{"503", 503, `{"error":{"message":"unavailable"}}`},
		{"529", 529, `{"error":{"message":"overloaded"}}`},
		{"400 quota words", 400, `{"error":{"message":"Insufficient BALANCE, quota exceeded"}}`},
		{"403 Chinese quota words", 403, `{"error":{"message":"套餐额度已用完"}}`},
		{"401 rate limit words", 401, `{"error":{"message":"Too Many Requests"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &f05Upstream{status: c.status, ctype: "application/json", body: c.body}
			b := &f05Upstream{status: 200, ctype: "application/json", body: f05OKB}
			f05Pair(t, a, b, "f05b/m2")
			srv := New()
			rec := f05Send(srv, f05PlainBody)
			if rec.Code != 200 {
				t.Fatalf("status = %d, want 200 from the backup; body %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "from B") || strings.Contains(rec.Body.String(), "rate limited") {
				t.Fatalf("agent should see only B's reply: %s", rec.Body.String())
			}
			if n, model := b.hits(); n != 1 || model != "m2" {
				t.Fatalf("backup got %d requests for model %q, want 1 for m2", n, model)
			}
			last := srv.Recent()[0]
			if last.Provider != "f05b" || !strings.HasPrefix(last.Fallback, "f05a: ") {
				t.Fatalf("record provider=%q fallback=%q", last.Provider, last.Fallback)
			}
		})
	}
}

func TestF05FallbackPlainBadRequestIsNotSwitched(t *testing.T) {
	a := &f05Upstream{status: 400, ctype: "application/json", body: `{"error":{"message":"messages: field required"}}`}
	b := &f05Upstream{status: 200, ctype: "application/json", body: f05OKB}
	f05Pair(t, a, b, "f05b/m2")
	srv := New()
	rec := f05Send(srv, f05PlainBody)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "field required") {
		t.Fatalf("got %d %s, want the original 400", rec.Code, rec.Body.String())
	}
	if n, _ := b.hits(); n != 0 {
		t.Fatalf("backup was called %d times for a non-retryable error", n)
	}
	if last := srv.Recent()[0]; last.Provider != "f05a" || last.Fallback != "" {
		t.Fatalf("record provider=%q fallback=%q", last.Provider, last.Fallback)
	}
}

func TestF05FallbackBothFailReturnsLastError(t *testing.T) {
	a := &f05Upstream{status: 429, ctype: "application/json", body: `{"error":{"message":"slow down"}}`}
	b := &f05Upstream{status: 503, ctype: "application/json", body: `{"error":{"message":"B is down"}}`}
	f05Pair(t, a, b, "f05b/m2")
	srv := New()
	rec := f05Send(srv, f05PlainBody)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "B is down") {
		t.Fatalf("got %d %s, want B's 503", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "slow down") {
		t.Fatalf("the first failure should not reach the agent: %s", rec.Body.String())
	}
	if last := srv.Recent()[0]; last.Provider != "f05b" || !strings.HasPrefix(last.Fallback, "f05a: ") {
		t.Fatalf("record provider=%q fallback=%q", last.Provider, last.Fallback)
	}
}

func TestF05FallbackRestingProviderIsTriedLast(t *testing.T) {
	a := &f05Upstream{status: 429, ctype: "application/json", body: `{"error":{"message":"slow down"}}`}
	b := &f05Upstream{status: 200, ctype: "application/json", body: f05OKB}
	f05Pair(t, a, b, "f05b/m2")
	srv := New()
	if rec := f05Send(srv, f05PlainBody); rec.Code != 200 {
		t.Fatalf("first request: %d %s", rec.Code, rec.Body.String())
	}
	rec := f05Send(srv, f05PlainBody)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("second request: %d %s", rec.Code, rec.Body.String())
	}
	if n, _ := a.hits(); n != 1 {
		t.Fatalf("resting provider was tried %d times, want 1", n)
	}
	if n, _ := b.hits(); n != 2 {
		t.Fatalf("backup was called %d times, want 2", n)
	}
	if last := srv.Recent()[0]; last.Provider != "f05b" || !strings.HasPrefix(last.Fallback, "f05a: ") {
		t.Fatalf("record provider=%q fallback=%q", last.Provider, last.Fallback)
	}
}

func TestF05FallbackSkipsUnknownAndRepeatedEntries(t *testing.T) {
	a := &f05Upstream{status: 429, ctype: "application/json", body: `{"error":{"message":"slow down"}}`}
	b := &f05Upstream{status: 200, ctype: "application/json", body: f05OKB}
	f05Pair(t, a, b, "nowhere/x", "f05b/m2", "f05b/m2", "f05a/m1")
	srv := New()
	rec := f05Send(srv, f05PlainBody)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n, _ := b.hits(); n != 1 {
		t.Fatalf("backup was called %d times, want 1", n)
	}
}

func TestF05FallbackStopsOnceStreamingStarted(t *testing.T) {
	a := &f05Upstream{status: 200, ctype: "text/event-stream",
		body: "data: {\"id\":\"a\",\"choices\":[{\"delta\":{\"content\":\"partial A\"}}]}\n\n" +
			"data: {\"error\":{\"message\":\"connection reset\"}}\n\n"}
	b := &f05Upstream{status: 200, ctype: "text/event-stream",
		body: "data: {\"id\":\"b\",\"choices\":[{\"delta\":{\"content\":\"from B\"}}]}\n\ndata: [DONE]\n\n"}
	f05Pair(t, a, b, "f05b/m2")
	srv := New()
	rec := f05Send(srv, f05StreamBody)
	if !strings.Contains(rec.Body.String(), "partial A") {
		t.Fatalf("agent lost the started reply: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("backup content was mixed into a started reply: %s", rec.Body.String())
	}
	if n, _ := b.hits(); n != 0 {
		t.Fatalf("backup was called %d times after output started", n)
	}
}

func TestF05FallbackStreamSwitchesBeforeFirstByte(t *testing.T) {
	a := &f05Upstream{status: 429, ctype: "application/json", body: `{"error":{"message":"slow down"}}`}
	b := &f05Upstream{status: 200, ctype: "text/event-stream",
		body: "data: {\"id\":\"b\",\"choices\":[{\"delta\":{\"content\":\"from B\"}}]}\n\ndata: [DONE]\n\n"}
	f05Pair(t, a, b, "f05b/m2")
	srv := New()
	rec := f05Send(srv, f05StreamBody)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") || strings.Contains(rec.Body.String(), "slow down") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestF05FallbackAnthropicClientGetsBackupReply(t *testing.T) {
	a := &f05Upstream{status: 429, ctype: "application/json", body: `{"error":{"message":"slow down"}}`}
	b := &f05Upstream{status: 200, ctype: "text/event-stream",
		body: "data: {\"id\":\"b\",\"choices\":[{\"delta\":{\"content\":\"from B\"}}]}\n\ndata: [DONE]\n\n"}
	f05Pair(t, a, b, "f05b/m2")
	srv := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"f05a/m1","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if n, model := b.hits(); n != 1 || model != "m2" {
		t.Fatalf("backup got %d requests for model %q", n, model)
	}
}

func TestF05FallbackStopsWhenClientIsGone(t *testing.T) {
	a := &f05Upstream{status: 429, ctype: "application/json", body: `{"error":{"message":"slow down"}}`}
	b := &f05Upstream{status: 200, ctype: "application/json", body: f05OKB}
	f05Pair(t, a, b, "f05b/m2")
	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f05SendCtx(ctx, srv, f05PlainBody)
	if n, _ := b.hits(); n != 0 {
		t.Fatalf("backup was called %d times after the client went away", n)
	}
}

func TestF05FallbackAbsentWithoutBackups(t *testing.T) {
	a := &f05Upstream{status: 429, ctype: "application/json", body: `{"error":{"message":"slow down"}}`}
	b := &f05Upstream{status: 200, ctype: "application/json", body: f05OKB}
	f05Pair(t, a, b)
	srv := New()
	rec := f05Send(srv, f05PlainBody)
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "slow down") {
		t.Fatalf("got %d %s, want the original 429", rec.Code, rec.Body.String())
	}
	if n, _ := b.hits(); n != 0 {
		t.Fatalf("unrelated provider was called %d times", n)
	}
	last := srv.Recent()[0]
	if last.Fallback != "" {
		t.Fatalf("fallback = %q, want empty", last.Fallback)
	}
	out, _ := json.Marshal(last)
	if strings.Contains(string(out), `"fallback"`) {
		t.Fatalf("record carries a fallback key without a switch: %s", out)
	}
}
