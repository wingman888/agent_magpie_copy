package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// multiFake answers each endpoint path as scripted and counts the requests.
type multiFake struct {
	mu    sync.Mutex
	calls map[string]int
	rules map[string]func(w http.ResponseWriter)
}

func (f *multiFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[r.URL.Path]++
	h := f.rules[r.URL.Path]
	f.mu.Unlock()
	if h == nil {
		http.NotFound(w, r)
		return
	}
	h(w)
}

func (f *multiFake) n(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func jsonErr(code int, msg string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"`+msg+`"}}`)
	}
}

func chatOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"chat ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`))
}

func respOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"resp ok"}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`))
}

func twoEndpoints(t *testing.T, f *multiFake, models ...string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: models,
		Chat: up.URL + "/v1", Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

const chatBody = `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
const respBody = `{"model":"m1","input":"hi"}`

func TestPassthroughSwitchesEndpointAndRemembers(t *testing.T) {
	f := &multiFake{rules: map[string]func(http.ResponseWriter){
		"/v1/chat/completions": jsonErr(400, "The model `m1` is not accessible via the /chat/completions endpoint"),
		"/v1/responses":        respOK,
	}}
	twoEndpoints(t, f, "m1")
	srv := New()
	do := func() (int, string) {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody)))
		return rec.Code, rec.Body.String()
	}
	code, body := do()
	if code != 200 || !strings.Contains(body, "resp ok") {
		t.Fatalf("%d %s", code, body)
	}
	if f.n("/v1/chat/completions") != 1 || f.n("/v1/responses") != 1 {
		t.Fatalf("chat=%d responses=%d", f.n("/v1/chat/completions"), f.n("/v1/responses"))
	}
	if code, _ = do(); code != 200 || f.n("/v1/chat/completions") != 1 || f.n("/v1/responses") != 2 {
		t.Fatalf("second: %d chat=%d responses=%d", code, f.n("/v1/chat/completions"), f.n("/v1/responses"))
	}
}

func TestResponsesClientSwitchesToChat(t *testing.T) {
	f := &multiFake{rules: map[string]func(http.ResponseWriter){
		"/v1/responses":        jsonErr(400, "unsupported_api_for_model"),
		"/v1/chat/completions": chatOK,
	}}
	twoEndpoints(t, f, "m1")
	srv := New()
	do := func() (int, string) {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(respBody)))
		return rec.Code, rec.Body.String()
	}
	code, body := do()
	if code != 200 || !strings.Contains(body, "chat ok") {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ = do(); code != 200 || f.n("/v1/responses") != 1 || f.n("/v1/chat/completions") != 2 {
		t.Fatalf("second: %d responses=%d chat=%d", code, f.n("/v1/responses"), f.n("/v1/chat/completions"))
	}
}

func TestRejectionIsPerModel(t *testing.T) {
	f := &multiFake{rules: map[string]func(http.ResponseWriter){
		"/v1/chat/completions": jsonErr(400, "not a chat model"),
		"/v1/responses":        respOK,
	}}
	twoEndpoints(t, f, "m1", "m2")
	srv := New()
	do := func(model string) int {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)))
		return rec.Code
	}
	do("m1")
	do("m2")
	if f.n("/v1/chat/completions") != 2 {
		t.Errorf("m2 should still try chat: %d", f.n("/v1/chat/completions"))
	}
}

func TestNoSwitchOn5xxAndReturnsLastError(t *testing.T) {
	f := &multiFake{rules: map[string]func(http.ResponseWriter){
		"/v1/chat/completions": jsonErr(400, "bad: use v1/responses"),
		"/v1/responses":        jsonErr(400, "unsupported_api_for_model"),
	}}
	twoEndpoints(t, f, "m1")
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody)))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "unsupported_api_for_model") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	f2 := &multiFake{rules: map[string]func(http.ResponseWriter){
		"/v1/chat/completions": jsonErr(500, "use v1/responses"),
		"/v1/responses":        respOK,
	}}
	twoEndpoints(t, f2, "m1")
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody)))
	if rec.Code != 500 || f2.n("/v1/responses") != 0 {
		t.Fatalf("%d responses=%d", rec.Code, f2.n("/v1/responses"))
	}
	if wrongEndpoint(200, []byte("use v1/responses")) || wrongEndpoint(503, []byte("use v1/responses")) {
		t.Error("only 4xx switches")
	}
}

func TestDeclaredAPIsSelectSingleEndpoint(t *testing.T) {
	f := &multiFake{rules: map[string]func(http.ResponseWriter){
		"/v1/chat/completions": chatOK,
		"/v1/responses":        respOK,
		"/v1/messages":         chatOK,
	}}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"gpt-x", "claude-x"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("fake", up.URL, []catalog.Model{
		{ID: "gpt-x", Name: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", Name: "claude-x", APIs: []string{"chat"}},
	}); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("fake")
	if got := p.APIs("gpt-x"); len(got) != 1 || got[0] != provider.Responses {
		t.Fatalf("APIs = %v", got)
	}
	if p.APIs("zzz") != nil {
		t.Error("unknown model should be nil")
	}
	srv := New()
	for _, tc := range []struct{ path, model, body, hit string }{
		{"/v1/messages", "gpt-x", `{"model":"gpt-x","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`, "/v1/responses"},
		{"/v1/chat/completions", "gpt-x", `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`, "/v1/responses"},
		{"/v1/responses", "claude-x", `{"model":"claude-x","input":"hi"}`, "/v1/chat/completions"},
		{"/v1/messages", "claude-x", `{"model":"claude-x","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions"},
	} {
		before := map[string]int{}
		for _, k := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
			before[k] = f.n(k)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
		if rec.Code != 200 {
			t.Fatalf("%s %s: %d %s", tc.path, tc.model, rec.Code, rec.Body.String())
		}
		for k, n := range before {
			want := 0
			if k == tc.hit {
				want = 1
			}
			if f.n(k)-n != want {
				t.Errorf("%s %s: %s got %d requests, want %d", tc.path, tc.model, k, f.n(k)-n, want)
			}
		}
	}
}

func TestTranslationOrder(t *testing.T) {
	openai := provider.Provider{ID: "o", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}
	other := provider.Provider{ID: "x", Chat: "https://relay.example/v1", Responses: "https://relay.example/v1"}
	srv := New()
	first := func(p provider.Provider, model string) provider.Protocol {
		return srv.endpoints(p, model, provider.Anthropic)[0]
	}
	for _, m := range []string{"gpt-6-sol", "o4-mini", "codex-mini", "openai/gpt-6"} {
		if first(openai, m) != provider.Responses {
			t.Errorf("openai %s", m)
		}
	}
	if first(other, "gpt-6-sol") != provider.Chat || first(other, "openai/gpt-oss-120b") != provider.Chat {
		t.Error("other vendors prefer chat")
	}
	srv.rejectEndpoint("o", "gpt-6-sol", provider.Responses)
	if got := srv.endpoints(openai, "gpt-6-sol", provider.Anthropic); len(got) != 1 || got[0] != provider.Chat {
		t.Errorf("after rejection: %v", got)
	}
}
