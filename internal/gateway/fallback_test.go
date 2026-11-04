package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// fbUpstream is a fake provider endpoint that answers with a fixed status and body.
type fbUpstream struct {
	code  int
	ctype string
	body  string
	hits  atomic.Int32
	model atomic.Value // last "model" field it received
}

func (u *fbUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.hits.Add(1)
	b, _ := io.ReadAll(r.Body)
	var m struct {
		Model string `json:"model"`
	}
	json.Unmarshal(b, &m)
	u.model.Store(m.Model)
	w.Header().Set("Content-Type", u.ctype)
	w.WriteHeader(u.code)
	io.WriteString(w, u.body)
}

func (u *fbUpstream) lastModel() string {
	s, _ := u.model.Load().(string)
	return s
}

// fbSetup points config at a temp dir and registers providers a (primary,
// with fallbacks) and b (plain). It returns the upstreams for a and b.
func fbSetup(t *testing.T, a, b *fbUpstream, aFallback []string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	ua := httptest.NewServer(a)
	t.Cleanup(ua.Close)
	ub := httptest.NewServer(b)
	t.Cleanup(ub.Close)
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Chat: ua.URL + "/v1", Key: "k",
		Models: []string{"m1"}, Fallback: aFallback}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Chat: ub.URL + "/v1", Key: "k",
		Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
}

const fbOK = `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from B"},"finish_reason":"stop"}]}`

func fbPost(t *testing.T, s *Server, model string, stream bool) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]`
	if stream {
		body += `,"stream":true`
	}
	body += `}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestFallbackOn429SwitchesToB(t *testing.T) {
	a := &fbUpstream{code: 429, ctype: "application/json", body: `{"error":{"message":"slow down A"}}`}
	b := &fbUpstream{code: 200, ctype: "application/json", body: fbOK}
	fbSetup(t, a, b, []string{"b/m2"})
	s := New()
	rec := fbPost(t, s, "a/m1", false)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "slow down A") || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("body = %s, want only B's reply", rec.Body)
	}
	if got := b.lastModel(); got != "m2" {
		t.Fatalf("B received model %q, want m2", got)
	}
	calls := s.Recent()
	if len(calls) != 1 {
		t.Fatalf("recent = %d calls, want 1", len(calls))
	}
	if calls[0].Provider != "b" {
		t.Fatalf("record provider = %q, want b", calls[0].Provider)
	}
	if !strings.HasPrefix(calls[0].Fallback, "a: ") {
		t.Fatalf("record fallback = %q, want prefix %q", calls[0].Fallback, "a: ")
	}
}

