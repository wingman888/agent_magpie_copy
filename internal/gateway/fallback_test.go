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
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// scripted is an upstream that answers every call with one fixed reply and
// counts what it got.
type scripted struct {
	mu     sync.Mutex
	calls  int
	got    []byte
	code   int
	ctype  string
	reply  string
	before func(w http.ResponseWriter) // optional: replaces the reply entirely
}

func (f *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls++
	f.got = b
	f.mu.Unlock()
	if f.before != nil {
		f.before(w)
		return
	}
	ct := f.ctype
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	if f.code != 0 {
		w.WriteHeader(f.code)
	}
	io.WriteString(w, f.reply)
}

func (f *scripted) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *scripted) model() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return modelOf(f.got)
}

const chatOK = `{"id":"c1","object":"chat.completion","model":"m2","choices":[{"index":0,"message":{"role":"assistant","content":"answer from B"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`

// twoProviders saves Chat providers "a" (model m1, fallbacks as given) and
// "b" (model m2), backed by the two upstreams.
func twoProviders(t *testing.T, a, b *scripted, fallback ...string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ua := httptest.NewServer(a)
	t.Cleanup(ua.Close)
	ub := httptest.NewServer(b)
	t.Cleanup(ub.Close)
	if err := provider.Save(provider.Provider{ID: "a", Name: "Prov A", Key: "k", Models: []string{"m1"},
		Chat: ua.URL + "/v1", Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "Prov B", Key: "k", Models: []string{"m2"},
		Chat: ub.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

func send(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const chatReq = `{"model":"a/m1","messages":[{"role":"user","content":"hi"}]}`

func TestFallbackOnSwitchableErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  int
		reply string
	}{
		{"429", 429, `{"error":{"message":"slow down please"}}`},
		{"402", 402, `{"error":{"message":"payment required here"}}`},
		{"529", 529, `{"error":{"message":"the engine is busy"}}`},
		{"500", 500, `{"error":{"message":"internal kaboom"}}`},
		{"400 quota zh", 400, `{"error":{"message":"套餐额度已用完，请明日再试"}}`},
		{"403 balance", 403, `{"error":{"message":"Insufficient Balance on account"}}`},
		{"401 too many", 401, `{"error":{"message":"Too Many Requests"}}`},
		{"400 code only", 400, `{"error":{"code":"insufficient_quota","message":"denied for account"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &scripted{code: tc.code, reply: tc.reply}
			b := &scripted{reply: chatOK}
			twoProviders(t, a, b, "b/m2")
			srv := New()
			code, body := send(srv.Handler(), "/v1/chat/completions", chatReq)
			if code != 200 || !strings.Contains(body, "answer from B") {
				t.Fatalf("status %d: %s", code, body)
			}
			if strings.Contains(body, "Prov A") || strings.Contains(body, "error") {
				t.Fatalf("reply carries A's failure: %s", body)
			}
			if a.count() != 1 || b.count() != 1 {
				t.Fatalf("calls a=%d b=%d", a.count(), b.count())
			}
			if b.model() != "m2" {
				t.Fatalf("B got model %q", b.model())
			}
			calls := srv.Recent()
			if len(calls) != 1 || calls[0].Provider != "b" || calls[0].Status != 200 || !strings.HasPrefix(calls[0].Fallback, "a: ") {
				t.Fatalf("record: %+v", calls)
			}
		})
	}
}

// An Anthropic client of a Chat upstream goes through translation; the
// fallback works there too.
func TestFallbackTranslated(t *testing.T) {
	a := &scripted{code: 429, reply: `{"error":{"message":"rate limited"}}`}
	b := &scripted{ctype: "text/event-stream", reply: sse(
		`data: {"id":"c1","model":"m2","choices":[{"index":0,"delta":{"role":"assistant","content":"translated B"}}]}`,
		`data: {"id":"c1","model":"m2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
		`data: [DONE]`)}
	twoProviders(t, a, b, "b/m2")
	srv := New()
	code, body := send(srv.Handler(), "/v1/messages", `{"model":"a/m1","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "translated B") || strings.Contains(body, "rate limited") {
		t.Fatalf("status %d: %s", code, body)
	}
	if b.model() != "m2" {
		t.Fatalf("B got model %q", b.model())
	}
}

func TestFallbackRestPeriod(t *testing.T) {
	a := &scripted{code: 429, reply: `{"error":{"message":"rate limit reached"}}`}
	b := &scripted{reply: chatOK}
	twoProviders(t, a, b, "b/m2")
	srv := New()
	now := time.Now()
	srv.rest.now = func() time.Time { return now }
	h := srv.Handler()
	if code, body := send(h, "/v1/chat/completions", chatReq); code != 200 {
		t.Fatalf("first: %d %s", code, body)
	}
	now = now.Add(30 * time.Second)
	code, body := send(h, "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, "answer from B") {
		t.Fatalf("second: %d %s", code, body)
	}
	if a.count() != 1 || b.count() != 2 {
		t.Fatalf("resting A was tried: a=%d b=%d", a.count(), b.count())
	}
	c := srv.Recent()[0]
	if c.Provider != "b" || !strings.HasPrefix(c.Fallback, "a: resting after 429") {
		t.Fatalf("record: %+v", c)
	}
	// once rested, A is first again
	now = now.Add(31 * time.Second)
	send(h, "/v1/chat/completions", chatReq)
	if a.count() != 2 || b.count() != 3 {
		t.Fatalf("after rest: a=%d b=%d", a.count(), b.count())
	}
	if c := srv.Recent()[0]; c.Provider != "b" || !strings.HasPrefix(c.Fallback, "a: 429") {
		t.Fatalf("record: %+v", c)
	}
	// resting providers are still tried last when nobody else can answer
	srv.rest.reset()
	b.code, b.reply = 503, `{"error":{"message":"b down"}}`
	send(h, "/v1/chat/completions", chatReq) // both rest now
	a.code, a.reply = 0, chatOK
	code, body = send(h, "/v1/chat/completions", chatReq)
	if code != 200 || a.count() != 4 {
		t.Fatalf("resting only: %d %s a=%d", code, body, a.count())
	}
}

func TestNoFallbackOnRequestError(t *testing.T) {
	a := &scripted{code: 400, reply: `{"error":{"message":"messages: field required"}}`}
	b := &scripted{reply: chatOK}
	twoProviders(t, a, b, "b/m2")
	srv := New()
	code, body := send(srv.Handler(), "/v1/chat/completions", chatReq)
	if code != 400 || !strings.Contains(body, "messages: field required") {
		t.Fatalf("status %d: %s", code, body)
	}
	if b.count() != 0 {
		t.Fatalf("B was called")
	}
	if c := srv.Recent()[0]; c.Provider != "a" || c.Fallback != "" {
		t.Fatalf("record: %+v", c)
	}
	a.code = 404
	if code, _ := send(srv.Handler(), "/v1/chat/completions", chatReq); code != 404 || b.count() != 0 {
		t.Fatalf("404: %d b=%d", code, b.count())
	}
}

func TestFallbackAllFail(t *testing.T) {
	a := &scripted{code: 429, reply: `{"error":{"message":"a is limited"}}`}
	b := &scripted{code: 503, reply: `{"error":{"message":"b is down for maintenance"}}`}
	twoProviders(t, a, b, "b/m2")
	srv := New()
	code, body := send(srv.Handler(), "/v1/chat/completions", chatReq)
	if code != 503 || !strings.Contains(body, "b is down for maintenance") || strings.Contains(body, "a is limited") {
		t.Fatalf("status %d: %s", code, body)
	}
	c := srv.Recent()[0]
	if c.Provider != "b" || c.Status != 503 || !strings.HasPrefix(c.Fallback, "a: 429") {
		t.Fatalf("record: %+v", c)
	}
}

func TestFallbackSkipsUnknownAndRepeats(t *testing.T) {
	a := &scripted{code: 429, reply: `{"error":{"message":"limited"}}`}
	b := &scripted{reply: chatOK}
	twoProviders(t, a, b, "nowhere/x", "a/m1", "b/m2", "b/m2")
	srv := New()
	code, body := send(srv.Handler(), "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, "answer from B") {
		t.Fatalf("status %d: %s", code, body)
	}
	if a.count() != 1 || b.count() != 1 {
		t.Fatalf("calls a=%d b=%d", a.count(), b.count())
	}
	if c := srv.Recent()[0]; strings.Count(c.Fallback, ";") != 0 {
		t.Fatalf("fallback note: %q", c.Fallback)
	}
}

// Fallbacks are not followed transitively.
func TestFallbackNotRecursive(t *testing.T) {
	a := &scripted{code: 429, reply: `{"error":{"message":"limited"}}`}
	b := &scripted{code: 429, reply: `{"error":{"message":"b limited"}}`}
	twoProviders(t, a, b, "b/m2")
	c := &scripted{reply: chatOK}
	uc := httptest.NewServer(c)
	t.Cleanup(uc.Close)
	provider.Save(provider.Provider{ID: "c", Name: "C", Key: "k", Models: []string{"m3"}, Chat: uc.URL + "/v1"})
	p, _ := provider.Find("b")
	p.Fallback = []string{"c/m3"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	code, _ := send(New().Handler(), "/v1/chat/completions", chatReq)
	if code != 429 || c.count() != 0 {
		t.Fatalf("status %d, c=%d", code, c.count())
	}
}

func TestStreamStartedDoesNotFallBack(t *testing.T) {
	a := &scripted{before: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"from A partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // the connection drops mid-stream
	}}
	b := &scripted{reply: chatOK}
	twoProviders(t, a, b, "b/m2")
	srv := New()
	code, body := send(srv.Handler(), "/v1/chat/completions", `{"model":"a/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from A partial") || strings.Contains(body, "answer from B") {
		t.Fatalf("status %d: %s", code, body)
	}
	if b.count() != 0 {
		t.Fatalf("B was called after A started")
	}
	if c := srv.Recent()[0]; c.Provider != "a" || c.Fallback != "" {
		t.Fatalf("record: %+v", c)
	}
}

func TestFallbackStopsWhenClientGone(t *testing.T) {
	a := &scripted{code: 429, reply: `{"error":{"message":"limited"}}`}
	b := &scripted{reply: chatOK}
	twoProviders(t, a, b, "b/m2")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)).WithContext(ctx)
	New().Handler().ServeHTTP(rec, req)
	if b.count() != 0 {
		t.Fatalf("B was tried for a gone client")
	}
}

func TestNoFallbackConfiguredUnchanged(t *testing.T) {
	a := &scripted{code: 429, reply: `{"error":{"message":"a is limited"}}`}
	b := &scripted{reply: chatOK}
	twoProviders(t, a, b)
	srv := New()
	code, body := send(srv.Handler(), "/v1/chat/completions", chatReq)
	if code != 429 || !strings.Contains(body, "a is limited") || b.count() != 0 {
		t.Fatalf("status %d: %s", code, body)
	}
	c := srv.Recent()[0]
	if c.Fallback != "" {
		t.Fatalf("record: %+v", c)
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), `"fallback"`) {
		t.Fatalf("record JSON has fallback: %s", raw)
	}
	a.code, a.reply = 0, chatOK
	code, body = send(srv.Handler(), "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, "answer from B") { // A replies with the same canned answer
		t.Fatalf("status %d: %s", code, body)
	}
}

func TestSwitchable(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{402, "", true}, {408, "", true}, {429, "", true}, {500, "", true}, {503, "", true}, {529, "", true},
		{400, "messages: field required", false},
		{404, "quota exceeded", false},
		{400, "You exceeded your current QUOTA", true},
		{401, "invalid api key", false},
		{403, "Usage Limit reached", true},
		{400, "请求频率过高", true},
		{403, "账户欠费", true},
		{400, "server Overloaded", true},
	} {
		if got := switchable(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("switchable(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}
