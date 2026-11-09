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

// s2Reply is what an s2Up answers on one path.
type s2Reply struct {
	code int
	body string
}

// s2Up is an upstream that counts the requests it gets by path. A path it
// has no reply for answers 404.
type s2Up struct {
	mu    sync.Mutex
	hits  map[string]int
	reply map[string]s2Reply
}

func (u *s2Up) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	u.mu.Lock()
	u.hits[r.URL.Path]++
	rep, ok := u.reply[r.URL.Path]
	u.mu.Unlock()
	if !ok {
		w.WriteHeader(404)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if rep.code != 0 {
		w.WriteHeader(rep.code)
	}
	io.WriteString(w, rep.body)
}

// s2Hits is how many requests each path got, and resets the count.
func (u *s2Up) s2Hits() map[string]int {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := u.hits
	u.hits = map[string]int{}
	return out
}

func (u *s2Up) s2Count(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits[path]
}

const s2ChatOK = "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"pong\"}}]}\n\ndata: [DONE]\n\n"

const s2ResponsesOK = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"pong\"}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"m\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"

// s2Serve starts an upstream with the given replies and saves provider p
// (ID "s2") pointed at it: each base p sets gets the upstream's URL.
func s2Serve(t *testing.T, replies map[string]s2Reply, p provider.Provider) (*s2Up, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	u := &s2Up{hits: map[string]int{}, reply: replies}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	if p.Chat != "" {
		p.Chat = up.URL + "/v1"
	}
	if p.Responses != "" {
		p.Responses = up.URL + "/v1"
	}
	if p.Anthropic != "" {
		p.Anthropic = up.URL
	}
	p.ID, p.Name, p.Key = "s2", "S2", "k"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return u, up.URL
}

