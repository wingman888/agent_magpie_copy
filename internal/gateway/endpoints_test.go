package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// endpointFake is an upstream serving several endpoints, each with its own
// reply: a status (0 meaning 200) and a body, and it counts what hit it.
type endpointFake struct {
	code  map[string]int
	body  map[string]string
	calls map[string]int
}

func newEndpointFake() *endpointFake {
	return &endpointFake{code: map[string]int{}, body: map[string]string{}, calls: map[string]int{}}
}

func (f *endpointFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	path := r.URL.Path
	f.calls[path]++
	switch {
	case f.code[path] >= 400:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.code[path])
		io.WriteString(w, f.body[path])
	case strings.HasSuffix(path, "/chat/completions"):
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"chat ok"}}]}`,
			`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
			`data: [DONE]`))
	case strings.HasSuffix(path, "/responses"):
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1","usage":{"input_tokens":0,"output_tokens":0}}}`,
			`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"responses ok"}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`,
		))
	default:
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"anthropic ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}
}

// endpointSetup saves provider "fake" with the given bases pointing at the
// fake, serving the given models.
func endpointSetup(t *testing.T, f *endpointFake, chat, responses, anthropic bool, models ...string) *httptest.Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: models}
	if chat {
		p.Chat = up.URL + "/v1"
	}
	if responses {
		p.Responses = up.URL + "/v1"
	}
	if anthropic {
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return up
}

// call posts a body to a client path and returns the status.
func endpointCall(t *testing.T, h http.Handler, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const chatBody = `{"model":"m1","messages":[{"role":"user","content":"hello"}]}`
const responsesBody = `{"model":"m1","input":"hello"}`

// A Chat client whose model is refused on Chat Completions is answered by
// Responses, and the refusal is remembered.
func TestChatClientRefusedOnChatUsesResponses(t *testing.T) {
	f := newEndpointFake()
	f.code["/v1/chat/completions"] = 400
	f.body["/v1/chat/completions"] = `{"error":{"message":"model m1 not accessible via the /chat/completions endpoint"}}`
	endpointSetup(t, f, true, true, false, "m1")
	h := New().Handler()

	code, body := endpointCall(t, h, "/v1/chat/completions", chatBody)
	if code != 200 || !strings.Contains(body, "responses ok") {
		t.Fatalf("first: status %d: %s", code, body)
	}
	if f.calls["/v1/chat/completions"] != 1 || f.calls["/v1/responses"] != 1 {
		t.Fatalf("first calls: %v", f.calls)
	}
	code, body = endpointCall(t, h, "/v1/chat/completions", chatBody)
	if code != 200 || !strings.Contains(body, "responses ok") {
		t.Fatalf("second: status %d: %s", code, body)
	}
	if f.calls["/v1/chat/completions"] != 1 || f.calls["/v1/responses"] != 2 {
		t.Fatalf("second calls should not reach chat again: %v", f.calls)
	}
}

// A Responses client whose model is refused on Responses is answered by Chat
// Completions, and then only Chat is used.
func TestResponsesClientRefusedUsesChatAndRemembers(t *testing.T) {
	f := newEndpointFake()
	f.code["/v1/responses"] = 400
	f.body["/v1/responses"] = `{"error":{"code":"unsupported_api_for_model","message":"model m1 does not support this api"}}`
	endpointSetup(t, f, true, true, false, "m1")
	h := New().Handler()

	code, body := endpointCall(t, h, "/v1/responses", responsesBody)
	if code != 200 || !strings.Contains(body, "chat ok") {
		t.Fatalf("first: status %d: %s", code, body)
	}
	if f.calls["/v1/responses"] != 1 || f.calls["/v1/chat/completions"] != 1 {
		t.Fatalf("first calls: %v", f.calls)
	}
	code, body = endpointCall(t, h, "/v1/responses", responsesBody)
	if code != 200 || !strings.Contains(body, "chat ok") {
		t.Fatalf("second: status %d: %s", code, body)
	}
	if f.calls["/v1/responses"] != 1 || f.calls["/v1/chat/completions"] != 2 {
		t.Fatalf("second calls should not reach responses again: %v", f.calls)
	}
}

// A model's list naming its APIs sends it to those only, with no probes.
func TestDeclaredAPIsAreTheOnlyEndpoints(t *testing.T) {
	f := newEndpointFake()
	up := endpointSetup(t, f, true, true, true, "gpt-x", "claude-x")
	if err := catalog.SaveLive("fake", up.URL, []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat"}},
	}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	gptChat := `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`
	claudeResp := `{"model":"claude-x","input":"hi"}`
	claudeAnt := `{"model":"claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	gptAnt := `{"model":"gpt-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	for _, tc := range []struct {
		path, body string
		want       string
	}{
		{"/v1/messages", gptAnt, "/v1/responses"},
		{"/v1/chat/completions", gptChat, "/v1/responses"},
		{"/v1/responses", claudeResp, "/v1/chat/completions"},
		{"/v1/messages", claudeAnt, "/v1/chat/completions"},
	} {
		before := map[string]int{}
		for k, v := range f.calls {
			before[k] = v
		}
		if code, body := endpointCall(t, h, tc.path, tc.body); code != 200 {
			t.Fatalf("%s: status %d: %s", tc.path, code, body)
		}
		for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
			got := f.calls[path] - before[path]
			want := 0
			if path == tc.want {
				want = 1
			}
			if got != want {
				t.Errorf("%s %s: %s hit %d times, want %d", tc.path, tc.body, path, got, want)
			}
		}
	}
}

