package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

const fbChatOK = `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`

// fbReply is what a modelUpstream answers one model with.
type fbReply struct {
	code  int
	ctype string
	body  string
}

// modelUpstream answers each request by the model the request names. A model
// that is not in replies gets a 200 with a chat completion. It records the
// models it was asked for.
type modelUpstream struct {
	mu      sync.Mutex
	replies map[string]fbReply
	seen    []string
}

func (m *modelUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	model := modelOf(b)
	m.mu.Lock()
	m.seen = append(m.seen, model)
	rp, ok := m.replies[model]
	m.mu.Unlock()
	if !ok {
		rp = fbReply{code: http.StatusOK, body: fbChatOK}
	}
	if rp.code == 0 {
		rp.code = http.StatusOK
	}
	if rp.ctype == "" {
		rp.ctype = "application/json"
	}
	w.Header().Set("Content-Type", rp.ctype)
	w.WriteHeader(rp.code)
	io.WriteString(w, rp.body)
}

// asked lists the models the upstream was asked for, in order.
func (m *modelUpstream) asked() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.seen...)
}

// isolate points magpie's config and cache at temp dirs.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// saveChat serves up at a URL, saves p as a Chat provider reaching it, and
// returns the upstream server.
func saveChat(t *testing.T, p provider.Provider, up http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p.Chat = srv.URL + "/v1"
	if p.Key == "" {
		p.Key = "sk-" + p.ID
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return srv
}

// postChat sends a Chat Completions body to the gateway.
func postChat(s *Server, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

const fbRequest = `{"model":"pri/m1","messages":[{"role":"user","content":"hi"}]}`

func TestRateLimitedProviderFallsBackWithItsOwnModelName(t *testing.T) {
	isolate(t)
	pri := &modelUpstream{replies: map[string]fbReply{"m1": {code: 429, body: `{"error":{"message":"rate limited"}}`}}}
	bak := &modelUpstream{}
	saveChat(t, provider.Provider{ID: "pri", Name: "Pri", Models: []string{"m1"}, Fallback: []string{"bak/m2"}}, pri)
	saveChat(t, provider.Provider{ID: "bak", Name: "Bak", Models: []string{"m2"}}, bak)

	s := New()
	code, body := postChat(s, fbRequest)
	if code != 200 || !strings.Contains(body, "hi") {
		t.Fatalf("%d %s", code, body)
	}
	if want := []string{"m1"}; !reflect.DeepEqual(pri.asked(), want) {
		t.Fatalf("pri asked for %v, want %v", pri.asked(), want)
	}
	if want := []string{"m2"}; !reflect.DeepEqual(bak.asked(), want) {
		t.Fatalf("bak asked for %v, want the fallback's own name %v", bak.asked(), want)
	}
	call := s.Recent()[0]
	if call.Provider != "bak" || !strings.Contains(call.Fallback, "pri") {
		t.Fatalf("call record: provider %q fallback %q", call.Provider, call.Fallback)
	}
}

func TestBadRequestIsReturnedWithoutFallback(t *testing.T) {
	isolate(t)
	pri := &modelUpstream{replies: map[string]fbReply{"m1": {code: 400, body: `{"error":{"message":"bad field"}}`}}}
	bak := &modelUpstream{}
	saveChat(t, provider.Provider{ID: "pri", Name: "Pri", Models: []string{"m1"}, Fallback: []string{"bak/m2"}}, pri)
	saveChat(t, provider.Provider{ID: "bak", Name: "Bak", Models: []string{"m2"}}, bak)

	code, body := postChat(New(), fbRequest)
	if code != 400 || !strings.Contains(body, "bad field") {
		t.Fatalf("%d %s; want the 400 as it is", code, body)
	}
	if len(bak.asked()) != 0 {
		t.Fatalf("fallback was asked after a 400: %v", bak.asked())
	}
}

func TestLastCandidatesErrorIsReturned(t *testing.T) {
	isolate(t)
	pri := &modelUpstream{replies: map[string]fbReply{"m1": {code: 429, body: `{"error":{"message":"pri limited"}}`}}}
	bak := &modelUpstream{replies: map[string]fbReply{"m2": {code: 503, body: `{"error":{"message":"bak down"}}`}}}
	saveChat(t, provider.Provider{ID: "pri", Name: "Pri", Models: []string{"m1"}, Fallback: []string{"bak/m2"}}, pri)
	saveChat(t, provider.Provider{ID: "bak", Name: "Bak", Models: []string{"m2"}}, bak)

	s := New()
	code, body := postChat(s, fbRequest)
	if code != 503 || !strings.Contains(body, "bak down") {
		t.Fatalf("%d %s; want the last candidate's 503", code, body)
	}
	if call := s.Recent()[0]; call.Status != 503 || call.Provider != "bak" {
		t.Fatalf("call record: %+v", call)
	}
}

func TestFailedProviderIsSkippedForAMinute(t *testing.T) {
	isolate(t)
	pri := &modelUpstream{replies: map[string]fbReply{"m1": {code: 429, body: `{"error":{"message":"rate limited"}}`}}}
	bak := &modelUpstream{}
	saveChat(t, provider.Provider{ID: "pri", Name: "Pri", Models: []string{"m1"}, Fallback: []string{"bak/m2"}}, pri)
	saveChat(t, provider.Provider{ID: "bak", Name: "Bak", Models: []string{"m2"}}, bak)

	s := New()
	if code, body := postChat(s, fbRequest); code != 200 {
		t.Fatalf("first request: %d %s", code, body)
	}
	// the next request goes to the fallback without asking the failed provider
	if code, body := postChat(s, fbRequest); code != 200 {
		t.Fatalf("second request: %d %s", code, body)
	}
	if want := []string{"m1"}; !reflect.DeepEqual(pri.asked(), want) {
		t.Fatalf("pri asked for %v, want %v: it should be skipped during the cooldown", pri.asked(), want)
	}
	if want := []string{"m2", "m2"}; !reflect.DeepEqual(bak.asked(), want) {
		t.Fatalf("bak asked for %v, want %v", bak.asked(), want)
	}
	if last := s.Recent()[0]; last.Fallback != "" || last.Provider != "bak" {
		t.Fatalf("second request record: %+v", last)
	}
}

func TestUnresolvableFallbackEntriesAreSkipped(t *testing.T) {
	isolate(t)
	pri := &modelUpstream{replies: map[string]fbReply{"m1": {code: 429, body: `{"error":{"message":"rate limited"}}`}}}
	bak := &modelUpstream{}
	saveChat(t, provider.Provider{ID: "pri", Name: "Pri", Models: []string{"m1"}, Fallback: []string{"nope/x", "bak/m2"}}, pri)
	saveChat(t, provider.Provider{ID: "bak", Name: "Bak", Models: []string{"m2"}}, bak)

	if code, body := postChat(New(), fbRequest); code != 200 {
		t.Fatalf("%d %s; the unknown entry should be skipped", code, body)
	}
	if want := []string{"m2"}; !reflect.DeepEqual(bak.asked(), want) {
		t.Fatalf("bak asked for %v, want %v", bak.asked(), want)
	}
}

func TestEveryKeyIsTriedBeforeTheFallback(t *testing.T) {
	isolate(t)
	pri := &modelUpstream{replies: map[string]fbReply{"m1": {code: 429, body: `{"error":{"message":"rate limited"}}`}}}
	bak := &modelUpstream{}
	saveChat(t, provider.Provider{ID: "pri", Name: "Pri", Key: "sk-1", KeyName: "personal", Models: []string{"m1"},
		Fallback: []string{"bak/m2"}}, pri)
	saveChat(t, provider.Provider{ID: "bak", Name: "Bak", Models: []string{"m2"}}, bak)
	if err := provider.AddKey("pri", "team", "sk-2", ""); err != nil {
		t.Fatal(err)
	}

	s := New()
	if code, body := postChat(s, fbRequest); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if want := []string{"m1", "m1"}; !reflect.DeepEqual(pri.asked(), want) {
		t.Fatalf("pri asked for %v, want both keys %v", pri.asked(), want)
	}
	call := s.Recent()[0]
	if call.Provider != "bak" || !strings.Contains(call.Fallback, "pri (key personal)") || !strings.Contains(call.Fallback, "pri (key team)") {
		t.Fatalf("call record: provider %q fallback %q", call.Provider, call.Fallback)
	}
}

func TestStreamOpeningErrorFallsBack(t *testing.T) {
	isolate(t)
	pri := &modelUpstream{replies: map[string]fbReply{"m1": {ctype: "text/event-stream",
		body: "data: {\"error\":{\"message\":\"Rate limit reached for requests\"}}\n\n"}}}
	bak := &modelUpstream{replies: map[string]fbReply{"m2": {ctype: "text/event-stream",
		body: "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"}}}
	saveChat(t, provider.Provider{ID: "pri", Name: "Pri", Models: []string{"m1"}, Fallback: []string{"bak/m2"}}, pri)
	saveChat(t, provider.Provider{ID: "bak", Name: "Bak", Models: []string{"m2"}}, bak)

	s := New()
	code, body := postChat(s, `{"model":"pri/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hi") || strings.Contains(body, "Rate limit") {
		t.Fatalf("%d %s; want the fallback's stream only", code, body)
	}
	if call := s.Recent()[0]; call.Provider != "bak" || !strings.Contains(call.Fallback, "pri") {
		t.Fatalf("call record: %+v", call)
	}
}

func TestPlainStreamErrorIsNotRetried(t *testing.T) {
	isolate(t)
	pri := &modelUpstream{replies: map[string]fbReply{"m1": {ctype: "text/event-stream",
		body: "data: {\"error\":{\"message\":\"invalid tool schema\"}}\n\n"}}}
	bak := &modelUpstream{}
	saveChat(t, provider.Provider{ID: "pri", Name: "Pri", Models: []string{"m1"}, Fallback: []string{"bak/m2"}}, pri)
	saveChat(t, provider.Provider{ID: "bak", Name: "Bak", Models: []string{"m2"}}, bak)

	code, body := postChat(New(), `{"model":"pri/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "invalid tool schema") {
		t.Fatalf("%d %s; want the stream relayed as it was", code, body)
	}
	if len(bak.asked()) != 0 {
		t.Fatalf("fallback was asked for a non-retryable error: %v", bak.asked())
	}
}

func TestNoFallbackKeepsSingleProviderBehaviour(t *testing.T) {
	isolate(t)
	pri := &modelUpstream{replies: map[string]fbReply{"m1": {code: 429, body: `{"error":{"message":"rate limited"}}`}}}
	saveChat(t, provider.Provider{ID: "pri", Name: "Pri", Models: []string{"m1"}}, pri)

	s := New()
	code, body := postChat(s, fbRequest)
	if code != 429 || !strings.Contains(body, "rate limited") {
		t.Fatalf("%d %s", code, body)
	}
	if call := s.Recent()[0]; call.Fallback != "" || call.Provider != "pri" {
		t.Fatalf("call record: %+v", call)
	}
}

func TestCatalogsNameAndEffortsPrefixedModels(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "magpie"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", dir)
	fixture := `{
	  "openai": {"models": {
	    "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","reasoning_options":[{"type":"effort","values":["low","high"]}]}
	  }},
	  "deepseek": {"models": {
	    "deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat"}
	  }}
	}`
	if err := os.WriteFile(catalog.CachePath(), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	saveChat(t, provider.Provider{ID: "gw", Name: "GW", Catalog: "openai,deepseek",
		Models: []string{"deepseek/deepseek-chat", "gpt-5.5"}}, &modelUpstream{})
	entries := map[string]provider.Entry{}
	for _, e := range provider.Catalog() {
		entries[e.ID] = e
	}
	if d := entries["gw/deepseek/deepseek-chat"]; d.Name != "DeepSeek Chat" {
		t.Fatalf("deepseek entry: %+v", d)
	}
	g := entries["gw/gpt-5.5"]
	if g.Name != "GPT-5.5" || !reflect.DeepEqual(g.Efforts, []string{"low", "high"}) {
		t.Fatalf("gpt-5.5 entry: %+v", g)
	}
}
