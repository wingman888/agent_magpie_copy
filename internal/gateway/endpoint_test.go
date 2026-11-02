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

// epFake is an upstream with all three endpoints; rejects maps a path to the
// status and body it turns the model away with.
type epFake struct {
	mu      sync.Mutex
	hits    map[string]int
	rejects map[string][2]string // path → {status, body}
}

func (f *epFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	f.mu.Lock()
	if f.hits == nil {
		f.hits = map[string]int{}
	}
	f.hits[r.URL.Path]++
	rej, ok := f.rejects[r.URL.Path]
	f.mu.Unlock()
	if ok {
		w.Header().Set("Content-Type", "application/json")
		code := 400
		if rej[0] != "" {
			code = epAtoi(rej[0])
		}
		w.WriteHeader(code)
		io.WriteString(w, rej[1])
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	switch r.URL.Path {
	case "/v1/responses":
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"m","usage":{"input_tokens":0,"output_tokens":0}}}`,
			`data: {"type":"response.output_text.delta","delta":"via responses"}`,
			`data: {"type":"response.completed","response":{"id":"r1","model":"m","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`))
	case "/v1/messages":
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"a1","role":"assistant","model":"m","usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"via anthropic"}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
	default:
		io.WriteString(w, sse(
			`data: {"id":"c1","model":"m","choices":[{"delta":{"role":"assistant","content":"via chat"}}]}`,
			`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`))
	}
}

func epAtoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func (f *epFake) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

// epSetup saves provider "fake" with all three endpoints on the fake.
func epSetup(t *testing.T, f *epFake, id string) *Server {
	t.Helper()
	foFresh(t)
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: "Fake", Key: "k", Models: []string{"m", "gpt-x", "claude-x"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	s := New()
	s.retryBase = time.Millisecond
	return s
}

func epDo(s *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const epChatReq = `{"model":"%s","messages":[{"role":"user","content":"hi"}]}`

func epChat(model string) string { return strings.Replace(epChatReq, "%s", model, 1) }
func epResp(model string) string { return `{"model":"` + model + `","input":"hi"}` }
func epAnt(model string) string {
	return `{"model":"` + model + `","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
}

func TestChatRejectedSwitchesToResponsesAndRemembers(t *testing.T) {
	f := &epFake{rejects: map[string][2]string{"/v1/chat/completions": {"400", `{"error":{"message":"model \"m\" is not accessible via the /chat/completions endpoint"}}`}}}
	s := epSetup(t, f, "fake")
	// Chat only provider-side order: the agent speaks Chat, so Chat is tried first
	for i := 0; i < 2; i++ {
		code, body := epDo(s, "/v1/chat/completions", epChat("m"))
		if code != 200 || !strings.Contains(body, "via responses") {
			t.Fatalf("round %d: %d %s", i, code, body)
		}
	}
	if f.count("/v1/chat/completions") != 1 || f.count("/v1/responses") != 2 {
		t.Fatalf("chat=%d responses=%d", f.count("/v1/chat/completions"), f.count("/v1/responses"))
	}
	// another model is not affected by the memory
	if code, _ := epDo(s, "/v1/chat/completions", epChat("gpt-x")); code != 200 || f.count("/v1/chat/completions") != 2 {
		t.Errorf("other model should still probe chat: %d chat=%d", code, f.count("/v1/chat/completions"))
	}
}

func TestResponsesRejectedSwitchesToChat(t *testing.T) {
	f := &epFake{rejects: map[string][2]string{"/v1/responses": {"400", `{"error":{"code":"unsupported_api_for_model","message":"nope"}}`}}}
	s := epSetup(t, f, "fake")
	for i := 0; i < 2; i++ {
		code, body := epDo(s, "/v1/responses", epResp("m"))
		if code != 200 || !strings.Contains(body, "via chat") {
			t.Fatalf("round %d: %d %s", i, code, body)
		}
	}
	if f.count("/v1/responses") != 1 || f.count("/v1/chat/completions") != 2 {
		t.Fatalf("responses=%d chat=%d", f.count("/v1/responses"), f.count("/v1/chat/completions"))
	}
}

func TestAnthropicRejectedRelayThenTranslate(t *testing.T) {
	f := &epFake{rejects: map[string][2]string{"/v1/messages": {"400", `{"error":{"message":"use /v1/chat/completions"}}`}}}
	s := epSetup(t, f, "fake")
	code, body := epDo(s, "/v1/messages", epAnt("m"))
	if code != 200 || !strings.Contains(body, "via chat") {
		t.Fatalf("%d %s", code, body)
	}
	epDo(s, "/v1/messages", epAnt("m"))
	if f.count("/v1/messages") != 1 || f.count("/v1/chat/completions") != 2 {
		t.Fatalf("messages=%d chat=%d", f.count("/v1/messages"), f.count("/v1/chat/completions"))
	}
}

func TestServerErrorDoesNotSwitchEndpoint(t *testing.T) {
	f := &epFake{rejects: map[string][2]string{"/v1/chat/completions": {"500", `{"error":{"message":"use v1/responses"}}`}}}
	s := epSetup(t, f, "fake")
	code, _ := epDo(s, "/v1/chat/completions", epChat("m"))
	if code != 500 {
		t.Fatalf("status %d", code)
	}
	if f.count("/v1/responses") != 0 {
		t.Errorf("responses was probed")
	}
}

func TestRejectedEverywhereReturnsLastError(t *testing.T) {
	msg := `{"error":{"message":"not a chat model"}}`
	f := &epFake{rejects: map[string][2]string{
		"/v1/chat/completions": {"400", msg}, "/v1/responses": {"404", `{"error":{"message":"use /v1/chat/completions"}}`},
		"/v1/messages": {"403", `{"error":{"message":"model is not accessible via the /v1/messages endpoint"}}`}}}
	s := epSetup(t, f, "fake")
	code, body := epDo(s, "/v1/chat/completions", epChat("m"))
	if code != 403 || !strings.Contains(body, "not accessible via the /v1/messages") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestDeclaredAPIsAreNotProbed(t *testing.T) {
	f := &epFake{}
	s := epSetup(t, f, "fake")
	catalog.SaveLive("fake", "http://x", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}}, {ID: "claude-x", APIs: []string{"chat"}}})
	for _, tc := range []struct{ path, body, want string }{
		{"/v1/messages", epAnt("gpt-x"), "/v1/responses"},
		{"/v1/chat/completions", epChat("gpt-x"), "/v1/responses"},
		{"/v1/responses", epResp("claude-x"), "/v1/chat/completions"},
		{"/v1/messages", epAnt("claude-x"), "/v1/chat/completions"},
	} {
		f.mu.Lock()
		f.hits = map[string]int{}
		f.mu.Unlock()
		if code, body := epDo(s, tc.path, tc.body); code != 200 {
			t.Fatalf("%s: %d %s", tc.path, code, body)
		}
		total := 0
		f.mu.Lock()
		for _, n := range f.hits {
			total += n
		}
		f.mu.Unlock()
		if total != 1 || f.count(tc.want) != 1 {
			t.Errorf("%s %s: hits %v", tc.path, tc.want, f.hits)
		}
	}
}

func TestCountTokensOnlyWhenAnthropicServes(t *testing.T) {
	f := &epFake{}
	s := epSetup(t, f, "fake")
	catalog.SaveLive("fake", "http://x", []catalog.Model{{ID: "claude-x", APIs: []string{"chat"}}, {ID: "m", APIs: []string{"chat", "anthropic"}}})
	if code, _ := epDo(s, "/v1/messages/count_tokens", epAnt("claude-x")); code != 200 {
		t.Fatalf("status %d", code)
	}
	if f.count("/v1/messages/count_tokens") != 0 {
		t.Error("count_tokens forwarded for a chat-only model")
	}
	epDo(s, "/v1/messages/count_tokens", epAnt("m"))
	if f.count("/v1/messages/count_tokens") != 1 {
		t.Error("count_tokens not forwarded for an anthropic model")
	}
}

func TestRoutePriority(t *testing.T) {
	s := New()
	hosted := func(host string) provider.Provider {
		return provider.Provider{ID: "x", Chat: "https://" + host + "/v1", Responses: "https://" + host + "/v1"}
	}
	copilot := hosted("api.githubcopilot.com")
	copilot.Account = &provider.Account{Agent: "copilot"}
	copilot.Anthropic = "https://api.githubcopilot.com"
	other := hosted("relay.example.com")
	for _, tc := range []struct {
		name  string
		p     provider.Provider
		model string
		want  provider.Protocol
		next  provider.Protocol
	}{
		{"openai gpt", hosted("api.openai.com"), "gpt-6-sol", provider.Responses, provider.Chat},
		{"openai o4", hosted("api.openai.com"), "o4-mini", provider.Responses, provider.Chat},
		{"copilot gpt", copilot, "gpt-6-sol", provider.Responses, provider.Chat},
		{"copilot o4", copilot, "openai/o4-mini", provider.Responses, provider.Chat},
		{"copilot claude", copilot, "claude-x", provider.Chat, provider.Responses},
		{"other gpt-oss", other, "openai/gpt-oss-120b", provider.Chat, provider.Responses},
		{"other gpt", other, "gpt-5", provider.Chat, provider.Responses},
	} {
		got := s.route(tc.p, tc.model, provider.Gemini)
		if len(got) < 2 || got[0] != tc.want || got[1] != tc.next {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	// after a rejection only the remaining endpoints are left
	s.markRejected("x", "gpt-6-sol", provider.Responses)
	if got := s.route(hosted("api.openai.com"), "gpt-6-sol", provider.Gemini); len(got) != 1 || got[0] != provider.Chat {
		t.Errorf("after reject: %v", got)
	}
	if got := s.route(hosted("api.openai.com"), "gpt-other", provider.Gemini); got[0] != provider.Responses {
		t.Errorf("other model: %v", got)
	}
}

func TestAPIRejected(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `model "x" is not accessible via the /chat/completions endpoint`, true},
		{400, `{"code":"unsupported_api_for_model"}`, true},
		{404, `use /v1/chat/completions`, true},
		{400, `not supported in the v1/responses`, true},
		{400, `messages: field required`, false},
		{500, `use v1/responses`, false},
		{200, `use v1/responses`, false},
		{429, `use v1/responses`, false},
	} {
		if got := apiRejected(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("apiRejected(%d,%q)=%v", tc.status, tc.body, got)
		}
	}
}
