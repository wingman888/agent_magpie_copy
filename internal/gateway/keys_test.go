package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const okChat = `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`

// byKey is an upstream that answers by API key: a key listed in code gets that
// status, any other a normal reply. It records the keys it was sent.
type byKey struct {
	mu   sync.Mutex
	code map[string]int
	seen []string
}

func (b *byKey) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	b.mu.Lock()
	b.seen = append(b.seen, key)
	code := b.code[key]
	b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
		return
	}
	io.WriteString(w, okChat)
}

// took returns the keys seen since the last call, and forgets them.
func (b *byKey) took() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.seen
	b.seen = nil
	return out
}

// keyedRelay points a custom provider, current key k1, at the upstream.
func keyedRelay(t *testing.T, b *byKey) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", Models: []string{"m1"},
		Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

// chatVia sends one chat request through the handler and returns the status.
func chatVia(h http.Handler) int {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`))
	h.ServeHTTP(rec, req)
	return rec.Code
}

func join(xs []string) string { return strings.Join(xs, ",") }

// A limited first key is retried with the second within the same request; the
// next request goes straight to the second, while the first rests.
func TestKeyFailoverWithinRequestAndRest(t *testing.T) {
	b := &byKey{code: map[string]int{"k1": 429}}
	keyedRelay(t, b)
	if err := provider.AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	srv := New()
	h := srv.Handler()

	if code := chatVia(h); code != 200 {
		t.Fatalf("status %d, want the second key's 200", code)
	}
	if got := join(b.took()); got != "k1,k2" {
		t.Fatalf("first request sent keys %s, want k1,k2", got)
	}
	if rec := srv.Recent(); len(rec) == 0 || rec[0].Provider != "relay" || rec[0].Status != 200 {
		t.Fatalf("call record = %+v", rec)
	}

	if code := chatVia(h); code != 200 {
		t.Fatalf("second status %d", code)
	}
	if got := join(b.took()); got != "k2" {
		t.Fatalf("second request sent keys %s, want k2 only", got)
	}
}

// A key that is off is never sent, not even after a failure.
func TestDisabledKeyNeverTried(t *testing.T) {
	b := &byKey{code: map[string]int{"k1": 429}}
	keyedRelay(t, b)
	if err := provider.AddKey("relay", "two", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "three", "k3", ""); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyOn("relay", provider.KeyRef("k2"), false); err != nil {
		t.Fatal(err)
	}
	if code := chatVia(New().Handler()); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got := join(b.took()); got != "k1,k3" {
		t.Fatalf("sent keys %s, want k1,k3", got)
	}
}

// When every key fails, the client gets the last one's error.
func TestLastKeyErrorReachesClient(t *testing.T) {
	b := &byKey{code: map[string]int{"k1": 429, "k2": 429}}
	keyedRelay(t, b)
	if err := provider.AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if code := chatVia(New().Handler()); code != 429 {
		t.Fatalf("status %d, want the last key's 429", code)
	}
	if got := join(b.took()); got != "k1,k2" {
		t.Fatalf("sent keys %s, want k1,k2", got)
	}
}

// A bad request is the client's error and is not retried with another key.
func TestBadRequestNotRetriedWithOtherKey(t *testing.T) {
	b := &byKey{code: map[string]int{"k1": 400}}
	keyedRelay(t, b)
	if err := provider.AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if code := chatVia(New().Handler()); code != 400 {
		t.Fatalf("status %d, want 400 passed through", code)
	}
	if got := join(b.took()); got != "k1" {
		t.Fatalf("sent keys %s, want k1 only", got)
	}
}

func TestRetryableStatuses(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   bool
	}{
		{429, "", true},
		{402, "", true},
		{529, "", true},
		{503, "", true},
		{403, "quota exceeded for this key", true},
		{403, "insufficient balance", true},
		{403, "invalid api key", false},
		{400, "bad request: quota", false},
		{401, "", false},
		{500, "", false},
	}
	for _, c := range cases {
		if got := retryable(c.status, []byte(c.body)); got != c.want {
			t.Errorf("retryable(%d, %q) = %v, want %v", c.status, c.body, got, c.want)
		}
	}
}
