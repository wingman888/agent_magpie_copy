package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// keyFake is an upstream that answers each API key with its own status (200
// when unset) and records the path and key of every call.
type keyFake struct {
	mu    sync.Mutex
	codes map[string]int
	calls []string
}

func (f *keyFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	f.calls = append(f.calls, r.URL.Path+" "+key)
	code := f.codes[key]
	f.mu.Unlock()
	if code != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
		return
	}
	stream := streamOf(body)
	switch r.URL.Path {
	case "/v1/chat/completions":
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`,
				`data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
				`data: [DONE]`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	case "/v1/messages":
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m1\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}",
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}",
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}",
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}",
				"event: message_stop\ndata: {\"type\":\"message_stop\"}"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *keyFake) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// keyedUp isolates magpie's files and starts the fake upstream.
func keyedUp(t *testing.T, codes map[string]int) (*keyFake, *httptest.Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &keyFake{codes: codes}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	return f, up
}

func doKeyed(s *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

const chatBody = `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`

func TestKeysFailOverToNextKey(t *testing.T) {
	f, up := keyedUp(t, map[string]int{"k-one": 429})
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k-one", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "team", "k-two", ""); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, out := doKeyed(s, "/v1/chat/completions", chatBody); code != 200 {
		t.Fatalf("first request: %d %s", code, out)
	}
	if got := strings.Join(f.seen(), ", "); got != "/v1/chat/completions k-one, /v1/chat/completions k-two" {
		t.Fatalf("calls: %s", got)
	}
	c := s.Recent()[0]
	if c.Status != 200 || c.Provider != "relay" || c.Fallback != "relay" {
		t.Errorf("call record = %+v; want the first key named as the failed candidate", c)
	}

	// the next request goes straight to the second key
	if code, out := doKeyed(s, "/v1/chat/completions", chatBody); code != 200 {
		t.Fatalf("second request: %d %s", code, out)
	}
	calls := f.seen()
	if len(calls) != 3 || calls[2] != "/v1/chat/completions k-two" {
		t.Errorf("second request calls: %v", calls)
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Errorf("second request should not fall back: %q", c.Fallback)
	}
}

func TestDisabledKeyIsNeverTried(t *testing.T) {
	f, up := keyedUp(t, map[string]int{"k-one": 429})
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k-one", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "off", "k-two", ""); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "on", "k-three", ""); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyOn("relay", provider.KeyRef("k-two"), false); err != nil {
		t.Fatal(err)
	}
	if code, out := doKeyed(New(), "/v1/chat/completions", chatBody); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	if got := strings.Join(f.seen(), ", "); got != "/v1/chat/completions k-one, /v1/chat/completions k-three" {
		t.Errorf("calls: %s", got)
	}
}

func TestLastKeyErrorIsReturned(t *testing.T) {
	_, up := keyedUp(t, map[string]int{"k-one": 429, "k-two": 429})
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k-one", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "team", "k-two", ""); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, out := doKeyed(s, "/v1/chat/completions", chatBody)
	if code != 429 || !strings.Contains(out, "rate limit") {
		t.Errorf("got %d %s; want the last candidate's 429", code, out)
	}
	if c := s.Recent()[0]; c.Status != 429 || c.Fallback != "relay" {
		t.Errorf("call record = %+v", c)
	}
}

func TestBadRequestIsNotRetried(t *testing.T) {
	f, up := keyedUp(t, map[string]int{"k-one": 400})
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k-one", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "team", "k-two", ""); err != nil {
		t.Fatal(err)
	}
	if code, _ := doKeyed(New(), "/v1/chat/completions", chatBody); code != 400 {
		t.Errorf("status %d, want 400 passed through", code)
	}
	if got := f.seen(); len(got) != 1 {
		t.Errorf("a 400 moved on to another key: %v", got)
	}
}

func TestKeysFollowTheirProtocol(t *testing.T) {
	f, up := keyedUp(t, nil)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k-ant",
		Models: []string{"m1", "claude-sonnet-5", "gpt-5.5"}, Chat: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyProtocol("relay", provider.KeyRef("k-ant"), provider.Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "oai", "k-oai", provider.Chat); err != nil {
		t.Fatal(err)
	}
	s := New()
	anthropicBody := func(model string) string {
		return `{"model":"` + model + `","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`
	}
	openaiBody := func(model string) string {
		return `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
	}
	cases := []struct{ path, body, want string }{
		// Claude Code asks for a Claude model: the Anthropic key, relayed
		{"/v1/messages", anthropicBody("claude-sonnet-5"), "/v1/messages k-ant"},
		// an OpenAI client asks for a Claude model: translated, still the Anthropic key
		{"/v1/chat/completions", openaiBody("claude-sonnet-5"), "/v1/messages k-ant"},
		// Claude Code asks for a GPT model: translated to the OpenAI key
		{"/v1/messages", anthropicBody("gpt-5.5"), "/v1/chat/completions k-oai"},
	}
	for _, c := range cases {
		if code, out := doKeyed(s, c.path, c.body); code != 200 {
			t.Fatalf("%s: %d %s", c.path, code, out)
		}
		calls := f.seen()
		if got := calls[len(calls)-1]; got != c.want {
			t.Errorf("%s %s: used %q, want %q", c.path, c.body, got, c.want)
		}
	}
	for _, c := range f.seen() {
		if strings.HasPrefix(c, "/v1/messages k-oai") || strings.HasPrefix(c, "/v1/chat/completions k-ant") {
			t.Errorf("a key reached an endpoint it is not limited to: %s", c)
		}
	}
}

func TestCountTokensNeverUsesOpenAIOnlyKey(t *testing.T) {
	f, up := keyedUp(t, nil)
	if err := provider.Save(provider.Provider{ID: "oai", Name: "OAI", Key: "k-oai", Models: []string{"m-oai"},
		Chat: up.URL + "/v1", Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyProtocol("oai", provider.KeyRef("k-oai"), provider.Chat); err != nil {
		t.Fatal(err)
	}
	code, out := doKeyed(New(), "/v1/messages/count_tokens", `{"model":"m-oai","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(out, "input_tokens") {
		t.Fatalf("%d %s", code, out)
	}
	if got := f.seen(); len(got) != 0 {
		t.Errorf("token counting reached the upstream with an OpenAI-only key: %v", got)
	}
}

func TestKeyLimitedToMissingEndpointIsNotUsed(t *testing.T) {
	f, up := keyedUp(t, nil)
	// the provider only speaks chat; its only key is limited to Anthropic
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k-ant", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyProtocol("relay", provider.KeyRef("k-ant"), provider.Anthropic); err != nil {
		t.Fatal(err)
	}
	code, out := doKeyed(New(), "/v1/chat/completions", chatBody)
	if code != 502 || !strings.Contains(out, "has no usable key") {
		t.Errorf("got %d %s; want 502 with \"has no usable key\"", code, out)
	}
	if got := f.seen(); len(got) != 0 {
		t.Errorf("a key limited to a missing endpoint reached the upstream: %v", got)
	}
}
