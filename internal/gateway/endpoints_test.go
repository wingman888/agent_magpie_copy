package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// endpointFake answers each upstream path with its scripted status and body,
// and counts the requests each path gets. A path without a reply is a 404.
type endpointFake struct {
	status map[string]int
	reply  map[string]string
	calls  map[string]int
}

func (f *endpointFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	f.calls[r.URL.Path]++
	reply, ok := f.reply[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	code := f.status[r.URL.Path]
	if code == 0 {
		code = 200
	}
	if code >= 400 {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	w.WriteHeader(code)
	io.WriteString(w, reply)
}

const endpointChatReply = `data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"chat ok"}}]}

data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}

data: [DONE]

`

const endpointResponsesReply = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"r1","model":"m1","usage":{"input_tokens":0,"output_tokens":0}}}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","delta":"fallback ok"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}` + "\n\n"

// endpointUpstream starts the fake and points magpie's config at a temp dir.
func endpointUpstream(t *testing.T, f *endpointFake) *httptest.Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	return up
}

func serveAt(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func TestChatClientMovesToResponsesAndRemembers(t *testing.T) {
	f := &endpointFake{
		status: map[string]int{"/v1/chat/completions": 400},
		reply: map[string]string{
			"/v1/chat/completions": `{"error":{"message":"model m1 is not accessible via the /chat/completions endpoint"}}`,
			"/v1/responses":        endpointResponsesReply,
		},
		calls: map[string]int{},
	}
	up := endpointUpstream(t, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	body := `{"model":"m1","messages":[{"role":"user","content":"hello"}]}`

	code, out := serveAt(h, "/v1/chat/completions", body)
	if code != 200 || !strings.Contains(out, "fallback ok") {
		t.Fatalf("first: %d %s", code, out)
	}
	if strings.Contains(out, "not accessible") {
		t.Errorf("the refusal reached the agent: %s", out)
	}
	if f.calls["/v1/chat/completions"] != 1 || f.calls["/v1/responses"] != 1 {
		t.Fatalf("first calls: %v", f.calls)
	}

	code, out = serveAt(h, "/v1/chat/completions", body)
	if code != 200 || !strings.Contains(out, "fallback ok") {
		t.Fatalf("second: %d %s", code, out)
	}
	if f.calls["/v1/chat/completions"] != 1 || f.calls["/v1/responses"] != 2 {
		t.Fatalf("second calls, chat must not be asked again: %v", f.calls)
	}
}

func TestResponsesClientMovesToChatAndRemembers(t *testing.T) {
	f := &endpointFake{
		status: map[string]int{"/v1/responses": 400},
		reply: map[string]string{
			"/v1/responses":        `{"error":{"code":"unsupported_api_for_model","message":"model m1 is not supported"}}`,
			"/v1/chat/completions": endpointChatReply,
		},
		calls: map[string]int{},
	}
	up := endpointUpstream(t, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	body := `{"model":"m1","input":"hello"}`

	code, out := serveAt(h, "/v1/responses", body)
	if code != 200 || !strings.Contains(out, "chat ok") {
		t.Fatalf("first: %d %s", code, out)
	}
	if f.calls["/v1/responses"] != 1 || f.calls["/v1/chat/completions"] != 1 {
		t.Fatalf("first calls: %v", f.calls)
	}

	code, out = serveAt(h, "/v1/responses", body)
	if code != 200 || !strings.Contains(out, "chat ok") {
		t.Fatalf("second: %d %s", code, out)
	}
	if f.calls["/v1/responses"] != 1 || f.calls["/v1/chat/completions"] != 2 {
		t.Fatalf("second calls, responses must not be asked again: %v", f.calls)
	}
}

func TestDeclaredAPIsDecideEndpoints(t *testing.T) {
	f := &endpointFake{
		reply: map[string]string{
			"/v1/chat/completions": endpointChatReply,
			"/v1/responses":        endpointResponsesReply,
		},
		calls: map[string]int{},
	}
	up := endpointUpstream(t, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"gpt-x", "claude-x"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("fake", up.URL, []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat"}},
	}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	chatBody := `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`
	claudeOpenAI := `{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`
	claudeAnthropic := `{"model":"claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	gptAnthropic := `{"model":"gpt-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`

	steps := []struct {
		name, path, body, want string
	}{
		{"chat client, responses-only model", "/v1/chat/completions", chatBody, "/v1/responses"},
		{"responses client, chat-only model", "/v1/responses", `{"model":"claude-x","input":"hi"}`, "/v1/chat/completions"},
		{"anthropic client, chat-only model", "/v1/messages", claudeAnthropic, "/v1/chat/completions"},
		{"anthropic client, responses-only model", "/v1/messages", gptAnthropic, "/v1/responses"},
		{"chat client, chat-only model", "/v1/chat/completions", claudeOpenAI, "/v1/chat/completions"},
	}
	for _, st := range steps {
		f.calls = map[string]int{}
		code, out := serveAt(h, st.path, st.body)
		if code != 200 {
			t.Fatalf("%s: %d %s", st.name, code, out)
		}
		if len(f.calls) != 1 || f.calls[st.want] != 1 {
			t.Fatalf("%s: upstream calls %v, want one to %s", st.name, f.calls, st.want)
		}
	}

	// count_tokens is not probed at Anthropic for a model that is not
	// declared for it
	f.calls = map[string]int{}
	code, out := serveAt(h, "/v1/messages/count_tokens", claudeAnthropic)
	if code != 200 || !strings.Contains(out, "input_tokens") {
		t.Fatalf("count_tokens: %d %s", code, out)
	}
	if f.calls["/v1/messages/count_tokens"] != 0 {
		t.Fatalf("count_tokens probed the provider: %v", f.calls)
	}
}

func TestServerErrorsDoNotMoveEndpoint(t *testing.T) {
	f := &endpointFake{
		status: map[string]int{"/v1/chat/completions": 500},
		reply: map[string]string{
			"/v1/chat/completions": `{"error":{"message":"internal: use v1/responses"}}`,
			"/v1/responses":        endpointResponsesReply,
		},
		calls: map[string]int{},
	}
	up := endpointUpstream(t, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	quickRetries(t)
	h := New().Handler()
	body := `{"model":"m1","messages":[{"role":"user","content":"hello"}]}`
	for i := 1; i <= 2; i++ {
		code, out := serveAt(h, "/v1/chat/completions", body)
		if code != 500 {
			t.Fatalf("request %d: status %d, want the 500 passed on: %s", i, code, out)
		}
	}
	// the only candidate is tried again twice on a 500 before it is passed on
	if f.calls["/v1/chat/completions"] != 6 || f.calls["/v1/responses"] != 0 {
		t.Fatalf("calls: %v", f.calls)
	}
}

func TestRoutesOrderByVendor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	s := New()
	openai := provider.Provider{ID: "oa", Name: "OpenAI", Key: "k",
		Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}
	other := provider.Provider{ID: "other", Name: "Other", Key: "k",
		Chat: "https://relay.example/v1", Responses: "https://relay.example/v1"}
	copilot := provider.Provider{ID: "copilot", Name: "Copilot",
		Chat: "https://api.githubcopilot.com", Responses: "https://api.githubcopilot.com",
		Anthropic: "https://api.githubcopilot.com", Account: &provider.Account{Agent: "copilot"}}
	// Gemini is an API no provider speaks, so every route is a translation
	cases := []struct {
		name  string
		p     provider.Provider
		model string
		want  []provider.Protocol
	}{
		{"openai gpt", openai, "gpt-6-sol", []provider.Protocol{provider.Responses, provider.Chat}},
		{"openai o-series", openai, "o4-mini", []provider.Protocol{provider.Responses, provider.Chat}},
		{"openai with vendor prefix", openai, "openai/gpt-6-sol", []provider.Protocol{provider.Responses, provider.Chat}},
		{"other vendor's claude", other, "claude-x", []provider.Protocol{provider.Chat, provider.Responses}},
		{"other vendor's gpt-oss", other, "openai/gpt-oss-120b", []provider.Protocol{provider.Chat, provider.Responses}},
		{"copilot gpt", copilot, "gpt-5.5", []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}},
		{"copilot claude", copilot, "claude-opus-5.5", []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}},
	}
	for _, tc := range cases {
		var got []provider.Protocol
		for _, rt := range s.routes(tc.p, tc.model, provider.Gemini, []byte(`{}`)) {
			got = append(got, rt.to)
		}
		if strings.Join(protoNames(got), ",") != strings.Join(protoNames(tc.want), ",") {
			t.Errorf("%s: routes %v, want %v", tc.name, got, tc.want)
		}
	}

	// a refusal leaves only the other endpoint, for that model alone
	s.markRefused("oa", "gpt-6-sol", provider.Responses)
	if got := s.routes(openai, "gpt-6-sol", provider.Gemini, []byte(`{}`)); len(got) != 1 || got[0].to != provider.Chat {
		t.Errorf("after refusal: %v", got)
	}
	if got := s.routes(openai, "o4-mini", provider.Gemini, []byte(`{}`)); len(got) != 2 || got[0].to != provider.Responses {
		t.Errorf("refusal leaked to another model: %v", got)
	}
}

func protoNames(ps []provider.Protocol) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, string(p))
	}
	return out
}
