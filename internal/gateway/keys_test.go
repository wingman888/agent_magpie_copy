package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// keyUpstream is a Chat upstream that answers each key with a status from
// codes (200 when the key is not listed), and records the keys it saw.
type keyUpstream struct {
	mu    sync.Mutex
	codes map[string]int
	seen  []string
}

func (k *keyUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	k.mu.Lock()
	k.seen = append(k.seen, key)
	code := k.codes[key]
	k.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
	if code == http.StatusOK {
		io.WriteString(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
		return
	}
	io.WriteString(w, `{"error":{"message":"slow down for `+key+`"}}`)
}

// keyedRelay saves a Chat provider "fake" with two enabled keys, the first
// named "personal" and the second "team".
func keyedRelay(t *testing.T, up *keyUpstream) *httptest.Server {
	t.Helper()
	isolate(t)
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "sk-1", KeyName: "personal",
		Chat: srv.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("fake", "team", "sk-2", ""); err != nil {
		t.Fatal(err)
	}
	return srv
}

func sendChat(s *Server) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`))
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestRateLimitedKeyHandsOverToNextKey(t *testing.T) {
	up := &keyUpstream{codes: map[string]int{"sk-1": http.StatusTooManyRequests}}
	keyedRelay(t, up)
	s := New()

	code, body := sendChat(s)
	if code != 200 || !strings.Contains(body, "hi") {
		t.Fatalf("first request: %d %s", code, body)
	}
	calls := s.Recent()
	if len(calls) != 1 || calls[0].Provider != "fake" || !strings.Contains(calls[0].Fallback, "fake") || !strings.Contains(calls[0].Fallback, "personal") {
		t.Fatalf("call record: %+v", calls)
	}
	// the next request goes straight to the second key
	if code, body := sendChat(s); code != 200 {
		t.Fatalf("second request: %d %s", code, body)
	}
	if want := []string{"sk-1", "sk-2", "sk-2"}; !reflect.DeepEqual(up.seen, want) {
		t.Fatalf("keys tried %v, want %v", up.seen, want)
	}
	if last := s.Recent()[0]; last.Fallback != "" {
		t.Fatalf("second request records a fallback: %q", last.Fallback)
	}
}

func TestBadRequestIsNotRetriedWithAnotherKey(t *testing.T) {
	up := &keyUpstream{codes: map[string]int{"sk-1": http.StatusBadRequest, "sk-2": http.StatusBadRequest}}
	keyedRelay(t, up)
	code, body := sendChat(New())
	if code != 400 || !strings.Contains(body, "sk-1") {
		t.Fatalf("%d %s", code, body)
	}
	if want := []string{"sk-1"}; !reflect.DeepEqual(up.seen, want) {
		t.Fatalf("keys tried %v, want %v", up.seen, want)
	}
}

func TestLastKeysErrorIsReturned(t *testing.T) {
	up := &keyUpstream{codes: map[string]int{"sk-1": http.StatusTooManyRequests, "sk-2": http.StatusTooManyRequests}}
	keyedRelay(t, up)
	code, body := sendChat(New())
	if code != 429 || !strings.Contains(body, "sk-2") {
		t.Fatalf("%d %s; want the second key's 429", code, body)
	}
	if want := []string{"sk-1", "sk-2"}; !reflect.DeepEqual(up.seen, want) {
		t.Fatalf("keys tried %v, want %v", up.seen, want)
	}
}

func TestDisabledKeyIsNeverTried(t *testing.T) {
	up := &keyUpstream{codes: map[string]int{"sk-1": http.StatusTooManyRequests}}
	keyedRelay(t, up)
	if err := provider.SetKeyOn("fake", provider.KeyRef("sk-2"), false); err != nil {
		t.Fatal(err)
	}
	// the only enabled key is rate limited; the disabled one must not be tried
	code, _ := sendChat(New())
	if code != 429 {
		t.Fatalf("status %d, want 429", code)
	}
	for _, k := range up.seen {
		if k == "sk-2" {
			t.Fatalf("disabled key was tried: %v", up.seen)
		}
	}
}