// s2Call posts a request to a server magpie has been built into.
func s2Call(s *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// Chat client, model served on Responses only: Chat is refused, Responses
// answers, and the next request goes straight to Responses.
func TestS2ChatRefusedThenResponsesIsRemembered(t *testing.T) {
	u, _ := s2Serve(t, map[string]s2Reply{
		"/v1/chat/completions": {400, `{"error":{"message":"model gpt-x is not accessible via the /chat/completions endpoint"}}`},
		"/v1/responses":        {0, s2ResponsesOK},
	}, provider.Provider{Chat: "x", Responses: "x", Models: []string{"gpt-x"}})
	s := New()
	body := `{"model":"s2/gpt-x","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	code, out := s2Call(s, "/v1/chat/completions", body)
	if code != 200 || !strings.Contains(out, "pong") {
		t.Fatalf("first call: %d %s", code, out)
	}
	if h := u.s2Hits(); h["/v1/chat/completions"] != 1 || h["/v1/responses"] != 1 {
		t.Fatalf("first call hits: %v", h)
	}
	if code, out = s2Call(s, "/v1/chat/completions", body); code != 200 || !strings.Contains(out, "pong") {
		t.Fatalf("second call: %d %s", code, out)
	}
	if h := u.s2Hits(); len(h) != 1 || h["/v1/responses"] != 1 {
		t.Fatalf("second call should only reach responses: %v", h)
	}
}

// Responses client, model refused on Responses with unsupported_api_for_model:
// Chat answers, and the next request goes straight to Chat.
func TestS2ResponsesRefusedThenChatIsRemembered(t *testing.T) {
	u, _ := s2Serve(t, map[string]s2Reply{
		"/v1/responses":        {400, `{"error":{"code":"unsupported_api_for_model","message":"gpt-x is not supported"}}`},
		"/v1/chat/completions": {0, s2ChatOK},
	}, provider.Provider{Chat: "x", Responses: "x", Models: []string{"gpt-x"}})
	s := New()
	body := `{"model":"s2/gpt-x","stream":true,"input":"hi"}`
	if code, out := s2Call(s, "/v1/responses", body); code != 200 || !strings.Contains(out, "pong") {
		t.Fatalf("first call: %d %s", code, out)
	}
	if h := u.s2Hits(); h["/v1/responses"] != 1 || h["/v1/chat/completions"] != 1 {
		t.Fatalf("first call hits: %v", h)
	}
	if code, out := s2Call(s, "/v1/responses", body); code != 200 || !strings.Contains(out, "pong") {
		t.Fatalf("second call: %d %s", code, out)
	}
	if h := u.s2Hits(); len(h) != 1 || h["/v1/chat/completions"] != 1 {
		t.Fatalf("second call should only reach chat: %v", h)
	}
}

// A model list that names one API per model: each request reaches only that
// API, whatever the agent spoke and whatever else the provider has.
func TestS2DeclaredAPIsAreTheOnlyProbes(t *testing.T) {
	cases := []struct {
		name, path, model, want string
	}{
		{"anthropic client, responses-only model", "/v1/messages", "gpt-x", "/v1/responses"},
		{"chat client, responses-only model", "/v1/chat/completions", "gpt-x", "/v1/responses"},
		{"responses client, chat-only model", "/v1/responses", "claude-x", "/v1/chat/completions"},
		{"anthropic client, chat-only model", "/v1/messages", "claude-x", "/v1/chat/completions"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, url := s2Serve(t, map[string]s2Reply{
				"/v1/chat/completions": {0, s2ChatOK},
				"/v1/responses":        {0, s2ResponsesOK},
				"/v1/messages":         {0, s2ChatOK},
			}, provider.Provider{Chat: "x", Responses: "x", Anthropic: "x", Models: []string{"gpt-x", "claude-x"}})
			if err := catalog.SaveLive("s2", url, []catalog.Model{
				{ID: "gpt-x", APIs: []string{"responses"}},
				{ID: "claude-x", APIs: []string{"chat"}},
			}); err != nil {
				t.Fatal(err)
			}
			body := `{"model":"s2/` + c.model + `","stream":true,"max_tokens":9,"input":"hi","messages":[{"role":"user","content":"hi"}]}`
			code, out := s2Call(New(), c.path, body)
			if code != 200 || !strings.Contains(out, "pong") {
				t.Fatalf("%d %s", code, out)
			}
			if h := u.s2Hits(); len(h) != 1 || h[c.want] != 1 {
				t.Fatalf("hits %v, want one request to %s", h, c.want)
			}
		})
	}
}

// count_tokens goes to Anthropic only when the model can be sent there.
func TestS2CountTokensOnlyForAnthropicModels(t *testing.T) {
	u, url := s2Serve(t, map[string]s2Reply{
		"/v1/messages/count_tokens": {0, `{"input_tokens":7}`},
	}, provider.Provider{Chat: "x", Anthropic: "x", Models: []string{"gpt-x", "claude-x"}})
	if err := catalog.SaveLive("s2", url, []catalog.Model{
		{ID: "gpt-x", APIs: []string{"chat"}},
		{ID: "claude-x", APIs: []string{"anthropic"}},
	}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, _ := s2Call(s, "/v1/messages/count_tokens", `{"model":"s2/gpt-x","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("gpt-x count: %d", code)
	}
	if n := u.s2Count("/v1/messages/count_tokens"); n != 0 {
		t.Fatalf("a chat-only model was counted upstream %d times", n)
	}
	if code, out := s2Call(s, "/v1/messages/count_tokens", `{"model":"s2/claude-x","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(out, "7") {
		t.Fatalf("claude-x count: %d %s", code, out)
	}
	if n := u.s2Count("/v1/messages/count_tokens"); n != 1 {
		t.Fatalf("an Anthropic model should be counted upstream once, got %d", n)
	}
}

// A 5xx is not an endpoint refusal, even when it names another endpoint: the
// reply goes to the agent, and no other endpoint is probed.
func TestS2ServerErrorDoesNotSwitchEndpoint(t *testing.T) {
	u, _ := s2Serve(t, map[string]s2Reply{
		"/v1/chat/completions": {500, `{"error":"use v1/responses"}`},
		"/v1/responses":        {0, s2ResponsesOK},
	}, provider.Provider{Chat: "x", Responses: "x", Models: []string{"gpt-x"}})
	s3Sooner(t)
	code, _ := s2Call(New(), "/v1/chat/completions", `{"model":"s2/gpt-x","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 500 {
		t.Fatalf("status %d, want 500", code)
	}
	// the only provider's 500 may clear by itself, so it is tried three times
	// in all (see retryDelays); what must not happen is a probe of responses
	if h := u.s2Hits(); len(h) != 1 || h["/v1/chat/completions"] != 3 {
		t.Fatalf("hits %v, want only chat, three times", h)
	}
}

// The refusal matcher: 4xx replies that say the model is on another endpoint.
func TestS2NotServedMatcher(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `model gpt-x is not accessible via the /chat/completions endpoint`, true},
		{400, `{"error":{"code":"unsupported_api_for_model"}}`, true},
		{400, `not a chat model`, true},
		{404, `not supported in the v1/responses`, true},
		{400, `use /v1/chat/completions`, true},
		{500, `use v1/responses`, false},
		{503, `not accessible via the /chat/completions endpoint`, false},
		{200, `use v1/responses`, false},
		{400, `messages: field required`, false},
		{400, `context length exceeded`, false},
	} {
		if got := notChatModel(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("notChatModel(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

// The order requests are tried in, without any upstream.
func TestS2EndpointOrder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const u = "https://example.test/v1"
	oai := provider.Provider{ID: "s2oai", Key: "k", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1", Models: []string{"gpt-6-sol"}}
	cp := provider.Provider{ID: "s2cp", Key: "k", Chat: "https://api.githubcopilot.com", Responses: "https://api.githubcopilot.com", Anthropic: "https://api.githubcopilot.com",
		Account: &provider.Account{Agent: "copilot", User: "u"}, Models: []string{"gpt-6-sol", "o4-mini", "claude-x", "openai/gpt-oss-120b"}}
	other := provider.Provider{ID: "s2other", Key: "k", Chat: u, Responses: u, Models: []string{"openai/gpt-oss-120b"}}
	s := New()
	for _, tc := range []struct {
		name  string
		p     provider.Provider
		model string
		from  provider.Protocol
		relay bool
		want  []provider.Protocol
	}{
		{"openai translates to responses first", oai, "gpt-6-sol", provider.Anthropic, false, []provider.Protocol{provider.Responses, provider.Chat}},
		{"copilot openai translates to responses first", cp, "gpt-6-sol", provider.Anthropic, false, []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}},
		{"copilot o-series translates to responses first", cp, "o4-mini", provider.Anthropic, false, []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}},
		{"copilot claude translates to chat first", cp, "claude-x", provider.Anthropic, false, []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}},
		{"gpt-oss is another vendor's", cp, "openai/gpt-oss-120b", provider.Anthropic, false, []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}},
		{"other vendor translates to chat first", other, "openai/gpt-oss-120b", provider.Anthropic, false, []provider.Protocol{provider.Chat, provider.Responses}},
		{"agent's own api is relayed first", cp, "gpt-6-sol", provider.Chat, true, []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}},
	} {
		if got := s.endpointOrder(tc.p, tc.model, tc.from, tc.relay); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A refusal is remembered per provider, model and endpoint, and the refused
// endpoint is left out until nothing else is left.
func TestS2RefusalIsScopedAndLeavesOthers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cp := provider.Provider{ID: "s2cp", Key: "k", Chat: "https://api.githubcopilot.com", Responses: "https://api.githubcopilot.com",
		Account: &provider.Account{Agent: "copilot", User: "u"}, Models: []string{"gpt-6-sol", "o4-mini"}}
	other := cp
	other.ID = "s2cp2"
	s := New()
	s.markNotServed(cp.ID, "gpt-6-sol", provider.Responses)
	if got := s.endpointOrder(cp, "gpt-6-sol", provider.Anthropic, false); !slices.Equal(got, []provider.Protocol{provider.Chat}) {
		t.Fatalf("refused endpoint still tried: %v", got)
	}
	if got := s.endpointOrder(cp, "o4-mini", provider.Anthropic, false); !slices.Equal(got, []provider.Protocol{provider.Responses, provider.Chat}) {
		t.Fatalf("other model affected: %v", got)
	}
	if got := s.endpointOrder(other, "gpt-6-sol", provider.Anthropic, false); !slices.Equal(got, []provider.Protocol{provider.Responses, provider.Chat}) {
		t.Fatalf("other provider affected: %v", got)
	}
	// once every endpoint has refused it, they are all tried again, in order,
	// rather than the request having nowhere to go
	s.markNotServed(cp.ID, "gpt-6-sol", provider.Chat)
	if got := s.endpointOrder(cp, "gpt-6-sol", provider.Anthropic, false); !slices.Equal(got, []provider.Protocol{provider.Responses, provider.Chat}) {
		t.Fatalf("every endpoint refused: %v", got)
	}
}
