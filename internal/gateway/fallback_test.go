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

// modelFake answers every request with status and body (a plain reply when
// status is 0), and remembers the model each request asked for.
type modelFake struct {
	mu     sync.Mutex
	status int
	body   string
	models []string
}

func (f *modelFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var req struct {
		Model string `json:"model"`
	}
	json.Unmarshal(b, &req)
	f.mu.Lock()
	f.models = append(f.models, req.Model)
	f.mu.Unlock()
	if f.status != 0 {
		w.WriteHeader(f.status)
		io.WriteString(w, f.body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"x","object":"chat.completion","model":"`+req.Model+`","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
}

func (f *modelFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.models)
}

// fallbackSetup saves two providers: p1 (the primary) and p2 (the fallback
// target), each served by its own fake, and returns the fakes and a gateway.
func fallbackSetup(t *testing.T, primary *modelFake, fallback *modelFake, fallbackList []string) (*Server, http.Handler) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up1 := httptest.NewServer(primary)
	t.Cleanup(up1.Close)
	up2 := httptest.NewServer(fallback)
	t.Cleanup(up2.Close)
	if err := provider.Save(provider.Provider{ID: "p2", Name: "Fallback Two", Chat: up2.URL + "/v1", Models: []string{"m2"}, Key: "k2"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "p1", Name: "Primary One", Chat: up1.URL + "/v1", Models: []string{"m1"}, Key: "k1", Fallback: fallbackList}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	return srv, srv.Handler()
}

const fallbackBody = `{"model":"p1/m1","messages":[{"role":"user","content":"hi"}]}`

func TestFallbackListIsCleanedAndSaved(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "p1", Name: "P1", Chat: "https://a.example/v1", Key: "k1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetFallback("p1", []string{"", " p2/m2 ", "p2/m2", "p3/m3", "  "}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("p1")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Fallback, "|"); got != "p2/m2|p3/m3" {
		t.Fatalf("fallback = %q, want p2/m2|p3/m3", got)
	}
	if err := provider.SetFallback("p1", []string{""}); err != nil {
		t.Fatal(err)
	}
	if p, _ = provider.Find("p1"); len(p.Fallback) != 0 {
		t.Fatalf("empty list should clear fallback, got %v", p.Fallback)
	}
	if err := provider.SetFallback("nope", []string{"p2/m2"}); err == nil {
		t.Fatal("setting fallback on an unknown provider should fail")
	}
}

func TestFallbackNotUsedFor400(t *testing.T) {
	primary := &modelFake{status: 400, body: `{"error":{"message":"bad param"}}`}
	fb := &modelFake{}
	srv, h := fallbackSetup(t, primary, fb, []string{"p2/m2"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fallbackBody)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 passed through", rec.Code)
	}
	if fb.count() != 0 {
		t.Fatalf("fallback was called for a 400")
	}
	if c := srv.Recent()[0]; c.Fallback != "" {
		t.Fatalf("400 should not record a fallback, got %q", c.Fallback)
	}
}

func TestFallbackOn429UsesFallbackModelName(t *testing.T) {
	primary := &modelFake{status: 429, body: `{"error":{"message":"rate limit reached"}}`}
	fb := &modelFake{}
	srv, h := fallbackSetup(t, primary, fb, []string{"p2/m2"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fallbackBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s; client should not see the 429", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "rate limit") {
		t.Fatalf("client saw the primary's error: %s", rec.Body.String())
	}
	if len(fb.models) != 1 || fb.models[0] != "m2" {
		t.Fatalf("fallback upstream got models %v, want [m2]", fb.models)
	}
	c := srv.Recent()[0]
	if c.Status != http.StatusOK || c.Provider != "p2" || c.Fallback != "p1" {
		t.Fatalf("call record: status=%d provider=%q fallback=%q", c.Status, c.Provider, c.Fallback)
	}

	// the primary is resting: the next request goes straight to the fallback
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fallbackBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("second request: status %d", rec.Code)
	}
	if primary.count() != 1 {
		t.Fatalf("primary was tried again while resting (%d calls)", primary.count())
	}
	if c := srv.Recent()[0]; c.Provider != "p2" || c.Fallback != "" {
		t.Fatalf("second call: provider=%q fallback=%q", c.Provider, c.Fallback)
	}
}

func TestFallbackAllFailReturnsLastError(t *testing.T) {
	primary := &modelFake{status: 429, body: `{"error":{"message":"rate limit reached"}}`}
	fb := &modelFake{status: 503, body: `{"error":{"message":"service down"}}`}
	srv, h := fallbackSetup(t, primary, fb, []string{"p2/m2"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fallbackBody)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want the last candidate's 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "service down") {
		t.Fatalf("client should get the last candidate's error, got %s", rec.Body.String())
	}
	if c := srv.Recent()[0]; c.Fallback != "p1" || c.Provider != "p2" {
		t.Fatalf("call record: provider=%q fallback=%q", c.Provider, c.Fallback)
	}
}

func TestFallbackSkipsUnresolvableEntries(t *testing.T) {
	primary := &modelFake{status: 429, body: `{"error":{"message":"rate limit reached"}}`}
	fb := &modelFake{}
	srv, h := fallbackSetup(t, primary, fb, []string{"no-such/m", "nomodel", "/m", "p2/m2"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fallbackBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 via the resolvable entry", rec.Code)
	}
	if c := srv.Recent()[0]; c.Provider != "p2" {
		t.Fatalf("provider %q, want p2", c.Provider)
	}
}
