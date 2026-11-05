package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

type apiReply struct {
	code  int
	ctype string
	body  string
}

// apiFake answers each upstream API path with its reply, and counts the calls
// each path gets.
type apiFake struct {
	mu      sync.Mutex
	replies map[string]apiReply
	hits    map[string]int
}

func newAPIFake(replies map[string]apiReply) *apiFake {
	return &apiFake{replies: replies, hits: map[string]int{}}
}

func (f *apiFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	f.mu.Lock()
	f.hits[r.URL.Path]++
	rp, ok := f.replies[r.URL.Path]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", rp.ctype)
	w.WriteHeader(rp.code)
	io.WriteString(w, rp.body)
}

func (f *apiFake) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

var (
	respOKBody = sse(
		`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"fallback ok"}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`,
	)
	chatOKBody = sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"chat ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
		`data: [DONE]`,
	)
	respOK      = apiReply{200, "text/event-stream", respOKBody}
	chatOK      = apiReply{200, "text/event-stream", chatOKBody}
)

const (
	pathChat     = "/v1/chat/completions"
	pathResp     = "/v1/responses"
	pathAnthropic = "/v1/messages"
	pathCount    = "/v1/messages/count_tokens"

	chatRefusal = `{"error":{"message":"model m1 is not accessible via the /chat/completions endpoint"}}`
	respRefusal = `{"error":{"code":"unsupported_api_for_model","message":"model m1 does not support this API"}}`
)

// apiSetup saves a provider whose bases are set to the fake (a non-empty field
// of p marks the API as offered) and, when declare is set, its live model list
// with the APIs each model declares.
func apiSetup(t *testing.T, f *apiFake, p provider.Provider, declare []catalog.Model) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p.Key = "k"
	if p.Chat != "" {
		p.Chat = up.URL + "/v1"
	}
	if p.Responses != "" {
		p.Responses = up.URL + "/v1"
	}
	if p.Anthropic != "" {
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if declare != nil {
		if err := catalog.SaveLive(p.ID, up.URL, declare); err != nil {
			t.Fatal(err)
		}
	}
}

