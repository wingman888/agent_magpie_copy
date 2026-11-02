package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// endpointFake serves all three APIs; reject maps a path to the error it
// answers there, everything else answers with a short streamed reply.
type endpointFake struct {
	mu     sync.Mutex
	calls  map[string]int
	models map[string][]string // path → models asked for there
	reject map[string]func(model string) (int, string)
}

func newEndpointFake() *endpointFake {
	return &endpointFake{calls: map[string]int{}, models: map[string][]string{}, reject: map[string]func(string) (int, string){}}
}

func (f *endpointFake) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func (f *endpointFake) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

func (f *endpointFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	json.Unmarshal(body, &req)
	f.mu.Lock()
	f.calls[r.URL.Path]++
	f.models[r.URL.Path] = append(f.models[r.URL.Path], req.Model)
	rej := f.reject[r.URL.Path]
	f.mu.Unlock()
	if rej != nil {
		if code, msg := rej(req.Model); code != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			io.WriteString(w, msg)
			return
		}
	}
	switch r.URL.Path {
	case "/v1/chat/completions":
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"`+req.Model+`","choices":[{"index":0,"message":{"role":"assistant","content":"chat ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","model":"`+req.Model+`","choices":[{"delta":{"role":"assistant","content":"chat ok"}}]}`,
			`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
			`data: [DONE]`))
	case "/v1/responses":
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"`+req.Model+`","usage":{"input_tokens":0,"output_tokens":0}}}`,
			`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"responses ok"}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"`+req.Model+`","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`,
		))
	case "/v1/messages":
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"`+req.Model+`","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"anthropic ok"}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
		))
	default:
		http.NotFound(w, r)
	}
}

// setupEndpoints saves one provider, "fake", serving the given APIs at the
// fake, and returns the fake's URL.
func setupEndpoints(t *testing.T, f *endpointFake, models []string, protos ...provider.Protocol) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: models}
	for _, pr := range protos {
		switch pr {
		case provider.Chat:
			p.Chat = up.URL + "/v1"
		case provider.Responses:
			p.Responses = up.URL + "/v1"
		case provider.Anthropic:
			p.Anthropic = up.URL
		}
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return up.URL
}

