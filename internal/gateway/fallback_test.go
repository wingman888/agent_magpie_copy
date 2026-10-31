package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

type upstream struct {
	code  int
	body  string
	calls atomic.Int32
	model atomic.Value
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.calls.Add(1)
	b := make([]byte, 4096)
	n, _ := r.Body.Read(b)
	u.model.Store(string(b[:n]))
	w.Header().Set("Content-Type", "application/json")
	if u.code != 0 {
		w.WriteHeader(u.code)
	}
	w.Write([]byte(u.body))
}

const okChat = `{"id":"c1","choices":[{"message":{"role":"assistant","content":"from B"}}]}`

func twoProviders(t *testing.T, a, b *upstream, fb ...string) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ua, ub := httptest.NewServer(a), httptest.NewServer(b)
	t.Cleanup(ua.Close)
	t.Cleanup(ub.Close)
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Chat: ua.URL + "/v1", Models: []string{"m1"}, Fallback: fb}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "k", Chat: ub.URL + "/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	return New()
}

func send(s *Server, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const req = `{"model":"a/m1","messages":[{"role":"user","content":"hi"}]}`

func TestFallbackSwitches(t *testing.T) {
	for _, c := range []struct {
		code int
		body string
	}{
		{429, `{"error":{"message":"slow down"}}`},
		{402, `{"error":{"message":"pay"}}`},
		{529, `{"error":{"message":"busy"}}`},
		{400, `{"error":{"message":"额度已用完"}}`},
		{403, `{"error":{"message":"Insufficient balance"}}`},
		{401, `{"error":{"message":"Too Many Requests"}}`},
	} {
		a, b := &upstream{code: c.code, body: c.body}, &upstream{body: okChat}
		s := twoProviders(t, a, b, "nowhere/x", " b/m2 ", "b/m2", "a/m1")
		code, body := send(s, req)
		if code != 200 || !strings.Contains(body, "from B") || strings.Contains(body, c.body[10:14]) {
			t.Fatalf("%d: %d %s", c.code, code, body)
		}
		if !strings.Contains(b.model.Load().(string), `"model":"m2"`) || a.calls.Load() != 1 || b.calls.Load() != 1 {
			t.Errorf("%d: model %v calls %d %d", c.code, b.model.Load(), a.calls.Load(), b.calls.Load())
		}
		rc := s.Recent()[0]
		if rc.Provider != "b" || !strings.HasPrefix(rc.Fallback, "a: ") || strings.Contains(rc.Fallback, ";") {
			t.Errorf("record %+v", rc)
		}
	}
}

func TestFallbackRest(t *testing.T) {
	a, b := &upstream{code: 429, body: `{"error":{"message":"rate limit"}}`}, &upstream{body: okChat}
	s := twoProviders(t, a, b, "b/m2")
	send(s, req)
	if code, _ := send(s, req); code != 200 {
		t.Fatal(code)
	}
	if a.calls.Load() != 1 || b.calls.Load() != 2 {
		t.Errorf("calls %d %d", a.calls.Load(), b.calls.Load())
	}
	if rc := s.Recent()[0]; rc.Provider != "b" || !strings.HasPrefix(rc.Fallback, "a: ") {
		t.Errorf("%+v", rc)
	}
}

func TestFallbackNotForPlainErrors(t *testing.T) {
	a, b := &upstream{code: 400, body: `{"error":{"message":"messages: field required"}}`}, &upstream{body: okChat}
	s := twoProviders(t, a, b, "b/m2")
	code, body := send(s, req)
	if code != 400 || !strings.Contains(body, "field required") || b.calls.Load() != 0 {
		t.Fatalf("%d %s %d", code, body, b.calls.Load())
	}
	if s.Recent()[0].Fallback != "" {
		t.Error("fallback set")
	}
}

func TestFallbackAllFail(t *testing.T) {
	a, b := &upstream{code: 429, body: `{"error":{"message":"limit"}}`}, &upstream{code: 503, body: `{"error":{"message":"down b"}}`}
	s := twoProviders(t, a, b, "b/m2")
	code, body := send(s, req)
	if code != 503 || !strings.Contains(body, "down b") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestFallbackNotAfterStreamStarted(t *testing.T) {
	a := &upstream{body: "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n"}
	b := &upstream{body: okChat}
	s := twoProviders(t, a, b, "b/m2")
	code, body := send(s, `{"model":"a/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || b.calls.Load() != 0 || strings.Contains(body, "from B") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestNoFallbackConfigured(t *testing.T) {
	a, b := &upstream{code: 429, body: `{"error":{"message":"limit"}}`}, &upstream{body: okChat}
	s := twoProviders(t, a, b)
	if code, _ := send(s, req); code != 429 || b.calls.Load() != 0 || s.Recent()[0].Fallback != "" {
		t.Fatal(code)
	}
}

func TestRetryable(t *testing.T) {
	for _, c := range []struct {
		s    int
		b    string
		want bool
	}{{402, "", true}, {408, "", true}, {500, "", true}, {404, "quota", false}, {400, "Rate Limit", true}, {403, "套餐", true}, {400, "bad", false}} {
		if retryable(c.s, []byte(c.b)) != c.want {
			t.Errorf("%d %q", c.s, c.b)
		}
	}
}
