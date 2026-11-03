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

// upstream is a scripted provider. It counts the requests it gets, notes the
// model each one asked for, and answers with a fixed status and body.
type upstream struct {
	mu     sync.Mutex
	status int    // 0 means 200
	body   string // the reply body
	abort  bool   // send body as an event stream, then drop the connection
	onHit  func() // runs when a request arrives, before the reply
	hits   int
	models []string
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.hits++
	u.models = append(u.models, modelOf(raw))
	onHit := u.onHit
	u.mu.Unlock()
	if onHit != nil {
		onHit()
	}
	if u.abort {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, u.body)
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // cut the connection mid-reply
	}
	w.Header().Set("Content-Type", "application/json")
	if u.status != 0 {
		w.WriteHeader(u.status)
	}
	io.WriteString(w, u.body)
}

func (u *upstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

func (u *upstream) lastModel() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.models) == 0 {
		return ""
	}
	return u.models[len(u.models)-1]
}

const (
	fbChatOK      = `{"id":"b1","object":"chat.completion","model":"m2","choices":[{"index":0,"message":{"role":"assistant","content":"from B"},"finish_reason":"stop"}]}`
	fbRateLimited = `{"error":{"message":"rate limited"}}`
)

// pair starts upstreams for providers A and B and saves both. A's fallbacks
// are as given. It returns a fresh gateway, so each test starts with an empty
// resting table.
func pair(t *testing.T, a, b *upstream, fallback ...string) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ua := httptest.NewServer(a)
	t.Cleanup(ua.Close)
	ub := httptest.NewServer(b)
	t.Cleanup(ub.Close)
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Models: []string{"m1"},
		Chat: ua.URL + "/v1", Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "k", Models: []string{"m2"},
		Chat: ub.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	return New()
}

