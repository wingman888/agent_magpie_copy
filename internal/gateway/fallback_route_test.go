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

// gwfbUpstream is a fake vendor. It counts requests and answers with a fixed
// status and body; abort cuts the connection after the body is sent.
type gwfbUpstream struct {
	mu     sync.Mutex
	status int
	body   string
	stream bool
	abort  bool
	header string // sent as X-Upstream-Id, to see whether a failed reply leaks headers
	calls  int
	model  string // the model the last request named
}

func (u *gwfbUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.calls++
	u.model = modelOf(raw)
	u.mu.Unlock()
	if u.header != "" {
		w.Header().Set("X-Upstream-Id", u.header)
	}
	if u.stream {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	status := u.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	io.WriteString(w, u.body)
	if u.abort {
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
}

func (u *gwfbUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func (u *gwfbUpstream) lastModel() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.model
}

const gwfbChatB = `{"id":"c1","object":"chat.completion","created":1,"model":"m2","choices":[{"index":0,"message":{"role":"assistant","content":"from B"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// gwfbSetup saves providers a (model m1, with aFallback) and b (model m2) pointed
// at the two fakes, and returns the gateway serving them.
func gwfbSetup(t *testing.T, a, b *gwfbUpstream, aFallback []string) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	upA := httptest.NewServer(a)
	t.Cleanup(upA.Close)
	upB := httptest.NewServer(b)
	t.Cleanup(upB.Close)
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "k", Chat: upA.URL + "/v1", Models: []string{"m1"}, Fallback: aFallback},
		{ID: "b", Name: "B", Key: "k", Chat: upB.URL + "/v1", Models: []string{"m2"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	return New()
}

func gwfbPost(s *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec
}

const gwfbReq = `{"model":"a/m1","messages":[{"role":"user","content":"hi"}]}`

func TestGWFallbackSwitchesOnRecoverableFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		msg    string // the upstream error text that must not reach the client
	}{
		{"429", 429, `{"error":{"message":"rate limited"}}`, "rate limited"},
		{"402", 402, `{"error":{"message":"payment needed"}}`, "payment needed"},
		{"529", 529, `{"error":{"message":"server busy"}}`, "server busy"},
		{"500", 500, `{"error":{"message":"boom"}}`, "boom"},
		{"400 quota zh", 400, `{"error":{"message":"额度已用完"}}`, "额度已用完"},
		{"403 balance", 403, `{"error":{"message":"Insufficient Balance"}}`, "Insufficient Balance"},
		{"400 too many requests", 400, `{"error":{"message":"Too Many Requests"}}`, "Too Many Requests"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &gwfbUpstream{status: tc.status, body: tc.body, header: "from-a"}
			b := &gwfbUpstream{body: gwfbChatB}
			s := gwfbSetup(t, a, b, []string{"b/m2"})
			rec := gwfbPost(s, gwfbReq)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), tc.msg) || rec.Header().Get("X-Upstream-Id") != "" {
				t.Errorf("failed attempt leaked into the reply: %s / %v", rec.Body.String(), rec.Header())
			}
			if got := b.lastModel(); got != "m2" {
				t.Errorf("fallback received model %q, want m2", got)
			}
			c := s.Recent()[0]
			if c.Provider != "b" || c.Status != 200 || !strings.HasPrefix(c.Fallback, "a: ") {
				t.Errorf("record %+v", c)
			}
		})
	}
}

func TestGWFallbackCoolsDownFailedProvider(t *testing.T) {
	a := &gwfbUpstream{status: 429, body: `{"error":{"message":"rate limited"}}`}
	b := &gwfbUpstream{body: gwfbChatB}
	s := gwfbSetup(t, a, b, []string{"b/m2"})

	if rec := gwfbPost(s, gwfbReq); rec.Code != 200 {
		t.Fatalf("first request: %d %s", rec.Code, rec.Body.String())
	}
	if rec := gwfbPost(s, gwfbReq); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("second request: %d %s", rec.Code, rec.Body.String())
	}
	if n := a.count(); n != 1 {
		t.Errorf("A was contacted %d times, want 1: the second request should go straight to B", n)
	}
	// both answers come from b; the second one did not try a, but says why a is skipped
	for i, c := range s.Recent() {
		if c.Provider != "b" || c.Fallback != "a: 429 rate limited" {
			t.Errorf("record %d %+v: want provider b and fallback %q", i, c, "a: 429 rate limited")
		}
	}
}

func TestGWFallbackStopsOnPlainBadRequest(t *testing.T) {
	a := &gwfbUpstream{status: 400, body: `{"error":{"message":"messages: field required"}}`}
	b := &gwfbUpstream{body: gwfbChatB}
	s := gwfbSetup(t, a, b, []string{"b/m2"})
	rec := gwfbPost(s, gwfbReq)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "messages: field required") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if b.count() != 0 {
		t.Errorf("fallback was contacted for a plain 400")
	}
	if c := s.Recent()[0]; c.Fallback != "" || c.Provider != "a" {
		t.Errorf("record %+v", c)
	}
}

func TestGWFallbackAllFailReturnsLastError(t *testing.T) {
	a := &gwfbUpstream{status: 429, body: `{"error":{"message":"rate limited"}}`}
	b := &gwfbUpstream{status: 503, body: `{"error":{"message":"down"}}`}
	s := gwfbSetup(t, a, b, []string{"b/m2"})
	rec := gwfbPost(s, gwfbReq)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "B: down") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	c := s.Recent()[0]
	if c.Provider != "b" || c.Status != 503 || c.Fallback != "a: 429 rate limited" {
		t.Errorf("record %+v", c)
	}
}

func TestGWFallbackSkipsInvalidAndDuplicateEntries(t *testing.T) {
	a := &gwfbUpstream{status: 429, body: `{"error":{"message":"rate limited"}}`}
	b := &gwfbUpstream{body: gwfbChatB}
	s := gwfbSetup(t, a, b, []string{"nowhere/x", "a/m1", "b/nope", " ", "b/m2", "b/m2"})
	rec := gwfbPost(s, gwfbReq)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestGWFallbackUnconfiguredUnchanged(t *testing.T) {
	a := &gwfbUpstream{status: 429, body: `{"error":{"message":"rate limited"}}`}
	b := &gwfbUpstream{body: gwfbChatB}
	s := gwfbSetup(t, a, b, nil)
	rec := gwfbPost(s, gwfbReq)
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "A: rate limited") || b.count() != 0 {
		t.Fatalf("%d %s (fallback calls %d)", rec.Code, rec.Body.String(), b.count())
	}
	c := s.Recent()[0]
	if c.Fallback != "" {
		t.Errorf("fallback = %q, want empty", c.Fallback)
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), `"fallback"`) {
		t.Errorf("record JSON carries a fallback key: %s", raw)
	}
}

func TestGWFallbackNoSwitchAfterStreamStarted(t *testing.T) {
	a := &gwfbUpstream{stream: true, abort: true,
		body: "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"from A\"},\"finish_reason\":null}]}\n\n"}
	b := &gwfbUpstream{stream: true, body: "data: {\"id\":\"c2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"from B\"},\"finish_reason\":null}]}\n\n"}
	s := gwfbSetup(t, a, b, []string{"b/m2"})
	rec := gwfbPost(s, `{"model":"a/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(rec.Body.String(), "from A") {
		t.Fatalf("A's first chunk missing: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "from B") || b.count() != 0 {
		t.Errorf("stream switched to fallback after output began: %s (fallback calls %d)", rec.Body.String(), b.count())
	}
}

func TestGWFallbackStreamingRequestSwitchesBeforeOutput(t *testing.T) {
	a := &gwfbUpstream{status: 429, body: `{"error":{"message":"rate limited"}}`}
	b := &gwfbUpstream{stream: true, body: "data: {\"id\":\"c2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"from B\"},\"finish_reason\":null}]}\n\n"}
	s := gwfbSetup(t, a, b, []string{"b/m2"})
	rec := gwfbPost(s, `{"model":"a/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from B") || strings.Contains(rec.Body.String(), "rate limited") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// A healthy main provider that answers while its fallback is cooling down is
// not a switch, so the record carries no fallback note.
func TestGWFallbackCoolingFallbackNotNotedWhenPrimaryAnswers(t *testing.T) {
	a := &gwfbUpstream{body: strings.Replace(gwfbChatB, "from B", "from A", 1)}
	b := &gwfbUpstream{body: gwfbChatB}
	s := gwfbSetup(t, a, b, []string{"b/m2"})
	s.cool("b", "b: 503 down")

	rec := gwfbPost(s, gwfbReq)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from A") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if a.count() != 1 || b.count() != 0 {
		t.Errorf("calls a=%d b=%d, want a=1 b=0", a.count(), b.count())
	}
	c := s.Recent()[0]
	if c.Provider != "a" || c.Fallback != "" {
		t.Errorf("record %+v: want provider a and no fallback", c)
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), `"fallback"`) {
		t.Errorf("record JSON carries a fallback key: %s", raw)
	}
}