func postTo(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// Acceptance 1: a Chat client's model turned away by /chat/completions is
// answered through /responses, and the next request goes there directly.
func TestChatClientMovesToResponsesAndRemembers(t *testing.T) {
	f := newEndpointFake()
	f.reject["/v1/chat/completions"] = func(string) (int, string) {
		return 400, `{"error":{"message":"model \"m1\" is not accessible via the /chat/completions endpoint","code":"unsupported_api_for_model"}}`
	}
	setupEndpoints(t, f, []string{"m1"}, provider.Chat, provider.Responses)
	h := New().Handler()
	code, body := postTo(h, "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "responses ok") {
		t.Fatalf("status %d: %s", code, body)
	}
	if strings.Contains(body, "not accessible") {
		t.Errorf("the refusal leaked: %s", body)
	}
	if c, r := f.count("/v1/chat/completions"), f.count("/v1/responses"); c != 1 || r != 1 {
		t.Fatalf("calls: chat=%d responses=%d", c, r)
	}
	code, body = postTo(h, "/v1/chat/completions", `{"model":"m1","stream":true,"messages":[{"role":"user","content":"again"}]}`)
	if code != 200 || !strings.Contains(body, "responses ok") {
		t.Fatalf("second status %d: %s", code, body)
	}
	if c, r := f.count("/v1/chat/completions"), f.count("/v1/responses"); c != 1 || r != 2 {
		t.Fatalf("remembered calls: chat=%d responses=%d", c, r)
	}
}

// Acceptance 2: the reverse, a Responses client's model only on Chat.
func TestResponsesClientMovesToChatAndRemembers(t *testing.T) {
	f := newEndpointFake()
	f.reject["/v1/responses"] = func(string) (int, string) {
		return 400, `{"error":{"message":"The requested model is not supported.","code":"unsupported_api_for_model","type":"invalid_request_error"}}`
	}
	setupEndpoints(t, f, []string{"m1"}, provider.Chat, provider.Responses)
	h := New().Handler()
	for i, stream := range []string{"false", "true"} {
		code, body := postTo(h, "/v1/responses", `{"model":"m1","stream":`+stream+`,"input":"hi"}`)
		if code != 200 || !strings.Contains(body, "chat ok") {
			t.Fatalf("request %d: status %d: %s", i, code, body)
		}
	}
	if c, r := f.count("/v1/chat/completions"), f.count("/v1/responses"); c != 2 || r != 1 {
		t.Fatalf("calls: chat=%d responses=%d", c, r)
	}
}

// Without a model list, an Anthropic client's model refused at /v1/messages
// moves to the next API, and an endpoint refused for one model stays open
// to the provider's others.
func TestRefusalIsPerModel(t *testing.T) {
	f := newEndpointFake()
	f.reject["/v1/messages"] = func(model string) (int, string) {
		if model == "gpt-x" {
			return 404, `{"type":"error","error":{"type":"not_found_error","message":"model gpt-x is not accessible via the /v1/messages endpoint"}}`
		}
		return 0, ""
	}
	setupEndpoints(t, f, []string{"gpt-x", "claude-x"}, provider.Chat, provider.Anthropic)
	h := New().Handler()
	code, body := postTo(h, "/v1/messages", `{"model":"gpt-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "chat ok") {
		t.Fatalf("gpt-x: status %d: %s", code, body)
	}
	code, body = postTo(h, "/v1/messages", `{"model":"claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "anthropic ok") {
		t.Fatalf("claude-x: status %d: %s", code, body)
	}
	if got := f.models["/v1/messages"]; !slices.Equal(got, []string{"gpt-x", "claude-x"}) {
		t.Errorf("messages endpoint saw %v", got)
	}
}

// Acceptance 3: the model list's word on where a model is served is final;
// nothing else is probed.
func TestDeclaredAPIsAreTheOnlyOnesTried(t *testing.T) {
	f := newEndpointFake()
	wrong := func(string) (int, string) { return 500, `{"error":{"message":"should not be asked here"}}` }
	setupEndpoints(t, f, []string{"gpt-x", "claude-x"}, provider.Chat, provider.Responses, provider.Anthropic)
	if err := catalog.SaveLive("fake", "", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat"}},
	}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	for _, tc := range []struct {
		path, body, want string
	}{
		{"/v1/messages", `{"model":"gpt-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "/v1/responses"},
		{"/v1/chat/completions", `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`, "/v1/responses"},
		{"/v1/responses", `{"model":"claude-x","input":"hi"}`, "/v1/chat/completions"},
		{"/v1/messages", `{"model":"claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions"},
	} {
		f.mu.Lock()
		f.calls = map[string]int{}
		f.reject = map[string]func(string) (int, string){}
		for _, p := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
			if p != tc.want {
				f.reject[p] = wrong
			}
		}
		f.mu.Unlock()
		code, body := postTo(h, tc.path, tc.body)
		if code != 200 {
			t.Errorf("%s %s: status %d: %s", tc.path, tc.body, code, body)
		}
		if f.total() != 1 || f.count(tc.want) != 1 {
			t.Errorf("%s %s: upstream calls %v, want one to %s", tc.path, tc.body, f.calls, tc.want)
		}
	}
}

// count_tokens goes upstream only where the model is on the Anthropic API.
func TestCountTokensOnlyWhereServed(t *testing.T) {
	f := newEndpointFake()
	setupEndpoints(t, f, []string{"gpt-x"}, provider.Chat, provider.Anthropic)
	if err := catalog.SaveLive("fake", "", []catalog.Model{{ID: "gpt-x", APIs: []string{"chat"}}}); err != nil {
		t.Fatal(err)
	}
	code, body := postTo(New().Handler(), "/v1/messages/count_tokens", `{"model":"gpt-x","messages":[{"role":"user","content":"hello there"}]}`)
	if code != 200 || !strings.Contains(body, "input_tokens") {
		t.Fatalf("status %d: %s", code, body)
	}
	if n := f.total(); n != 0 {
		t.Errorf("count_tokens went upstream %d times: %v", n, f.calls)
	}
}

// Acceptance 4: a server error, however worded, doesn't move the request.
func TestServerErrorDoesNotMoveEndpoint(t *testing.T) {
	f := newEndpointFake()
	f.reject["/v1/chat/completions"] = func(string) (int, string) {
		return 500, `{"error":{"message":"not a chat model, use v1/responses"}}`
	}
	setupEndpoints(t, f, []string{"m1"}, provider.Chat, provider.Responses)
	old := retrySleep
	retrySleep = func(ctx context.Context, d time.Duration) error { return nil }
	defer func() { retrySleep = old }()
	code, _ := postTo(New().Handler(), "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 500 {
		t.Errorf("status %d, want 500", code)
	}
	// the only provider is tried again on a server error (see
	// TestLastCandidateRetries), but always on the same endpoint
	if c, r := f.count("/v1/chat/completions"), f.count("/v1/responses"); c != 1+maxRetries || r != 0 {
		t.Errorf("calls: chat=%d responses=%d", c, r)
	}
}

// With nowhere left to move, the last refusal reaches the agent as it was.
func TestLastRefusalKeepsItsStatus(t *testing.T) {
	f := newEndpointFake()
	refuse := func(string) (int, string) {
		return 400, `{"error":{"message":"model not accessible via the requested endpoint"}}`
	}
	f.reject["/v1/chat/completions"] = refuse
	f.reject["/v1/responses"] = refuse
	setupEndpoints(t, f, []string{"m1"}, provider.Chat, provider.Responses)
	code, body := postTo(New().Handler(), "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || !strings.Contains(body, "not accessible") {
		t.Errorf("status %d: %s", code, body)
	}
	if c, r := f.count("/v1/chat/completions"), f.count("/v1/responses"); c != 1 || r != 1 {
		t.Errorf("calls: chat=%d responses=%d", c, r)
	}
}

func TestWrongEndpoint(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `model "gpt-6" is not accessible via the /chat/completions endpoint`, true},
		{400, `{"error":{"code":"unsupported_api_for_model"}}`, true},
		{400, `This model is only supported in v1/responses and not in v1/chat/completions. Use v1/responses.`, true},
		{400, `please use /v1/chat/completions instead`, true},
		{400, `This model is not supported in the v1/responses endpoint.`, true},
		{404, `This is not a chat model and thus not supported in the v1/chat/completions endpoint.`, true},
		{500, `use v1/responses`, false},
		{503, `not accessible via the /chat/completions endpoint`, false},
		{200, `use v1/responses`, false},
		{400, `messages: field required`, false},
		{429, `rate limit`, false},
	} {
		if got := wrongEndpoint(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("wrongEndpoint(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

// Acceptance 9: which API translation prefers, and what's left once one
// has turned a model away.
func TestEndpointPreference(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	openai := provider.Provider{ID: "openai-test", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}
	copilot := provider.Provider{ID: "copilot", Chat: "https://copilot.test", Responses: "https://copilot.test", Anthropic: "https://copilot.test"}
	other := provider.Provider{ID: "other-test", Chat: "https://other.test/v1", Responses: "https://other.test/v1", Anthropic: "https://other.test"}
	rc := []provider.Protocol{provider.Responses, provider.Chat}
	cr := []provider.Protocol{provider.Chat, provider.Responses}
	for _, tc := range []struct {
		p     provider.Provider
		model string
		want  []provider.Protocol
	}{
		{openai, "gpt-6-sol", rc},
		{openai, "anything", rc},
		{copilot, "gpt-6-sol", append(rc, provider.Anthropic)},
		{copilot, "o4-mini", append(rc, provider.Anthropic)},
		{copilot, "openai/gpt-6-sol", append(rc, provider.Anthropic)},
		{copilot, "codex-mini", append(rc, provider.Anthropic)},
		{copilot, "claude-sonnet-4.5", append(cr, provider.Anthropic)},
		{other, "openai/gpt-oss-120b", append(cr, provider.Anthropic)},
		{other, "gpt-6-sol", append(cr, provider.Anthropic)},
	} {
		// Gemini is never spoken upstream, so this is the translation order
		if got := endpoints(tc.p, tc.model, provider.Gemini, true); !slices.Equal(got, tc.want) {
			t.Errorf("%s %s: %v, want %v", tc.p.ID, tc.model, got, tc.want)
		}
	}
	// the agent's own API goes first when it may be relayed
	if got := endpoints(copilot, "gpt-6-sol", provider.Chat, true); !slices.Equal(got, []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}) {
		t.Errorf("relay first: %v", got)
	}
	if got := endpoints(copilot, "gpt-6-sol", provider.Chat, false); !slices.Equal(got, []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}) {
		t.Errorf("no relay: %v", got)
	}
	// a refusal leaves only what didn't refuse, for that model alone
	markOff(copilot, "gpt-6-sol", provider.Chat)
	markOff(copilot, "gpt-6-sol", provider.Anthropic)
	if got := endpoints(copilot, "gpt-6-sol", provider.Chat, true); !slices.Equal(got, []provider.Protocol{provider.Responses}) {
		t.Errorf("after refusals: %v", got)
	}
	if got := endpoints(copilot, "o4-mini", provider.Chat, true); len(got) != 3 {
		t.Errorf("another model: %v", got)
	}
	markOff(other, "openai/gpt-oss-120b", provider.Responses)
	if got := endpoints(other, "openai/gpt-oss-120b", provider.Responses, true); !slices.Equal(got, []provider.Protocol{provider.Chat, provider.Anthropic}) {
		t.Errorf("other after refusal: %v", got)
	}
	// a declared list narrows them, whatever the order
	if err := catalog.SaveLive("copilot", "", []catalog.Model{{ID: "claude-x", APIs: []string{"anthropic", "chat"}}}); err != nil {
		t.Fatal(err)
	}
	if got := endpoints(copilot, "claude-x", provider.Responses, true); !slices.Equal(got, []provider.Protocol{provider.Chat, provider.Anthropic}) {
		t.Errorf("declared: %v", got)
	}
}
