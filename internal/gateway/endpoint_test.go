package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// scriptedUpstream is an upstream that answers each path from its own script and
// counts the requests per path.
type scriptedUpstream struct {
	mu     sync.Mutex
	routes map[string]scripted
	calls  map[string]int
}

type scripted struct {
	code  int
	reply string
}

func (f *scriptedUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls[r.URL.Path]++
	rt, ok := f.routes[r.URL.Path]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(rt.reply, "data:") || strings.HasPrefix(rt.reply, "event:") {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	if rt.code != 0 {
		w.WriteHeader(rt.code)
	}
	io.WriteString(w, rt.reply)
}

func (f *scriptedUpstream) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func (f *scriptedUpstream) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = map[string]int{}
}

const (
	chatSSEReply = "data: {\"id\":\"c1\",\"model\":\"m1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n" +
		"data: [DONE]\n\n"
	responsesSSEReply = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"model\":\"m1\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"m1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":4,\"output_tokens\":2}}}\n\n"
	refusedChat = `{"error":{"message":"model m1 is not accessible via the /chat/completions endpoint"}}`
)

// endpointUpstream serves Chat and Responses under one base, the way a
// vendor with both endpoints does.
func endpointUpstream(t *testing.T, f http.Handler, p provider.Provider) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p.Chat = up.URL + "/v1"
	p.Responses = up.URL + "/v1"
	p.Anthropic = up.URL
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func callOn(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// Acceptance 1: a Chat client, Chat refuses the model; Responses answers, and
// the next request goes straight to Responses.
func TestChatClientMovesToResponsesAndRemembers(t *testing.T) {
	f := &scriptedUpstream{calls: map[string]int{}, routes: map[string]scripted{
		"/v1/chat/completions": {400, refusedChat},
		"/v1/responses":        {0, responsesSSEReply},
	}}
	endpointUpstream(t, f, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}})
	h := New().Handler()
	chatReq := `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
	code, body := callOn(h, "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, `"ok"`) {
		t.Fatalf("first: %d %s", code, body)
	}
	if f.count("/v1/chat/completions") != 1 || f.count("/v1/responses") != 1 {
		t.Fatalf("first calls: chat=%d responses=%d", f.count("/v1/chat/completions"), f.count("/v1/responses"))
	}
	code, body = callOn(h, "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, `"ok"`) {
		t.Fatalf("second: %d %s", code, body)
	}
	if f.count("/v1/chat/completions") != 1 || f.count("/v1/responses") != 2 {
		t.Fatalf("second calls: chat=%d responses=%d", f.count("/v1/chat/completions"), f.count("/v1/responses"))
	}
}

// Acceptance 2: a Responses client, /responses refuses the model; Chat
// answers, and the next request goes straight to Chat.
func TestResponsesClientMovesToChatAndRemembers(t *testing.T) {
	f := &scriptedUpstream{calls: map[string]int{}, routes: map[string]scripted{
		"/v1/responses":        {400, `{"error":{"code":"unsupported_api_for_model","message":"model m1 is not supported"}}`},
		"/v1/chat/completions": {0, chatSSEReply},
	}}
	endpointUpstream(t, f, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}})
	h := New().Handler()
	req := `{"model":"m1","input":"hi"}`
	code, body := callOn(h, "/v1/responses", req)
	if code != 200 || !strings.Contains(body, `"ok"`) {
		t.Fatalf("first: %d %s", code, body)
	}
	if f.count("/v1/responses") != 1 || f.count("/v1/chat/completions") != 1 {
		t.Fatalf("first calls: responses=%d chat=%d", f.count("/v1/responses"), f.count("/v1/chat/completions"))
	}
	code, body = callOn(h, "/v1/responses", req)
	if code != 200 || !strings.Contains(body, `"ok"`) {
		t.Fatalf("second: %d %s", code, body)
	}
	if f.count("/v1/responses") != 1 || f.count("/v1/chat/completions") != 2 {
		t.Fatalf("second calls: responses=%d chat=%d", f.count("/v1/responses"), f.count("/v1/chat/completions"))
	}
}

// Acceptance 3: a vendor list that says where each model is served sends
// every client's request to that one endpoint, with no probing.
func TestDeclaredAPIsAreTheOnlyEndpoints(t *testing.T) {
	f := &scriptedUpstream{calls: map[string]int{}, routes: map[string]scripted{
		"/v1/chat/completions": {0, chatSSEReply},
		"/v1/responses":        {0, responsesSSEReply},
		"/v1/messages":         {0, `{"id":"a1","type":"message","role":"assistant","model":"claude-x","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`},
	}}
	// endpointUpstream sets the cache directory, so the list goes in after it
	endpointUpstream(t, f, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"gpt-x", "claude-x"}})
	if err := catalog.SaveLive("fake", "https://fake.test", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat"}},
	}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	for _, tc := range []struct {
		name, path, body string
		only             string // the one endpoint the upstream may see
	}{
		{"anthropic client, gpt-x", "/v1/messages", `{"model":"gpt-x","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, "/v1/responses"},
		{"chat client, gpt-x", "/v1/chat/completions", `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`, "/v1/responses"},
		{"responses client, claude-x", "/v1/responses", `{"model":"claude-x","input":"hi"}`, "/v1/chat/completions"},
		{"anthropic client, claude-x", "/v1/messages", `{"model":"claude-x","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.reset()
			if code, body := callOn(h, tc.path, tc.body); code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			total := 0
			for path, n := range f.calls {
				total += n
				if path != tc.only && n != 0 {
					t.Errorf("%s was probed %d times", path, n)
				}
			}
			if f.count(tc.only) != 1 || total != 1 {
				t.Errorf("calls: %v, want one to %s", f.calls, tc.only)
			}
		})
	}
}

