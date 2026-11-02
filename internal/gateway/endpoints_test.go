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

// endpointFake serves the three APIs, each answering with a reply of its
// own unless told to refuse; it counts what it got per path.
type endpointFake struct {
	mu     sync.Mutex
	calls  map[string]int
	refuse map[string]refusal // by path
}

type refusal struct {
	code int
	body string
}

const (
	chatPath      = "/v1/chat/completions"
	responsesPath = "/v1/responses"
	messagesPath  = "/v1/messages"
)

func (f *endpointFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[r.URL.Path]++
	ref, refused := f.refuse[r.URL.Path]
	f.mu.Unlock()
	if refused {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(ref.code)
		io.WriteString(w, ref.body)
		return
	}
	switch r.URL.Path {
	case chatPath:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","model":"m","choices":[{"delta":{"role":"assistant","content":"from chat"}}]}`,
			`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
			`data: [DONE]`))
	case responsesPath:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m","usage":{"input_tokens":0,"output_tokens":0}}}`,
			`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"from responses"}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`))
	case messagesPath:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from messages"}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
	default:
		http.NotFound(w, r)
	}
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

// endpointSetup adds provider "fake" with the given endpoints on the fake.
func endpointSetup(t *testing.T, f *endpointFake, models []string, protos ...provider.Protocol) {
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
}