// chat sends one Chat Completions request through the gateway.
func chat(t *testing.T, s *Server, model string, stream bool) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, model, stream)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestFallbackSwitchesOnRetryableFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"rate limited 429", 429, fbRateLimited},
		{"payment required 402", 402, `{"error":{"message":"payment required"}}`},
		{"overloaded 529", 529, `{"error":{"message":"overloaded"}}`},
		{"server error 500", 500, `{"error":{"message":"boom"}}`},
		{"request timeout 408", 408, `{"error":{"message":"timeout"}}`},
		{"quota text on 400", 400, `{"error":{"message":"You exceeded your quota"}}`},
		{"chinese quota text on 400", 400, `{"error":{"message":"额度已用完"}}`},
		{"insufficient balance on 403 with mixed case", 403, `{"error":{"message":"Insufficient Balance"}}`},
		{"too many requests on 401 with mixed case", 401, `{"error":{"message":"Too many requests"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &upstream{status: c.status, body: c.body}
			b := &upstream{body: fbChatOK}
			s := pair(t, a, b, "b/m2")
			code, body := chat(t, s, "a/m1", false)
			if code != 200 || !strings.Contains(body, "from B") {
				t.Fatalf("status %d, body %s; want B's reply", code, body)
			}
			if strings.Contains(body, "error") {
				t.Errorf("reply carries A's error: %s", body)
			}
			if a.count() != 1 || b.count() != 1 {
				t.Fatalf("hits: A=%d B=%d, want 1 and 1", a.count(), b.count())
			}
			if b.lastModel() != "m2" {
				t.Errorf("B got model %q, want its own m2", b.lastModel())
			}
			rec := s.Recent()[0]
			if rec.Provider != "b" || !strings.HasPrefix(rec.Fallback, "a: ") {
				t.Errorf("record provider %q fallback %q", rec.Provider, rec.Fallback)
			}
		})
	}
}

func TestBadRequestIsNotSwitched(t *testing.T) {
	a := &upstream{status: 400, body: `{"error":{"message":"messages: field required"}}`}
	b := &upstream{body: fbChatOK}
	s := pair(t, a, b, "b/m2")
	code, body := chat(t, s, "a/m1", false)
	if code != 400 || !strings.Contains(body, "messages: field required") {
		t.Fatalf("status %d, body %s; want A's 400 as it was", code, body)
	}
	if b.count() != 0 {
		t.Errorf("B got %d requests, want none", b.count())
	}
	if rec := s.Recent()[0]; rec.Fallback != "" || rec.Provider != "a" {
		t.Errorf("record provider %q fallback %q", rec.Provider, rec.Fallback)
	}
}

func TestNotFoundIsNotSwitched(t *testing.T) {
	a := &upstream{status: 404, body: `{"error":{"message":"no such route"}}`}
	b := &upstream{body: fbChatOK}
	s := pair(t, a, b, "b/m2")
	code, body := chat(t, s, "a/m1", false)
	if code != 404 || !strings.Contains(body, "no such route") || b.count() != 0 {
		t.Fatalf("status %d, body %s, B hits %d; want A's 404 and no B request", code, body, b.count())
	}
}

func TestAllCandidatesFailReturnsLastAttempt(t *testing.T) {
	a := &upstream{status: 429, body: fbRateLimited}
	b := &upstream{status: 503, body: `{"error":{"message":"B is down"}}`}
	s := pair(t, a, b, "b/m2")
	code, body := chat(t, s, "a/m1", false)
	if code != 503 || !strings.Contains(body, "B is down") {
		t.Fatalf("status %d, body %s; want B's 503 and body", code, body)
	}
	if a.count() != 1 || b.count() != 1 {
		t.Errorf("hits: A=%d B=%d, want 1 and 1", a.count(), b.count())
	}
	rec := s.Recent()[0]
	if rec.Provider != "b" || rec.Status != 503 || !strings.HasPrefix(rec.Fallback, "a: 429") {
		t.Errorf("record provider %q status %d fallback %q", rec.Provider, rec.Status, rec.Fallback)
	}
}

func TestRestingProviderIsNotTriedFirst(t *testing.T) {
	a := &upstream{status: 429, body: fbRateLimited}
	b := &upstream{body: fbChatOK}
	s := pair(t, a, b, "b/m2")

	if code, body := chat(t, s, "a/m1", false); code != 200 || !strings.Contains(body, "from B") {
		t.Fatalf("first request: status %d, body %s", code, body)
	}
	if code, body := chat(t, s, "a/m1", false); code != 200 || !strings.Contains(body, "from B") {
		t.Fatalf("second request: status %d, body %s", code, body)
	}
	if a.count() != 1 {
		t.Errorf("A got %d requests, want 1: the second request must skip it", a.count())
	}
	if b.count() != 2 {
		t.Errorf("B got %d requests, want 2", b.count())
	}
	recent := s.Recent()
	for i, rec := range recent[:2] {
		if rec.Provider != "b" || !strings.HasPrefix(rec.Fallback, "a: ") {
			t.Errorf("record %d: provider %q fallback %q", i, rec.Provider, rec.Fallback)
		}
	}
}

func TestStreamingNoSwitchAfterFirstChunk(t *testing.T) {
	a := &upstream{abort: true, body: "data: " + `{"id":"a1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial"}}]}` + "\n\n"}
	b := &upstream{body: fbChatOK}
	s := pair(t, a, b, "b/m2")
	code, body := chat(t, s, "a/m1", true)
	if code != 200 {
		t.Fatalf("status %d, body %s", code, body)
	}
	if !strings.Contains(body, "partial") {
		t.Errorf("client lost A's first chunk: %s", body)
	}
	if strings.Contains(body, "from B") {
		t.Errorf("B's reply was mixed into the stream: %s", body)
	}
	if b.count() != 0 {
		t.Errorf("B got %d requests, want none after A started streaming", b.count())
	}
}

func TestMissingAndRepeatedFallbacksAreSkipped(t *testing.T) {
	a := &upstream{status: 429, body: fbRateLimited}
	b := &upstream{body: fbChatOK}
	// "nowhere/x" names no provider; "m2" has no provider; "b/m2" repeats;
	// "a/m1" is the primary itself
	s := pair(t, a, b, "nowhere/x", "m2", "b/m2", "b/m2", "a/m1")
	code, body := chat(t, s, "a/m1", false)
	if code != 200 || !strings.Contains(body, "from B") {
		t.Fatalf("status %d, body %s; want B's reply", code, body)
	}
	if b.count() != 1 {
		t.Errorf("B got %d requests, want 1", b.count())
	}
}

func TestClientDisconnectStopsFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// the client goes away while A is answering
	a := &upstream{status: 429, body: fbRateLimited, onHit: cancel}
	b := &upstream{body: fbChatOK}
	s := pair(t, a, b, "b/m2")
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m1","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if b.count() != 0 {
		t.Errorf("B got %d requests after the client left, want none", b.count())
	}
}

func TestNoFallbackConfiguredIsUnchanged(t *testing.T) {
	a := &upstream{status: 429, body: fbRateLimited}
	b := &upstream{body: fbChatOK}
	s := pair(t, a, b)
	code, body := chat(t, s, "a/m1", false)
	if code != 429 || !strings.Contains(body, "rate limited") || b.count() != 0 {
		t.Fatalf("status %d, body %s, B hits %d; want A's 429 and no B request", code, body, b.count())
	}
	rec := s.Recent()[0]
	if rec.Provider != "a" || rec.Fallback != "" {
		t.Errorf("record provider %q fallback %q", rec.Provider, rec.Fallback)
	}
	js, _ := json.Marshal(rec)
	if strings.Contains(string(js), `"fallback"`) {
		t.Errorf("record carries a fallback key: %s", js)
	}
}

func TestSwitchable(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   bool
	}{
		{402, "", true},
		{408, "", true},
		{429, "", true},
		{500, "", true},
		{503, "", true},
		{599, "", true},
		{400, "messages: field required", false},
		{400, "Insufficient balance", true},
		{400, "Too Many Requests", true},
		{400, "用量已达上限", true},
		{400, "账户欠费", true},
		{401, "invalid api key", false},
		{401, "usage limit reached", true},
		{403, "forbidden", false},
		{403, "CREDIT exhausted", true},
		{404, "quota", false},
		{200, "", false},
	}
	for _, c := range cases {
		if got := switchable(c.status, []byte(c.body)); got != c.want {
			t.Errorf("switchable(%d, %q) = %v, want %v", c.status, c.body, got, c.want)
		}
	}
}