func TestFallbackSwitchRules(t *testing.T) {
	cases := []struct {
		name       string
		code       int
		body       string
		wantSwitch bool
	}{
		{"402", 402, `{"error":{"message":"payment required"}}`, true},
		{"408", 408, `{"error":{"message":"timeout"}}`, true},
		{"429", 429, `{"error":{"message":"slow"}}`, true},
		{"500", 500, `{"error":{"message":"boom"}}`, true},
		{"529 overloaded", 529, `{"error":{"message":"overloaded"}}`, true},
		{"400 insufficient balance", 400, `{"error":{"message":"Insufficient balance"}}`, true},
		{"400 too many requests", 400, `{"error":{"message":"Too many requests"}}`, true},
		{"403 chinese quota", 403, `{"error":{"message":"额度已用完"}}`, true},
		{"401 usage limit", 401, `{"error":{"message":"Usage limit reached"}}`, true},
		{"400 plain", 400, `{"error":{"message":"messages: field required"}}`, false},
		{"404", 404, `{"error":{"message":"no such thing"}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &fbUpstream{code: tc.code, ctype: "application/json", body: tc.body}
			b := &fbUpstream{code: 200, ctype: "application/json", body: fbOK}
			fbSetup(t, a, b, []string{"b/m2"})
			rec := fbPost(t, New(), "a/m1", false)
			if tc.wantSwitch {
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
					t.Fatalf("status %d body %s, want B's reply", rec.Code, rec.Body)
				}
				if b.hits.Load() != 1 {
					t.Fatalf("B hits = %d, want 1", b.hits.Load())
				}
				return
			}
			if rec.Code != tc.code {
				t.Fatalf("status %d, want %d; body %s", rec.Code, tc.code, rec.Body)
			}
			if b.hits.Load() != 0 {
				t.Fatalf("B hits = %d, want 0", b.hits.Load())
			}
		})
	}
}

func TestFallbackPlain400NotSwitched(t *testing.T) {
	a := &fbUpstream{code: 400, ctype: "application/json", body: `{"error":{"message":"messages: field required"}}`}
	b := &fbUpstream{code: 200, ctype: "application/json", body: fbOK}
	fbSetup(t, a, b, []string{"b/m2"})
	s := New()
	rec := fbPost(t, s, "a/m1", false)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "messages: field required") {
		t.Fatalf("status %d body %s, want original 400", rec.Code, rec.Body)
	}
	if b.hits.Load() != 0 {
		t.Fatalf("B received %d requests, want none", b.hits.Load())
	}
}

func TestFallbackRestingProviderTriedLast(t *testing.T) {
	a := &fbUpstream{code: 429, ctype: "application/json", body: `{"error":{"message":"slow"}}`}
	b := &fbUpstream{code: 200, ctype: "application/json", body: fbOK}
	fbSetup(t, a, b, []string{"b/m2"})
	s := New()
	fbPost(t, s, "a/m1", false) // A fails and starts its rest
	before := a.hits.Load()
	rec := fbPost(t, s, "a/m1", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("status %d body %s, want B's reply", rec.Code, rec.Body)
	}
	if a.hits.Load() != before {
		t.Fatalf("A was contacted during its rest period")
	}
	if got := s.Recent()[0]; got.Provider != "b" || !strings.HasPrefix(got.Fallback, "a: ") {
		t.Fatalf("record = provider %q fallback %q", got.Provider, got.Fallback)
	}
}

func TestPlanRestingProvidersGoLast(t *testing.T) {
	a := &fbUpstream{code: 200, ctype: "application/json", body: fbOK}
	b := &fbUpstream{code: 200, ctype: "application/json", body: fbOK}
	fbSetup(t, a, b, []string{"b/m2", "nowhere/x", "a/m1", "b/m2"})
	s := New()
	primary, model, _ := provider.Resolve("a/m1")
	ids := func(cs []candidate) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.p.ID+"/"+c.model)
		}
		return out
	}
	if got, want := ids(s.plan(primary, model)), []string{"a/m1", "b/m2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	s.rest("a")
	if got, want := ids(s.plan(primary, model)), []string{"b/m2", "a/m1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plan with A resting = %q, want %q", got, want)
	}
	s.rest("b")
	if got, want := ids(s.plan(primary, model)), []string{"a/m1", "b/m2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plan with both resting = %q, want %q", got, want)
	}
}

func TestFallbackAllFailReturnsLastError(t *testing.T) {
	a := &fbUpstream{code: 429, ctype: "application/json", body: `{"error":{"message":"slow down A"}}`}
	b := &fbUpstream{code: 503, ctype: "application/json", body: `{"error":{"message":"B is down"}}`}
	fbSetup(t, a, b, []string{"b/m2"})
	s := New()
	rec := fbPost(t, s, "a/m1", false)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "B is down") {
		t.Fatalf("status %d body %s, want B's 503", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "slow down A") {
		t.Fatalf("body leaked A's error: %s", rec.Body)
	}
	if c := s.Recent()[0]; c.Status != 503 || c.Provider != "b" {
		t.Fatalf("record status %d provider %q", c.Status, c.Provider)
	}
}

func TestFallbackStreamNoSwitchAfterOutput(t *testing.T) {
	sse := "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial A\"}}]}\n\n" +
		"data: {\"error\":{\"message\":\"stream broke\"}}\n\n"
	a := &fbUpstream{code: 200, ctype: "text/event-stream", body: sse}
	b := &fbUpstream{code: 200, ctype: "application/json", body: fbOK}
	fbSetup(t, a, b, []string{"b/m2"})
	s := New()
	rec := fbPost(t, s, "a/m1", true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "partial A") {
		t.Fatalf("status %d body %s, want A's stream", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("B's content mixed into A's stream: %s", rec.Body)
	}
	if b.hits.Load() != 0 {
		t.Fatalf("B received %d requests, want none", b.hits.Load())
	}
	if c := s.Recent()[0]; c.Provider != "a" || c.Fallback != "" {
		t.Fatalf("record provider %q fallback %q, want a and empty", c.Provider, c.Fallback)
	}
}

func TestFallbackStopsWhenClientGone(t *testing.T) {
	a := &fbUpstream{code: 429, ctype: "application/json", body: `{"error":{"message":"slow"}}`}
	b := &fbUpstream{code: 200, ctype: "application/json", body: fbOK}
	fbSetup(t, a, b, []string{"b/m2"})
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := `{"model":"a/m1","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if b.hits.Load() != 0 {
		t.Fatalf("B received %d requests after the client left", b.hits.Load())
	}
}

func TestFallbackAbsentWithoutFallbackConfig(t *testing.T) {
	a := &fbUpstream{code: 429, ctype: "application/json", body: `{"error":{"message":"slow down A"}}`}
	b := &fbUpstream{code: 200, ctype: "application/json", body: fbOK}
	fbSetup(t, a, b, nil)
	s := New()
	rec := fbPost(t, s, "a/m1", false)
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "slow down A") {
		t.Fatalf("status %d body %s, want A's 429", rec.Code, rec.Body)
	}
	if b.hits.Load() != 0 {
		t.Fatalf("B received %d requests, want none", b.hits.Load())
	}
	c := s.Recent()[0]
	if c.Fallback != "" {
		t.Fatalf("fallback = %q, want empty", c.Fallback)
	}
	js, _ := json.Marshal(c)
	if strings.Contains(string(js), `"fallback"`) {
		t.Fatalf("record JSON has a fallback key: %s", js)
	}
}
