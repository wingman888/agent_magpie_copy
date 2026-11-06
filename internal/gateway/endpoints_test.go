package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// apiFake is an upstream with one reply per path, and a record of the paths it
// was asked for, in order.
type apiFake struct {
	mu    sync.Mutex
	hits  []string
	reply map[string]func(http.ResponseWriter)
}

func (f *apiFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	f.mu.Lock()
	f.hits = append(f.hits, r.URL.Path)
	f.mu.Unlock()
	if h, ok := f.reply[r.URL.Path]; ok {
		h(w)
		return
	}
	http.NotFound(w, r)
}

// seen lists the paths asked for, in order.
func (f *apiFake) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hits...)
}

func (f *apiFake) count(path string) int {
	n := 0
	for _, h := range f.seen() {
		if h == path {
			n++
		}
	}
	return n
}

func refuseWith(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

func chatOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"chat ok"}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`data: [DONE]`,
	))
}

func responsesOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"responses ok"}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`,
	))
}

func anthropicOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x","content":[{"type":"text","text":"anthropic ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
}

// setupFake starts the upstream with a temp config and cache, and saves a
// provider "fake" speaking Chat and Responses at it, and Anthropic too when
// anthropic is set. It returns the upstream's base URL.
func setupFake(t *testing.T, f *apiFake, anthropic bool) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1", Responses: up.URL + "/v1"}
	if anthropic {
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return up.URL
}

// send posts a body to a client API of s and returns the status and reply.
func send(s *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func TestChatClientReachesResponsesOnlyModel(t *testing.T) {
	f := &apiFake{reply: map[string]func(http.ResponseWriter){
		"/v1/chat/completions": refuseWith(400, `{"error":{"message":"The model m1 is not accessible via the /chat/completions endpoint"}}`),
		"/v1/responses":        responsesOK,
	}}
	setupFake(t, f, false)
	s := New()
	body := `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`

	code, reply := send(s, "/v1/chat/completions", body)
	if code != 200 || !strings.Contains(reply, "responses ok") {
		t.Fatalf("first call: status %d: %s", code, reply)
	}
	if strings.Contains(reply, "not accessible") {
		t.Errorf("the refused reply leaked to the agent: %s", reply)
	}
	if f.count("/v1/chat/completions") != 1 || f.count("/v1/responses") != 1 {
		t.Fatalf("first call upstream: %v", f.seen())
	}

	code, reply = send(s, "/v1/chat/completions", body)
	if code != 200 || !strings.Contains(reply, "responses ok") {
		t.Fatalf("second call: status %d: %s", code, reply)
	}
	if f.count("/v1/chat/completions") != 1 || f.count("/v1/responses") != 2 {
		t.Fatalf("refusal not remembered: %v", f.seen())
	}
}

func TestResponsesClientReachesChatOnlyModel(t *testing.T) {
	f := &apiFake{reply: map[string]func(http.ResponseWriter){
		"/v1/responses":        refuseWith(400, `{"error":{"code":"unsupported_api_for_model","message":"Model m1 is not usable here"}}`),
		"/v1/chat/completions": chatOK,
	}}
	setupFake(t, f, false)
	s := New()
	body := `{"model":"fake/m1","input":"hi"}`

	code, reply := send(s, "/v1/responses", body)
	if code != 200 || !strings.Contains(reply, "chat ok") {
		t.Fatalf("first call: status %d: %s", code, reply)
	}
	if f.count("/v1/responses") != 1 || f.count("/v1/chat/completions") != 1 {
		t.Fatalf("first call upstream: %v", f.seen())
	}

	code, reply = send(s, "/v1/responses", body)
	if code != 200 || !strings.Contains(reply, "chat ok") {
		t.Fatalf("second call: status %d: %s", code, reply)
	}
	if f.count("/v1/responses") != 1 || f.count("/v1/chat/completions") != 2 {
		t.Fatalf("refusal not remembered: %v", f.seen())
	}
}

func TestDeclaredAPIsAreTheOnlyOnesProbed(t *testing.T) {
	cases := []struct {
		name, path, body, want string // want is the one path expected; "" for none
	}{
		{"anthropic client, responses-only model", "/v1/messages",
			`{"model":"fake/gpt-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "/v1/responses"},
		{"chat client, responses-only model", "/v1/chat/completions",
			`{"model":"fake/gpt-x","messages":[{"role":"user","content":"hi"}]}`, "/v1/responses"},
		{"responses client, chat-only model", "/v1/responses",
			`{"model":"fake/claude-x","input":"hi"}`, "/v1/chat/completions"},
		{"anthropic client, chat-only model, anthropic endpoint too", "/v1/messages",
			`{"model":"fake/claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions"},
		{"count_tokens, model not on anthropic", "/v1/messages/count_tokens",
			`{"model":"fake/claude-x","messages":[{"role":"user","content":"hi"}]}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &apiFake{reply: map[string]func(http.ResponseWriter){
				"/v1/chat/completions": chatOK,
				"/v1/responses":        responsesOK,
				"/v1/messages":         anthropicOK,
			}}
			base := setupFake(t, f, true)
			if err := catalog.SaveLive("fake", base, []catalog.Model{
				{ID: "gpt-x", APIs: []string{"responses"}},
				{ID: "claude-x", APIs: []string{"chat"}},
			}); err != nil {
				t.Fatal(err)
			}
			code, reply := send(New(), tc.path, tc.body)
			if code != 200 {
				t.Fatalf("status %d: %s", code, reply)
			}
			got := f.seen()
			if tc.want == "" && len(got) != 0 {
				t.Fatalf("upstream asked %v, want none", got)
			}
			if tc.want != "" && (len(got) != 1 || got[0] != tc.want) {
				t.Fatalf("upstream asked %v, want only %s", got, tc.want)
			}
		})
	}
}

