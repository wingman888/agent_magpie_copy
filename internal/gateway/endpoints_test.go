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

// epFake serves the three endpoints; reject maps a path to the 4xx/5xx
// answer it gives instead of a normal reply.
type epFake struct {
	mu     sync.Mutex
	calls  map[string]int
	reject map[string][2]string // path → status, body
}

func newEPFake() *epFake {
	return &epFake{calls: map[string]int{}, reject: map[string][2]string{}}
}

func (f *epFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	f.mu.Lock()
	f.calls[r.URL.Path]++
	rej, bad := f.reject[r.URL.Path]
	f.mu.Unlock()
	if bad {
		w.Header().Set("Content-Type", "application/json")
		code := 400
		if rej[0] != "" {
			code = int(rej[0][0]-'0')*100 + int(rej[0][1]-'0')*10 + int(rej[0][2]-'0')
		}
		w.WriteHeader(code)
		io.WriteString(w, rej[1])
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	switch r.URL.Path {
	case "/v1/chat/completions":
		io.WriteString(w, sse(
			`data: {"id":"c1","model":"m","choices":[{"delta":{"role":"assistant","content":"chat ok"}}]}`,
			`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`))
	case "/v1/responses":
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"m"}}`,
			`data: {"type":"response.output_text.delta","delta":"responses ok"}`,
			`data: {"type":"response.completed","response":{"id":"r1","model":"m","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`))
	case "/v1/messages":
		io.WriteString(w, sse(
			`data: {"type":"message_start","message":{"id":"a1","model":"m","usage":{"input_tokens":1}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"anthropic ok"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`data: {"type":"message_stop"}`))
	default:
		http.NotFound(w, r)
	}
}

func (f *epFake) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

// epSetup adds a provider with all three endpoints on f; models are the
// vendor's list (with APIs) as last fetched.
func epSetup(t *testing.T, f *epFake, models []catalog.Model) http.Handler {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Chat: up.URL + "/v1", Responses: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	if len(models) > 0 {
		if err := catalog.SaveLive("fake", up.URL, models); err != nil {
			t.Fatal(err)
		}
	}
	return New().Handler()
}

func epPost(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const (
	chatBody = `{"model":"fake/m","messages":[{"role":"user","content":"hi"}]}`
	respBody = `{"model":"fake/m","input":"hi"}`
	antBody  = `{"model":"fake/m","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
)

func TestChatRejectedMovesToResponsesAndIsRemembered(t *testing.T) {
	f := newEPFake()
	f.reject["/v1/chat/completions"] = [2]string{"400", `{"error":{"message":"model \"m\" is not accessible via the /chat/completions endpoint"}}`}
	h := epSetup(t, f, nil)
	for i := 0; i < 2; i++ {
		code, body := epPost(h, "/v1/chat/completions", chatBody)
		if code != 200 || !strings.Contains(body, "responses ok") {
			t.Fatalf("round %d: %d %s", i, code, body)
		}
	}
	if f.count("/v1/chat/completions") != 1 || f.count("/v1/responses") != 2 || f.count("/v1/messages") != 0 {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestResponsesRejectedMovesToChat(t *testing.T) {
	f := newEPFake()
	f.reject["/v1/responses"] = [2]string{"400", `{"error":{"code":"unsupported_api_for_model","message":"nope"}}`}
	h := epSetup(t, f, nil)
	for i := 0; i < 2; i++ {
		code, body := epPost(h, "/v1/responses", respBody)
		if code != 200 || !strings.Contains(body, "chat ok") {
			t.Fatalf("round %d: %d %s", i, code, body)
		}
	}
	if f.count("/v1/responses") != 1 || f.count("/v1/chat/completions") != 2 {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestAnthropicRejectedMovesToChat(t *testing.T) {
	f := newEPFake()
	f.reject["/v1/messages"] = [2]string{"404", `{"error":{"message":"model is not accessible via the /v1/messages endpoint"}}`}
	h := epSetup(t, f, nil)
	for i := 0; i < 2; i++ {
		code, body := epPost(h, "/v1/messages", antBody)
		if code != 200 || !strings.Contains(body, "chat ok") {
			t.Fatalf("round %d: %d %s", i, code, body)
		}
	}
	if f.count("/v1/messages") != 1 || f.count("/v1/chat/completions") != 2 || f.count("/v1/responses") != 0 {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestRejectionIsPerModel(t *testing.T) {
	f := newEPFake()
	f.reject["/v1/chat/completions"] = [2]string{"400", `{"error":{"message":"not a chat model"}}`}
	h := epSetup(t, f, nil)
	epPost(h, "/v1/chat/completions", chatBody)
	delete(f.reject, "/v1/chat/completions")
	// another model of the same provider still starts at Chat
	code, body := epPost(h, "/v1/chat/completions", strings.Replace(chatBody, "fake/m", "fake/other", 1))
	if code != 200 || !strings.Contains(body, "chat ok") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestDeclaredAPIsSendOneRequest(t *testing.T) {
	models := []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat"}},
	}
	for _, tc := range []struct {
		path, body, model, want string
	}{
		{"/v1/messages", antBody, "gpt-x", "/v1/responses"},
		{"/v1/chat/completions", chatBody, "gpt-x", "/v1/responses"},
		{"/v1/responses", respBody, "claude-x", "/v1/chat/completions"},
		{"/v1/messages", antBody, "claude-x", "/v1/chat/completions"},
	} {
		f := newEPFake()
		h := epSetup(t, f, models)
		code, body := epPost(h, tc.path, strings.Replace(tc.body, "fake/m", "fake/"+tc.model, 1))
		if code != 200 {
			t.Fatalf("%s %s: %d %s", tc.path, tc.model, code, body)
		}
		total := 0
		for _, n := range f.calls {
			total += n
		}
		if total != 1 || f.count(tc.want) != 1 {
			t.Errorf("%s %s: calls %v, want one to %s", tc.path, tc.model, f.calls, tc.want)
		}
	}
}

func TestDeclaredAPIOnAgentProtocolIsRelayed(t *testing.T) {
	f := newEPFake()
	h := epSetup(t, f, []catalog.Model{{ID: "m", APIs: []string{"chat", "anthropic"}}})
	code, body := epPost(h, "/v1/messages", antBody)
	if code != 200 || !strings.Contains(body, "anthropic ok") || f.count("/v1/messages") != 1 {
		t.Fatalf("%d %s %v", code, body, f.calls)
	}
}

func TestServerErrorWithHintDoesNotSwitch(t *testing.T) {
	f := newEPFake()
	f.reject["/v1/chat/completions"] = [2]string{"500", `{"error":{"message":"use v1/responses"}}`}
	h := epSetup(t, f, nil)
	code, _ := epPost(h, "/v1/chat/completions", chatBody)
	if code != 500 || f.count("/v1/responses") != 0 {
		t.Fatalf("%d %v", code, f.calls)
	}
}

func TestNoEndpointLeftReturnsUpstreamError(t *testing.T) {
	f := newEPFake()
	msg := `{"error":{"message":"not a chat model"}}`
	f.reject["/v1/chat/completions"] = [2]string{"400", msg}
	f.reject["/v1/responses"] = [2]string{"400", `{"error":{"message":"unsupported_api_for_model"}}`}
	f.reject["/v1/messages"] = [2]string{"400", `{"error":{"message":"unsupported_api_for_model"}}`}
	h := epSetup(t, f, nil)
	code, body := epPost(h, "/v1/chat/completions", chatBody)
	if code != 400 || !strings.Contains(body, "unsupported_api_for_model") || strings.Contains(body, "chat model") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestCountTokensOnlyWhereAnthropicServes(t *testing.T) {
	f := newEPFake()
	f.reject["/v1/messages/count_tokens"] = [2]string{"400", `x`}
	h := epSetup(t, f, []catalog.Model{{ID: "m", APIs: []string{"chat"}}})
	code, body := epPost(h, "/v1/messages/count_tokens", antBody)
	if code != 200 || !strings.Contains(body, "input_tokens") || f.count("/v1/messages/count_tokens") != 0 {
		t.Fatalf("%d %s %v", code, body, f.calls)
	}
}

func TestCountTokensForwardedWhenAnthropicServes(t *testing.T) {
	f := newEPFake()
	h := epSetup(t, f, []catalog.Model{{ID: "m", APIs: []string{"anthropic"}}})
	epPost(h, "/v1/messages/count_tokens", antBody)
	if f.count("/v1/messages/count_tokens") != 1 {
		t.Fatalf("%v", f.calls)
	}
}

func TestEndpointOrder(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	s := New()
	all := func(host string) provider.Provider {
		return provider.Provider{ID: "x", Chat: "https://" + host + "/v1", Responses: "https://" + host + "/v1", Anthropic: "https://" + host}
	}
	chatFirst := []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
	respFirst := []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	same := func(a, b []provider.Protocol) bool { return strings.Join(protoStrings(a), ",") == strings.Join(protoStrings(b), ",") }
	for _, tc := range []struct {
		host, model string
		from        provider.Protocol
		want        []provider.Protocol
	}{
		{"api.openai.com", "gpt-6-sol", provider.Gemini, respFirst},
		{"api.openai.com", "o4-mini", provider.Gemini, respFirst},
		{"api.openai.com", "claude-x", provider.Gemini, respFirst},
		{"api.githubcopilot.com", "gpt-6-sol", provider.Gemini, respFirst},
		{"api.githubcopilot.com", "o4-mini", provider.Gemini, respFirst},
		{"api.githubcopilot.com", "claude-sonnet-5", provider.Gemini, chatFirst},
		{"openrouter.ai", "openai/gpt-oss-120b", provider.Gemini, chatFirst},
		{"openrouter.ai", "gpt-6-sol", provider.Gemini, chatFirst},
		{"openrouter.ai", "gpt-6-sol", provider.Responses, []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}},
	} {
		if got := s.endpoints(all(tc.host), tc.model, tc.from); !same(got, tc.want) {
			t.Errorf("%s %s from %s: %v, want %v", tc.host, tc.model, tc.from, got, tc.want)
		}
	}
	p := all("api.openai.com")
	s.reject("x", "gpt-6-sol", provider.Responses)
	if got := s.endpoints(p, "gpt-6-sol", provider.Gemini); !same(got, []provider.Protocol{provider.Chat, provider.Anthropic}) {
		t.Errorf("after reject: %v", got)
	}
	if got := s.endpoints(p, "o4-mini", provider.Gemini); !same(got, respFirst) {
		t.Errorf("other model: %v", got)
	}
}

func protoStrings(ps []provider.Protocol) []string {
	var out []string
	for _, p := range ps {
		out = append(out, string(p))
	}
	return out
}

func TestWrongEndpoint(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `model "x" is not accessible via the /chat/completions endpoint`, true},
		{400, `{"error":{"code":"unsupported_api_for_model"}}`, true},
		{404, `Use v1/chat/completions`, true},
		{400, `This model is not supported in the v1/responses endpoint`, true},
		{400, `messages: field required`, false},
		{500, `use v1/responses`, false},
		{200, `use v1/responses`, false},
		{429, `slow down`, false},
	} {
		if got := wrongEndpoint(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("wrongEndpoint(%d, %q) = %v", tc.status, tc.body, got)
		}
	}
}
