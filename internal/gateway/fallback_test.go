package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// scripted is an upstream that records the model of each request and replies
// with a fixed error, a fixed success, or a stream cut off after one chunk.
type scripted struct {
	mu     sync.Mutex
	status int    // 0 means a success reply
	errMsg string // error body, as the upstream sends it
	ok     string // text of a success reply
	stream bool   // send one SSE chunk, then abort the connection
	calls  []string
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.calls = append(s.calls, modelOf(body))
	status, errMsg, ok, stream := s.status, s.errMsg, s.ok, s.stream
	s.mu.Unlock()
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"content":"from A"},"finish_reason":null}]}`+"\n\n")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
		io.WriteString(w, errMsg)
		return
	}
	fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		modelOf(body), ok)
}

func (s *scripted) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// fallbackSetup saves provider a (model m1, with the given fallback list)
// backed by upstream a, and provider b (model m2) backed by upstream b.
func fallbackSetup(t *testing.T, a, b *scripted, fallback ...string) {
	t.Helper()
	fallbackSetupNamed(t, "A", a, b, fallback...)
}

// fallbackSetupNamed is fallbackSetup with a chosen display name for provider a.
func fallbackSetupNamed(t *testing.T, name string, a, b *scripted, fallback ...string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ua := httptest.NewServer(a)
	t.Cleanup(ua.Close)
	ub := httptest.NewServer(b)
	t.Cleanup(ub.Close)
	if err := provider.Save(provider.Provider{ID: "a", Name: name, Key: "k", Models: []string{"m1"},
		Chat: ua.URL + "/v1", Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "k", Models: []string{"m2"},
		Chat: ub.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

// chatVia sends a Chat Completions request for model through srv.
func chatVia(t *testing.T, srv *Server, model string, stream bool) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"model":%q,"stream":%v,"messages":[{"role":"user","content":"hi"}]}`, model, stream)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec
}

func TestFallbackSwitchesOnRetryableErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		msg    string
		hidden string // text of the failed reply, which must not reach the client
	}{
		{"429", 429, `{"error":{"message":"rate limited"}}`, "rate limited"},
		{"402", 402, `{"error":{"message":"payment needed"}}`, "payment needed"},
		{"529", 529, `{"error":{"message":"overloaded"}}`, "overloaded"},
		{"500", 500, `{"error":{"message":"boom"}}`, "boom"},
		{"400 quota words", 400, `{"error":{"message":"额度已用完"}}`, "额度已用完"},
		{"403 insufficient balance", 403, `{"error":{"message":"Insufficient balance"}}`, "Insufficient balance"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &scripted{status: c.status, errMsg: c.msg}
			b := &scripted{ok: "from B"}
			fallbackSetup(t, a, b, "b/m2")
			srv := New()
			rec := chatVia(t, srv, "a/m1", false)
			if rec.Code != 200 {
				t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "from B") || strings.Contains(rec.Body.String(), c.hidden) {
				t.Fatalf("client got %s", rec.Body.String())
			}
			if got := a.seen(); len(got) != 1 || got[0] != "m1" {
				t.Fatalf("A saw %q", got)
			}
			if got := b.seen(); len(got) != 1 || got[0] != "m2" {
				t.Fatalf("B saw %q, want its own model name m2", got)
			}
			last := srv.Recent()[0]
			if last.Provider != "b" || !strings.HasPrefix(last.Fallback, "a: ") {
				t.Fatalf("record provider %q fallback %q", last.Provider, last.Fallback)
			}
		})
	}
}

// The retry decision reads the provider's own error body, not the gateway's
// wrapping, so a quota error type with an unrelated message still switches.
func TestFallbackJudgesProviderErrorBody(t *testing.T) {
	a := &scripted{status: 400, errMsg: `{"error":{"type":"insufficient_quota","message":"request failed"}}`}
	b := &scripted{ok: "from B"}
	fallbackSetup(t, a, b, "b/m2")
	rec := chatVia(t, New(), "a/m1", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if got := b.seen(); len(got) != 1 {
		t.Fatalf("B saw %q, want one request", got)
	}
}

// A provider name containing a quota word must not make a plain 400 switch.
func TestFallbackIgnoresProviderNameInJudgement(t *testing.T) {
	a := &scripted{status: 400, errMsg: `{"error":{"message":"messages: field required"}}`}
	b := &scripted{ok: "from B"}
	fallbackSetupNamed(t, "Credit Relay", a, b, "b/m2")
	rec := chatVia(t, New(), "a/m1", false)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "messages: field required") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if got := b.seen(); len(got) != 0 {
		t.Fatalf("B was contacted for a plain 400: %q", got)
	}
}