func TestServerErrorDoesNotChangeAPI(t *testing.T) {
	f := &apiFake{reply: map[string]func(http.ResponseWriter){
		"/v1/chat/completions": refuseWith(500, `{"error":{"message":"use v1/responses"}}`),
		"/v1/responses":        responsesOK,
	}}
	setupFake(t, f, false)
	s := New()
	body := `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`

	code, reply := send(s, "/v1/chat/completions", body)
	if code != 500 {
		t.Fatalf("status %d, want 500: %s", code, reply)
	}
	if f.count("/v1/responses") != 0 {
		t.Fatalf("a 500 switched API: %v", f.seen())
	}
	send(s, "/v1/chat/completions", body)
	if f.count("/v1/chat/completions") != 2 || f.count("/v1/responses") != 0 {
		t.Fatalf("a 500 was remembered: %v", f.seen())
	}
}

func TestSuccessMentioningAPIDoesNotChangeAPI(t *testing.T) {
	f := &apiFake{reply: map[string]func(http.ResponseWriter){
		"/v1/chat/completions": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"use v1/responses"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		},
		"/v1/responses": responsesOK,
	}}
	setupFake(t, f, false)
	code, reply := send(New(), "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(reply, "use v1/responses") {
		t.Fatalf("status %d: %s", code, reply)
	}
	if f.count("/v1/responses") != 0 {
		t.Fatalf("a 200 switched API: %v", f.seen())
	}
}

type orderCase struct {
	name        string
	p           provider.Provider
	model       string
	from        provider.Protocol
	refuseModel string // the model refusals are recorded on; the model when empty
	refuse      []provider.Protocol
	want        string
}

func TestEndpointOrder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	openai := provider.Provider{ID: "openai", Name: "OpenAI", Key: "k",
		Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}
	copilot := provider.Provider{ID: "copilot", Name: "Copilot", Key: "k",
		Chat: "https://api.githubcopilot.com", Responses: "https://api.githubcopilot.com"}
	copilotAll := copilot
	copilotAll.Anthropic = "https://api.githubcopilot.com"
	relay := provider.Provider{ID: "relay", Name: "Relay", Key: "k",
		Chat: "https://relay.example/v1", Responses: "https://relay.example/v1"}

	cases := []orderCase{
		{name: "openai gpt-6-sol translated: responses first", p: openai, model: "gpt-6-sol", from: provider.Anthropic, want: "[responses chat]"},
		{name: "openai o4-mini translated: responses first", p: openai, model: "o4-mini", from: provider.Anthropic, want: "[responses chat]"},
		{name: "openai gpt-oss under a prefix is another vendor's", p: openai, model: "openai/gpt-oss-120b", from: provider.Anthropic, want: "[chat responses]"},
		{name: "copilot gpt-6-sol translated: responses first", p: copilot, model: "gpt-6-sol", from: provider.Anthropic, want: "[responses chat]"},
		{name: "copilot o4-mini translated: responses first", p: copilot, model: "o4-mini", from: provider.Anthropic, want: "[responses chat]"},
		{name: "copilot claude translated: chat first", p: copilot, model: "claude-sonnet-4.5", from: provider.Anthropic, want: "[chat responses]"},
		{name: "copilot gpt-oss under a prefix: chat first", p: copilot, model: "openai/gpt-oss-120b", from: provider.Anthropic, want: "[chat responses]"},
		{name: "other vendor gpt-6-sol: chat first", p: relay, model: "gpt-6-sol", from: provider.Anthropic, want: "[chat responses]"},
		{name: "copilot claude from anthropic: as is, then chat", p: copilotAll, model: "claude-sonnet-4.5", from: provider.Anthropic, want: "[anthropic chat responses]"},
		{name: "copilot claude from chat: as is, then the rest", p: copilotAll, model: "claude-sonnet-4.5", from: provider.Chat, want: "[chat responses anthropic]"},
		{name: "copilot gpt-6-sol from responses: as is first", p: copilotAll, model: "gpt-6-sol", from: provider.Responses, want: "[responses chat anthropic]"},
		{name: "refused chat leaves responses", p: copilot, model: "claude-sonnet-4.5", from: provider.Anthropic,
			refuse: []provider.Protocol{provider.Chat}, want: "[responses]"},
		{name: "refused on the model only", p: copilot, model: "gpt-6-sol", from: provider.Anthropic,
			refuseModel: "claude-sonnet-4.5", refuse: []provider.Protocol{provider.Chat}, want: "[responses chat]"},
		{name: "every API refused: all tried", p: copilot, model: "claude-sonnet-4.5", from: provider.Anthropic,
			refuse: []provider.Protocol{provider.Chat, provider.Responses}, want: "[chat responses]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			on := tc.refuseModel
			if on == "" {
				on = tc.model
			}
			for _, proto := range tc.refuse {
				s.refuse(tc.p.ID, on, proto)
			}
			if got := fmt.Sprint(s.endpoints(tc.p, tc.model, tc.from)); got != tc.want {
				t.Errorf("endpoints = %s, want %s", got, tc.want)
			}
		})
	}
}