func serveOn(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestChatClientMovesToResponsesAndRemembers(t *testing.T) {
	f := &endpointFake{refuse: map[string]refusal{chatPath: {400,
		`{"error":{"message":"model \"m1\" is not accessible via the /chat/completions endpoint","code":"unsupported_api_for_model"}}`}}}
	endpointSetup(t, f, []string{"m1"}, provider.Chat, provider.Responses)
	h := New().Handler()
	for _, stream := range []string{"false", "true"} {
		code, body := serveOn(h, "/v1/chat/completions", `{"model":"m1","stream":`+stream+`,"messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(body, "from responses") || strings.Contains(body, "not accessible") {
			t.Fatalf("stream=%s: %d %s", stream, code, body)
		}
	}
	if f.count(chatPath) != 1 || f.count(responsesPath) != 2 {
		t.Fatalf("calls: %v", f.calls)
	}
	// another model of the same provider is untouched by the memory
	serveOn(h, "/v1/chat/completions", `{"model":"fake/m2","messages":[{"role":"user","content":"hi"}]}`)
	if f.count(chatPath) != 2 {
		t.Fatalf("m2 should try chat first: %v", f.calls)
	}
}

func TestResponsesClientMovesToChatAndRemembers(t *testing.T) {
	f := &endpointFake{refuse: map[string]refusal{responsesPath: {400,
		`{"error":{"message":"The requested model is not supported.","code":"unsupported_api_for_model","type":"invalid_request_error"}}`}}}
	endpointSetup(t, f, []string{"m1"}, provider.Chat, provider.Responses)
	h := New().Handler()
	code, body := serveOn(h, "/v1/responses", `{"model":"m1","input":"hi"}`)
	if code != 200 || !strings.Contains(body, "from chat") {
		t.Fatalf("first: %d %s", code, body)
	}
	if f.count(responsesPath) != 1 || f.count(chatPath) != 1 {
		t.Fatalf("calls: %v", f.calls)
	}
	code, body = serveOn(h, "/v1/responses", `{"model":"m1","input":"again","stream":true}`)
	if code != 200 || !strings.Contains(body, "from chat") {
		t.Fatalf("second: %d %s", code, body)
	}
	if f.count(responsesPath) != 1 || f.count(chatPath) != 2 {
		t.Fatalf("second calls: %v", f.calls)
	}
}

func TestDeclaredAPIsAreNotProbed(t *testing.T) {
	for _, tc := range []struct {
		path, body, want string
	}{
		{"/v1/messages", `{"model":"gpt-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, responsesPath},
		{"/v1/chat/completions", `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`, responsesPath},
		{"/v1/responses", `{"model":"claude-x","input":"hi"}`, chatPath},
		{"/v1/messages", `{"model":"claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, chatPath},
		{"/v1/chat/completions", `{"model":"claude-x","stream":true,"messages":[{"role":"user","content":"hi"}]}`, chatPath},
	} {
		t.Run(tc.path+" "+tc.want, func(t *testing.T) {
			f := &endpointFake{}
			endpointSetup(t, f, []string{"gpt-x", "claude-x"}, provider.Chat, provider.Responses, provider.Anthropic)
			if err := catalog.SaveLive("fake", "http://x", []catalog.Model{
				{ID: "gpt-x", APIs: []string{"responses"}}, {ID: "claude-x", APIs: []string{"chat"}}}); err != nil {
				t.Fatal(err)
			}
			code, body := serveOn(New().Handler(), tc.path, tc.body)
			if code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			if f.count(tc.want) != 1 || f.total() != 1 {
				t.Fatalf("want one call to %s, got %v", tc.want, f.calls)
			}
		})
	}
}

func TestServerErrorWithEndpointWordsDoesNotMove(t *testing.T) {
	f := &endpointFake{refuse: map[string]refusal{chatPath: {500, `{"error":{"message":"oops, use v1/responses"}}`}}}
	endpointSetup(t, f, []string{"m1"}, provider.Chat, provider.Responses)
	code, body := serveOn(New().Handler(), "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 500 || f.count(responsesPath) != 0 {
		t.Fatalf("%d %s, calls %v", code, body, f.calls)
	}
	if wrongEndpoint(200, []byte("use v1/responses")) || wrongEndpoint(500, []byte("use v1/responses")) || wrongEndpoint(503, []byte("not a chat model")) {
		t.Error("only a 4xx moves")
	}
	for _, msg := range []string{
		`model x is not accessible via the /chat/completions endpoint`,
		`{"error":{"code":"unsupported_api_for_model"}}`,
		`Use v1/responses`,
		`please use /v1/chat/completions`,
		`This model is not supported in the v1/responses endpoint`,
		`not a chat model`,
	} {
		if !wrongEndpoint(400, []byte(msg)) {
			t.Errorf("400 %q should move", msg)
		}
	}
	for _, msg := range []string{`messages: field required`, `context length exceeded`, `model not found`} {
		if wrongEndpoint(400, []byte(msg)) {
			t.Errorf("400 %q should not move", msg)
		}
	}
}

func TestLastEndpointRefusalIsPassedOn(t *testing.T) {
	refused := `{"error":{"message":"model is not accessible via the /chat/completions endpoint"}}`
	f := &endpointFake{refuse: map[string]refusal{chatPath: {400, refused}, responsesPath: {404, `{"error":{"message":"use /v1/chat/completions"}}`}}}
	endpointSetup(t, f, []string{"m1"}, provider.Chat, provider.Responses)
	code, body := serveOn(New().Handler(), "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 404 || !strings.Contains(body, "use /v1/chat/completions") {
		t.Fatalf("%d %s", code, body)
	}
	if f.count(chatPath) != 1 || f.count(responsesPath) != 1 {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestEndpointPreference(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	s := New()
	all := func(p provider.Provider) provider.Provider {
		if p.Chat == "" {
			p.Chat = "https://relay.example/v1"
		}
		p.Responses, p.Anthropic = p.Chat, strings.TrimSuffix(p.Chat, "/v1")
		return p
	}
	openai := all(provider.Provider{ID: "openai", Chat: "https://api.openai.com/v1"})
	copilot := all(provider.Provider{ID: "copilot", Account: &provider.Account{Agent: "copilot"}})
	other := all(provider.Provider{ID: "relay"})
	R, C, A := provider.Responses, provider.Chat, provider.Anthropic
	for _, tc := range []struct {
		p     provider.Provider
		model string
		want  []provider.Protocol
	}{
		{openai, "gpt-5", []provider.Protocol{R, C, A}},
		{openai, "whatever", []provider.Protocol{R, C, A}},
		{copilot, "gpt-6-sol", []provider.Protocol{R, C, A}},
		{copilot, "o4-mini", []provider.Protocol{R, C, A}},
		{copilot, "codex-mini", []provider.Protocol{R, C, A}},
		{copilot, "claude-sonnet-5", []provider.Protocol{C, R, A}},
		{other, "gpt-5", []provider.Protocol{C, R, A}},
		{other, "openai/gpt-oss-120b", []provider.Protocol{C, R, A}},
	} {
		// a Gemini client speaks nothing upstream: every endpoint is a translation
		if got := s.endpoints(tc.p, tc.model, provider.Gemini); !slices.Equal(got, tc.want) {
			t.Errorf("%s %s: %v, want %v", tc.p.ID, tc.model, got, tc.want)
		}
	}
	// the client's own API goes first, as-is
	if got := s.endpoints(copilot, "gpt-6-sol", C); !slices.Equal(got, []provider.Protocol{C, R, A}) {
		t.Errorf("chat client: %v", got)
	}
	// once refused, an endpoint is left out for that model only
	s.markUnserved("copilot", "gpt-6-sol", C)
	if got := s.endpoints(copilot, "gpt-6-sol", C); !slices.Equal(got, []provider.Protocol{R, A}) {
		t.Errorf("after refusal: %v", got)
	}
	if got := s.endpoints(copilot, "gpt-5", C); !slices.Equal(got, []provider.Protocol{C, R, A}) {
		t.Errorf("another model: %v", got)
	}
	if got := s.endpoints(other, "gpt-6-sol", C); !slices.Equal(got, []provider.Protocol{C, R, A}) {
		t.Errorf("another provider: %v", got)
	}
}

func TestCountTokensOnlyWhereAnthropicServes(t *testing.T) {
	f := &endpointFake{}
	endpointSetup(t, f, []string{"gpt-x", "claude-x"}, provider.Chat, provider.Anthropic)
	if err := catalog.SaveLive("fake", "http://x", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"chat"}}, {ID: "claude-x", APIs: []string{"chat", "anthropic"}}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	code, body := serveOn(h, "/v1/messages/count_tokens", `{"model":"gpt-x","messages":[{"role":"user","content":"hello there"}]}`)
	if code != 200 || !strings.Contains(body, "input_tokens") || f.total() != 0 {
		t.Fatalf("gpt-x: %d %s, calls %v", code, body, f.calls)
	}
	serveOn(h, "/v1/messages/count_tokens", `{"model":"claude-x","messages":[{"role":"user","content":"hello there"}]}`)
	if f.count("/v1/messages/count_tokens") != 1 {
		t.Fatalf("claude-x should be counted upstream: %v", f.calls)
	}
}