// A fallback entry must name the provider it resolves to. A bare model name
// resolves to provider b, but must not send the request there.
func TestFallbackSkipsEntryResolvingToOtherProvider(t *testing.T) {
	a := &scripted{status: 429, errMsg: `{"error":{"message":"rate limited"}}`}
	b := &scripted{ok: "from B"}
	fallbackSetup(t, a, b, "m2")
	rec := chatVia(t, New(), "a/m1", false)
	if rec.Code != 429 {
		t.Fatalf("got %d %s, want the 429 of A", rec.Code, rec.Body.String())
	}
	if got := b.seen(); len(got) != 0 {
		t.Fatalf("B was contacted through a mismatched entry: %q", got)
	}
}

func TestFallbackCooldownSkipsFailedProvider(t *testing.T) {
	a := &scripted{status: 429, errMsg: `{"error":{"message":"rate limited"}}`}
	b := &scripted{ok: "from B"}
	fallbackSetup(t, a, b, "b/m2")
	srv := New()
	if rec := chatVia(t, srv, "a/m1", false); rec.Code != 200 {
		t.Fatalf("first request: %d %s", rec.Code, rec.Body.String())
	}
	rec := chatVia(t, srv, "a/m1", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("second request: %d %s", rec.Code, rec.Body.String())
	}
	if got := a.seen(); len(got) != 1 {
		t.Fatalf("A was contacted again during cooldown: %q", got)
	}
	if got := b.seen(); len(got) != 2 {
		t.Fatalf("B saw %q", got)
	}
}

func TestFallbackDoesNotSwitchOnBadRequest(t *testing.T) {
	a := &scripted{status: 400, errMsg: `{"error":{"message":"messages: field required"}}`}
	b := &scripted{ok: "from B"}
	fallbackSetup(t, a, b, "b/m2")
	rec := chatVia(t, New(), "a/m1", false)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "messages: field required") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if got := b.seen(); len(got) != 0 {
		t.Fatalf("B was contacted: %q", got)
	}
}

func TestFallbackAllFailReturnsLastError(t *testing.T) {
	a := &scripted{status: 429, errMsg: `{"error":{"message":"slow down"}}`}
	b := &scripted{status: 503, errMsg: `{"error":{"message":"upstream down"}}`}
	fallbackSetup(t, a, b, "b/m2")
	srv := New()
	rec := chatVia(t, srv, "a/m1", false)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "upstream down") || strings.Contains(rec.Body.String(), "slow down") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if last := srv.Recent()[0]; last.Provider != "b" || !strings.HasPrefix(last.Fallback, "a: ") {
		t.Fatalf("record provider %q fallback %q", last.Provider, last.Fallback)
	}
}

func TestFallbackSkipsUnknownAndRepeatedEntries(t *testing.T) {
	a := &scripted{status: 429, errMsg: `{"error":{"message":"rate limited"}}`}
	b := &scripted{ok: "from B"}
	fallbackSetup(t, a, b, "nowhere/x", "b/m2", "b/m2", "a/m1")
	rec := chatVia(t, New(), "a/m1", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if got := b.seen(); len(got) != 1 {
		t.Fatalf("B saw %q, want exactly one request", got)
	}
}

func TestFallbackNotAfterStreamStarted(t *testing.T) {
	a := &scripted{stream: true}
	b := &scripted{ok: "from B"}
	fallbackSetup(t, a, b, "b/m2")
	rec := chatVia(t, New(), "a/m1", true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from A") || strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if got := b.seen(); len(got) != 0 {
		t.Fatalf("B was contacted after A streamed: %q", got)
	}
}

func TestFallbackStopsWhenClientGone(t *testing.T) {
	a := &scripted{status: 429, errMsg: `{"error":{"message":"rate limited"}}`}
	b := &scripted{ok: "from B"}
	fallbackSetup(t, a, b, "b/m2")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := `{"model":"a/m1","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
	New().Handler().ServeHTTP(rec, req)
	if got := b.seen(); len(got) != 0 {
		t.Fatalf("B was contacted for a gone client: %q", got)
	}
}

func TestNoFallbackKeepsErrorAndRecord(t *testing.T) {
	a := &scripted{status: 429, errMsg: `{"error":{"message":"rate limited"}}`}
	b := &scripted{ok: "from B"}
	fallbackSetup(t, a, b)
	srv := New()
	rec := chatVia(t, srv, "a/m1", false)
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "rate limited") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if got := b.seen(); len(got) != 0 {
		t.Fatalf("B was contacted without a fallback: %q", got)
	}
	last := srv.Recent()[0]
	if last.Fallback != "" {
		t.Fatalf("fallback set without a switch: %q", last.Fallback)
	}
	raw, _ := json.Marshal(last)
	if strings.Contains(string(raw), `"fallback"`) {
		t.Fatalf("fallback key present in record: %s", raw)
	}
}
