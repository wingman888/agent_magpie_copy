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

// s2Upstream answers each endpoint from a script and counts the requests it got.
type s2Upstream struct {
	mu      sync.Mutex
	hits    map[string]int
	chat    int    // status for /chat/completions
	chatMsg string // error body for /chat/completions when chat >= 400
	resp    int    // status for /responses
	respMsg string // error body for /responses when resp >= 400
}

func (u *s2Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	u.mu.Lock()
	u.hits[r.URL.Path]++
	u.mu.Unlock()
	switch r.URL.Path {
	case "/v1/chat/completions":
		if u.chat >= 400 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(u.chat)
			io.WriteString(w, u.chatMsg)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"chat ok"}}]}`,
			`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			`data: [DONE]`,
		))
	case "/v1/responses":
		if u.resp >= 400 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(u.resp)
			io.WriteString(w, u.respMsg)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m","usage":{"input_tokens":0,"output_tokens":0}}}`,
			`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"responses ok"}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`,
		))
	default:
		http.NotFound(w, r)
	}
}

func (u *s2Upstream) count(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits[path]
}

func s2Serve(t *testing.T, p provider.Provider, u *s2Upstream) *httptest.Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	p.Key = "k"
	base := up.URL + "/v1"
	if p.Chat != "" {
		p.Chat = base
	}
	if p.Responses != "" {
		p.Responses = base
	}
	if p.Anthropic != "" {
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return up
}