// A 500 that happens to name another endpoint is an upstream failure, not a
// refusal: nothing is switched.
func TestServerErrorDoesNotSwitchEndpoint(t *testing.T) {
	f := newEndpointFake()
	f.code["/v1/chat/completions"] = 500
	f.body["/v1/chat/completions"] = `{"error":{"message":"internal error; use v1/responses"}}`
	endpointSetup(t, f, true, true, false, "m1")
	code, _ := endpointCall(t, New().Handler(), "/v1/chat/completions", chatBody)
	if code != 500 {
		t.Fatalf("status %d, want 500", code)
	}
	if f.calls["/v1/chat/completions"] != 1 || f.calls["/v1/responses"] != 0 {
		t.Fatalf("calls: %v", f.calls)
	}
}

// With no other endpoint to try, the last refusal goes back as it was.
func TestLastEndpointRefusalIsReturned(t *testing.T) {
	f := newEndpointFake()
	f.code["/v1/chat/completions"] = 400
	f.body["/v1/chat/completions"] = `{"error":{"message":"model m1 not accessible via the /chat/completions endpoint"}}`
	endpointSetup(t, f, true, false, false, "m1")
	code, body := endpointCall(t, New().Handler(), "/v1/chat/completions", chatBody)
	if code != 400 || !strings.Contains(body, "not accessible via the /chat/completions endpoint") {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.calls["/v1/chat/completions"] != 1 {
		t.Fatalf("calls: %v", f.calls)
	}
}

func TestNotChatModelPhrases(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `model m1 not accessible via the /chat/completions endpoint`, true},
		{400, `{"code":"unsupported_api_for_model"}`, true},
		{400, `not supported in the v1/responses`, true},
		{400, `use /v1/chat/completions`, true},
		{400, `messages: field required`, false},
		{400, `context length exceeded`, false},
		{500, `use v1/responses`, false},
		{503, `not accessible via the /chat/completions endpoint`, false},
		{200, `use v1/responses`, false},
	} {
		if got := notChatModel(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("notChatModel(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

func TestTranslationPreference(t *testing.T) {
	openai := provider.Provider{ID: "oai", Name: "OpenAI", Key: "k",
		Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}
	copilot := provider.Provider{ID: "gh", Name: "Copilot", Key: "k",
		Chat: "https://api.githubcopilot.com", Responses: "https://api.githubcopilot.com",
		Anthropic: "https://api.githubcopilot.com", Account: &provider.Account{Agent: "copilot"}}
	other := provider.Provider{ID: "or", Name: "Other", Key: "k",
		Chat: "https://openrouter.example/v1", Responses: "https://openrouter.example/v1"}
	for _, tc := range []struct {
		p     provider.Provider
		model string
		want  []provider.Protocol
	}{
		{openai, "gpt-6-sol", []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}},
		{openai, "o4-mini", []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}},
		{copilot, "gpt-6-sol", []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}},
		{copilot, "codex-mini", []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}},
		{copilot, "openai/gpt-6-sol", []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}},
		{copilot, "claude-sonnet-5", []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}},
		{other, "openai/gpt-oss-120b", []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}},
		{other, "gpt-6-sol", []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}},
	} {
		if got := preferred(tc.p, tc.model); !slices.Equal(got, tc.want) {
			t.Errorf("preferred(%s, %s) = %v, want %v", tc.p.ID, tc.model, got, tc.want)
		}
	}
}

// Once an endpoint is remembered as not serving a model, only that
// (provider, model, endpoint) is skipped.
func TestUnavailableMemoryIsPerModelAndEndpoint(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	s := New()
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Chat: "http://x/v1", Responses: "http://x/v1"}
	s.markUnavailable("fake", "m1", provider.Responses)
	if got := s.endpoints(p, "m1", provider.Chat, true); !slices.Equal(got, []provider.Protocol{provider.Chat}) {
		t.Errorf("m1 endpoints = %v, want chat only", got)
	}
	if got := s.endpoints(p, "m2", provider.Chat, true); !slices.Equal(got, []provider.Protocol{provider.Chat, provider.Responses}) {
		t.Errorf("m2 endpoints = %v, want chat then responses", got)
	}
	if got := s.endpoints(p, "m1", provider.Responses, true); !slices.Equal(got, []provider.Protocol{provider.Chat}) {
		t.Errorf("m1 from responses = %v, want chat only", got)
	}
}