// Acceptance 4: a 5xx that names another endpoint does not move the request.
func TestServerErrorDoesNotMoveEndpoint(t *testing.T) {
	f := &scriptedUpstream{calls: map[string]int{}, routes: map[string]scripted{
		"/v1/chat/completions": {500, `{"error":{"message":"upstream broke; use v1/responses"}}`},
		"/v1/responses":        {0, responsesSSEReply},
	}}
	endpointUpstream(t, f, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}})
	code, body := callOn(New().Handler(), "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 500 || !strings.Contains(body, "upstream broke") {
		t.Fatalf("%d %s", code, body)
	}
	if f.count("/v1/responses") != 0 {
		t.Fatal("a 5xx moved the request to another endpoint")
	}
}

// With no other endpoint to try, the refusal is the answer, as it came.
func TestRefusalWithNoOtherEndpointReachesAgent(t *testing.T) {
	f := &scriptedUpstream{calls: map[string]int{}, routes: map[string]scripted{
		"/v1/chat/completions": {400, refusedChat},
	}}
	endpointUpstream(t, f, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}})
	code, body := callOn(New().Handler(), "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || !strings.Contains(body, "not accessible via the /chat/completions endpoint") {
		t.Fatalf("%d %s", code, body)
	}
}

// Translation order: OpenAI's own models try Responses first, others Chat.
func TestPreferredEndpointOrder(t *testing.T) {
	copilot := provider.Provider{ID: "copilot", Chat: "https://api.githubcopilot.com", Responses: "https://api.githubcopilot.com", Anthropic: "https://api.githubcopilot.com"}
	for _, tc := range []struct {
		name  string
		p     provider.Provider
		model string
		want  provider.Protocol
	}{
		{"openai direct", provider.Provider{Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}, "gpt-6-sol", provider.Responses},
		{"openai o-series direct", provider.Provider{Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}, "o4-mini", provider.Responses},
		{"copilot gpt", copilot, "gpt-6-sol", provider.Responses},
		{"copilot prefixed o-series", copilot, "openai/o4-mini", provider.Responses},
		{"copilot claude", copilot, "claude-sonnet-5-5", provider.Chat},
		{"other vendor's gpt-oss", provider.Provider{Chat: "https://example.test/v1", Responses: "https://example.test/v1"}, "openai/gpt-oss-120b", provider.Chat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			if got := preferred(tc.p, tc.model)[0]; got != tc.want {
				t.Errorf("first = %s, want %s", got, tc.want)
			}
		})
	}
}