func s2Call(path, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestS2EndpointChatRefusedMovesToResponses(t *testing.T) {
	u := &s2Upstream{hits: map[string]int{}, chat: 400,
		chatMsg: `{"error":{"message":"model s2ep-m is not accessible via the /chat/completions endpoint"}}`}
	s2Serve(t, provider.Provider{ID: "s2ep", Name: "S2", Models: []string{"s2ep-m"}, Chat: "x", Responses: "x"}, u)

	body := `{"model":"s2ep-m","messages":[{"role":"user","content":"hi"}]}`
	if code, out := s2Call("/v1/chat/completions", body); code != 200 || !strings.Contains(out, "responses ok") {
		t.Fatalf("first: %d %s", code, out)
	}
	if u.count("/v1/chat/completions") != 1 || u.count("/v1/responses") != 1 {
		t.Fatalf("first calls: chat=%d responses=%d", u.count("/v1/chat/completions"), u.count("/v1/responses"))
	}
	if code, out := s2Call("/v1/chat/completions", body); code != 200 || !strings.Contains(out, "responses ok") {
		t.Fatalf("second: %d %s", code, out)
	}
	if u.count("/v1/chat/completions") != 1 || u.count("/v1/responses") != 2 {
		t.Fatalf("second calls: chat=%d responses=%d", u.count("/v1/chat/completions"), u.count("/v1/responses"))
	}
}

func TestS2EndpointResponsesRefusedMovesToChat(t *testing.T) {
	u := &s2Upstream{hits: map[string]int{}, resp: 400,
		respMsg: `{"error":{"code":"unsupported_api_for_model","message":"model not supported"}}`}
	s2Serve(t, provider.Provider{ID: "s2ep", Name: "S2", Models: []string{"s2ep-m"}, Chat: "x", Responses: "x"}, u)

	body := `{"model":"s2ep-m","input":"hi"}`
	if code, out := s2Call("/v1/responses", body); code != 200 || !strings.Contains(out, "chat ok") {
		t.Fatalf("first: %d %s", code, out)
	}
	if code, out := s2Call("/v1/responses", body); code != 200 || !strings.Contains(out, "chat ok") {
		t.Fatalf("second: %d %s", code, out)
	}
	if u.count("/v1/responses") != 1 || u.count("/v1/chat/completions") != 2 {
		t.Fatalf("calls: responses=%d chat=%d", u.count("/v1/responses"), u.count("/v1/chat/completions"))
	}
}

func TestS2EndpointServerErrorDoesNotSwitch(t *testing.T) {
	u := &s2Upstream{hits: map[string]int{}, chat: 500, chatMsg: `use v1/responses`}
	s2Serve(t, provider.Provider{ID: "s2ep", Name: "S2", Models: []string{"s2ep-m"}, Chat: "x", Responses: "x"}, u)

	code, _ := s2Call("/v1/chat/completions", `{"model":"s2ep-m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 500 {
		t.Fatalf("status %d, want 500", code)
	}
	if u.count("/v1/responses") != 0 {
		t.Fatalf("responses was tried after a 500: %d", u.count("/v1/responses"))
	}
}

func TestS2EndpointDeclaredAPIsOnly(t *testing.T) {
	if err := catalog.SaveLive("s2decl", "http://x", []catalog.Model{
		{ID: "s2decl-gpt", APIs: []string{"responses"}},
		{ID: "s2decl-claude", APIs: []string{"chat"}},
	}); err != nil {
		t.Fatal(err)
	}
	u := &s2Upstream{hits: map[string]int{}}
	s2Serve(t, provider.Provider{ID: "s2decl", Name: "S2", Models: []string{"s2decl-gpt", "s2decl-claude"},
		Chat: "x", Responses: "x", Anthropic: "x"}, u)

	if code, _ := s2Call("/v1/chat/completions", `{"model":"s2decl-gpt","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("gpt via chat: %d", code)
	}
	if u.count("/v1/responses") != 1 || u.count("/v1/chat/completions") != 0 {
		t.Fatalf("gpt calls: responses=%d chat=%d", u.count("/v1/responses"), u.count("/v1/chat/completions"))
	}
	if code, _ := s2Call("/v1/messages", `{"model":"s2decl-claude","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("claude via anthropic: %d", code)
	}
	if u.count("/v1/chat/completions") != 1 || u.count("/v1/messages") != 0 {
		t.Fatalf("claude calls: chat=%d messages=%d", u.count("/v1/chat/completions"), u.count("/v1/messages"))
	}
}

func TestS2EndpointNotOnEndpointPhrases(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `model is not accessible via the /chat/completions endpoint`, true},
		{400, `{"code":"unsupported_api_for_model"}`, true},
		{400, `This model is not supported in the v1/responses endpoint`, true},
		{400, `use /v1/chat/completions instead`, true},
		{404, `model not accessible via the /responses endpoint`, true},
		{500, `use v1/responses`, false},
		{503, `not accessible via the /chat/completions endpoint`, false},
		{400, `messages: field required`, false},
		{400, `context length exceeded`, false},
		{200, `use v1/responses`, false},
	} {
		if got := notChatModel(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("notChatModel(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

func TestS2EndpointOpenAIModel(t *testing.T) {
	for model, want := range map[string]bool{
		"gpt-6-sol":           true,
		"o4-mini":             true,
		"openai/o4-mini":      true,
		"codex-mini":          true,
		"openai/gpt-oss-120b": true,
		"claude-sonnet-5-5":   false,
		"ollama":              false,
		"o":                   false,
		"opus-4":              false,
	} {
		if got := openAIModel(model); got != want {
			t.Errorf("openAIModel(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestS2EndpointPreference(t *testing.T) {
	copilot := provider.Provider{ID: "copilot", Chat: "https://api.githubcopilot.com", Responses: "https://api.githubcopilot.com", Anthropic: "https://api.githubcopilot.com"}
	openai := provider.Provider{ID: "s2pref-openai", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}
	other := provider.Provider{ID: "s2pref-other", Chat: "https://openrouter.example/v1", Responses: "https://openrouter.example/v1"}
	for _, tc := range []struct {
		p     provider.Provider
		model string
		want  []provider.Protocol
	}{
		{copilot, "gpt-6-sol", []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}},
		{copilot, "o4-mini", []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}},
		{copilot, "claude-sonnet-5-5", []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}},
		{openai, "gpt-6-sol", []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}},
		{other, "openai/gpt-oss-120b", []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}},
	} {
		if got := preference(tc.p, tc.model); len(got) != len(tc.want) || got[0] != tc.want[0] || got[1] != tc.want[1] {
			t.Errorf("preference(%s, %q) = %v, want %v", tc.p.ID, tc.model, got, tc.want)
		}
	}
}

func TestS2EndpointRefusalIsPerModel(t *testing.T) {
	s := New()
	p := provider.Provider{ID: "s2mem", Chat: "https://x/v1", Responses: "https://x/v1"}
	s.markRefused(p.ID, "s2mem-a", provider.Chat)
	if got := s.usable(p, "s2mem-a"); len(got) != 1 || got[0] != provider.Responses {
		t.Errorf("refused model usable = %v, want [responses]", got)
	}
	if got := s.usable(p, "s2mem-b"); len(got) != 2 {
		t.Errorf("other model usable = %v, want both", got)
	}
}