func postTo(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const (
	chatReqBody = `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
	respReqBody = `{"model":"m1","input":"hi"}`
	anthReqBody = `{"model":"m1","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
)

// Acceptance 1: Chat is refused, Responses answers, and the next request goes
// straight to Responses.
func TestChatRefusalMovesToResponsesAndIsRemembered(t *testing.T) {
	f := newAPIFake(map[string]apiReply{
		pathChat: {400, "application/json", chatRefusal},
		pathResp: respOK,
	})
	apiSetup(t, f, provider.Provider{ID: "fake", Name: "Fake", Models: []string{"m1"}, Chat: "x", Responses: "x"}, nil)
	h := New().Handler()
	code, body := postTo(h, "/v1/chat/completions", chatReqBody)
	if code != 200 || !strings.Contains(body, "fallback ok") {
		t.Fatalf("first: %d %s", code, body)
	}
	if f.count(pathChat) != 1 || f.count(pathResp) != 1 {
		t.Fatalf("first calls: chat=%d responses=%d", f.count(pathChat), f.count(pathResp))
	}
	code, body = postTo(h, "/v1/chat/completions", chatReqBody)
	if code != 200 || !strings.Contains(body, "fallback ok") {
		t.Fatalf("second: %d %s", code, body)
	}
	if f.count(pathChat) != 1 || f.count(pathResp) != 2 {
		t.Fatalf("second calls: chat=%d responses=%d", f.count(pathChat), f.count(pathResp))
	}
}

// Acceptance 2: the reverse. Responses is refused, Chat answers, and the next
// request goes straight to Chat.
func TestResponsesRefusalMovesToChatAndIsRemembered(t *testing.T) {
	f := newAPIFake(map[string]apiReply{
		pathResp: {400, "application/json", respRefusal},
		pathChat: chatOK,
	})
	apiSetup(t, f, provider.Provider{ID: "fake", Name: "Fake", Models: []string{"m1"}, Chat: "x", Responses: "x"}, nil)
	h := New().Handler()
	code, body := postTo(h, "/v1/responses", respReqBody)
	if code != 200 || !strings.Contains(body, "chat ok") {
		t.Fatalf("first: %d %s", code, body)
	}
	if f.count(pathResp) != 1 || f.count(pathChat) != 1 {
		t.Fatalf("first calls: responses=%d chat=%d", f.count(pathResp), f.count(pathChat))
	}
	code, body = postTo(h, "/v1/responses", respReqBody)
	if code != 200 || !strings.Contains(body, "chat ok") {
		t.Fatalf("second: %d %s", code, body)
	}
	if f.count(pathResp) != 1 || f.count(pathChat) != 2 {
		t.Fatalf("second calls: responses=%d chat=%d", f.count(pathResp), f.count(pathChat))
	}
}

// Acceptance 3: a declared API list decides where a model goes, with no probe
// of the APIs it does not list, on every client protocol.
func TestDeclaredAPIsAreTheOnlyOnesAsked(t *testing.T) {
	f := newAPIFake(map[string]apiReply{
		pathChat: chatOK,
		pathResp: respOK,
		pathAnthropic: {200, "application/json", `{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"m1"}`},
	})
	declare := []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat"}},
	}
	apiSetup(t, f, provider.Provider{ID: "fake", Name: "Fake", Models: []string{"gpt-x", "claude-x"},
		Chat: "x", Responses: "x", Anthropic: "x"}, declare)
	h := New().Handler()
	reset := func() {
		f.mu.Lock()
		f.hits = map[string]int{}
		f.mu.Unlock()
	}
	check := func(name, path, body string, wantPath string) {
		t.Helper()
		reset()
		code, out := postTo(h, path, body)
		if code != 200 {
			t.Fatalf("%s: %d %s", name, code, out)
		}
		for _, p := range []string{pathChat, pathResp, pathAnthropic} {
			want := 0
			if p == wantPath {
				want = 1
			}
			if got := f.count(p); got != want {
				t.Errorf("%s: upstream %s got %d calls, want %d", name, p, got, want)
			}
		}
	}
	check("chat client gpt-x", "/v1/chat/completions", `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`, pathResp)
	check("anthropic client gpt-x", "/v1/messages", `{"model":"gpt-x","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, pathResp)
	check("responses client claude-x", "/v1/responses", `{"model":"claude-x","input":"hi"}`, pathChat)
	check("anthropic client claude-x", "/v1/messages", `{"model":"claude-x","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, pathChat)
}

// A 5xx that names another API is an outage, not a refusal: no other API is
// asked, and the error goes to the client.
func TestServerErrorNamingAnotherAPIDoesNotMove(t *testing.T) {
	f := newAPIFake(map[string]apiReply{
		pathChat: {500, "application/json", `{"error":{"message":"internal; use v1/responses"}}`},
		pathResp: respOK,
	})
	apiSetup(t, f, provider.Provider{ID: "fake", Name: "Fake", Models: []string{"m1"}, Chat: "x", Responses: "x"}, nil)
	code, body := postTo(New().Handler(), "/v1/chat/completions", chatReqBody)
	if code != 500 {
		t.Fatalf("status %d: %s", code, body)
	}
	// the lone candidate's 500 is tried again twice (retryDelays), still on Chat only
	if f.count(pathChat) != 3 || f.count(pathResp) != 0 {
		t.Fatalf("calls: chat=%d responses=%d", f.count(pathChat), f.count(pathResp))
	}
}

// An Anthropic request that the Anthropic API refuses is translated to Chat
// Completions, and the client gets Chat's answer.
func TestAnthropicClientMovesOffRefusingAnthropicAPI(t *testing.T) {
	f := newAPIFake(map[string]apiReply{
		pathAnthropic: {400, "application/json", `{"error":{"message":"model m1 is not accessible via the /v1/messages endpoint"}}`},
		pathChat:     chatOK,
	})
	apiSetup(t, f, provider.Provider{ID: "fake", Name: "Fake", Models: []string{"m1"}, Chat: "x", Anthropic: "x"}, nil)
	code, body := postTo(New().Handler(), "/v1/messages", anthReqBody)
	if code != 200 || !strings.Contains(body, "chat ok") {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.count(pathAnthropic) != 1 || f.count(pathChat) != 1 {
		t.Fatalf("calls: anthropic=%d chat=%d", f.count(pathAnthropic), f.count(pathChat))
	}
}

// count_tokens reaches the upstream only for a model that is served on the
// Anthropic API; otherwise it is estimated locally.
func TestCountTokensOnlyForAnthropicModels(t *testing.T) {
	f := newAPIFake(map[string]apiReply{
		pathCount: {200, "application/json", `{"input_tokens":7}`},
	})
	declare := []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat", "anthropic"}},
	}
	apiSetup(t, f, provider.Provider{ID: "fake", Name: "Fake", Models: []string{"gpt-x", "claude-x"},
		Responses: "x", Anthropic: "x"}, declare)
	h := New().Handler()
	code, _ := postTo(h, "/v1/messages/count_tokens", `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || f.count(pathCount) != 0 {
		t.Fatalf("gpt-x: status %d, upstream count calls %d", code, f.count(pathCount))
	}
	code, _ = postTo(h, "/v1/messages/count_tokens", `{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || f.count(pathCount) != 1 {
		t.Fatalf("claude-x: status %d, upstream count calls %d", code, f.count(pathCount))
	}
}

// Acceptance 9: which API a translated request tries first.
func TestTranslationOrderPrefersByVendorAndModel(t *testing.T) {
	openai := provider.Provider{ID: "openai", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}
	copilot := provider.Provider{ID: "copilot", Account: &provider.Account{Agent: "copilot"},
		Chat: "https://api.githubcopilot.com", Responses: "https://api.githubcopilot.com", Anthropic: "https://api.githubcopilot.com"}
	other := provider.Provider{ID: "other", Chat: "https://openrouter.example/api/v1", Responses: "https://openrouter.example/api/v1"}
	chatFirst := []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
	responsesFirst := []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	for _, tc := range []struct {
		name  string
		p     provider.Provider
		model string
		want  []provider.Protocol
	}{
		{"openai gpt", openai, "gpt-6-sol", responsesFirst},
		{"openai o-series", openai, "o4-mini", responsesFirst},
		{"openai codex", openai, "codex-mini", responsesFirst},
		{"openai vendor prefix", openai, "vendor/gpt-6-sol", responsesFirst},
		{"copilot gpt", copilot, "gpt-6-sol", responsesFirst},
		{"copilot o-series", copilot, "o4-mini", responsesFirst},
		{"copilot claude", copilot, "claude-x", chatFirst},
		{"other vendor gpt-oss", other, "openai/gpt-oss-120b", chatFirst},
		{"other vendor gpt", other, "gpt-6-sol", chatFirst},
	} {
		if got := translationOrder(tc.p, tc.model); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Refusals are per provider, model and API: refusing m1 on Chat leaves m2 and
// the other APIs alone, and when every API has refused a model they are all
// tried again.
func TestRefusalsAreScopedToModelAndAPI(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	s := New()
	p := provider.Provider{ID: "fake", Chat: "http://x/v1", Responses: "http://x/v1", Models: []string{"m1", "m2"}}
	s.refuse("fake", "m1", provider.Chat)
	if got := s.endpoints(p, "m1", provider.Chat, false); !slices.Equal(got, []provider.Protocol{provider.Responses}) {
		t.Errorf("m1 after refusal: %v", got)
	}
	if got := s.endpoints(p, "m2", provider.Chat, false); !slices.Equal(got, []provider.Protocol{provider.Chat, provider.Responses}) {
		t.Errorf("m2: %v", got)
	}
	s.refuse("fake", "m1", provider.Responses)
	if got := s.endpoints(p, "m1", provider.Chat, false); !slices.Equal(got, []provider.Protocol{provider.Chat, provider.Responses}) {
		t.Errorf("m1 with every API refused: %v", got)
	}
}

func TestRejectsEndpointOnlyFor4xx(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `model m1 is not accessible via the /chat/completions endpoint`, true},
		{400, `{"code":"unsupported_api_for_model"}`, true},
		{400, `此模型不存在 use /v1/chat/completions`, true},
		{422, `not supported in the v1/responses API`, true},
		{404, `not a chat model`, true},
		{400, `messages: field required`, false},
		{400, `context length exceeded`, false},
		{500, `use v1/responses`, false},
		{503, `not accessible via the /chat/completions endpoint`, false},
		{200, `use v1/responses`, false},
	} {
		if got := rejectsEndpoint(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("rejectsEndpoint(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}
