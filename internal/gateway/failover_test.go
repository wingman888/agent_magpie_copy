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

// keyFake answers 429 to the keys in limited and a plain reply to the rest,
// and remembers which keys it was called with.
type keyFake struct {
	mu      sync.Mutex
	limited map[string]bool
	seen    []string
}

func (f *keyFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	f.seen = append(f.seen, key)
	f.mu.Unlock()
	if f.limited[key] {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"rate limit reached"}}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
}

func TestKeyFailoverUsesNextKeyAndRemembersIt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &keyFake{limited: map[string]bool{"k1": true}}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: up.URL + "/v1", Models: []string{"m1"},
		Key: "k1", KeyName: "first",
		Keys: []provider.KeyEntry{{Name: "second", Key: "k2"}, {Name: "retired", Key: "k3", Off: true}}}); err != nil {
		t.Fatal(err)
	}

	srv := New()
	h := srv.Handler()
	body := `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: status %d, body %s", rec.Code, rec.Body.String())
	}
	calls := srv.Recent()
	if len(calls) == 0 {
		t.Fatal("no call recorded")
	}
	if c := calls[0]; c.Provider != "fake" || c.Status != http.StatusOK || c.Fallback != "fake (first)" {
		t.Fatalf("call record: provider=%q status=%d fallback=%q", c.Provider, c.Status, c.Fallback)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("second request: status %d", rec.Code)
	}
	if c := srv.Recent()[0]; c.Fallback != "" {
		t.Fatalf("second request should use the second key directly, fallback=%q", c.Fallback)
	}
	for _, k := range f.seen[2:] {
		if k != "k2" {
			t.Fatalf("second request used %q, want k2 (seen %v)", k, f.seen)
		}
	}
	for _, k := range f.seen {
		if k == "k3" {
			t.Fatalf("a disabled key was tried: %v", f.seen)
		}
	}
}
