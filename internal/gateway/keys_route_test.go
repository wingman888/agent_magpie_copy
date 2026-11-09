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

// mkUpstream answers like a vendor with several keys: "sk-one" is out of
// quota, "sk-two" works, and every request is logged as "path key".
type mkUpstream struct {
	mu   sync.Mutex
	hits []string
}

func (u *mkUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	u.mu.Lock()
	u.hits = append(u.hits, r.URL.Path+" "+key)
	u.mu.Unlock()
	switch {
	case key == "sk-one":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
	case r.URL.Path == "/v1/messages/count_tokens":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":7}`)
	case r.URL.Path == "/v1/messages" && strings.Contains(string(body), `"stream":true`):
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: "+`{"type":"message_start","message":{"id":"m1","model":"claude-x","usage":{"input_tokens":1,"output_tokens":0}}}`+"\n\n"+
			"event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
			"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`+"\n\n"+
			"event: content_block_stop\ndata: "+`{"type":"content_block_stop","index":0}`+"\n\n"+
			"event: message_delta\ndata: "+`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`+"\n\n"+
			"event: message_stop\ndata: "+`{"type":"message_stop"}`+"\n\n")
	case r.URL.Path == "/v1/messages":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"claude-x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	case strings.Contains(string(body), `"stream":true`):
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+`{"id":"c1","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`+"\n\n"+
			"data: "+`{"id":"c1","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`+"\n\n"+
			"data: [DONE]\n\n")
	default:
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}
}

// log returns the requests seen so far.
func (u *mkUpstream) log() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.hits...)
}

// mkSetup starts the fake and saves a provider with the given fields filled in.
func mkSetup(t *testing.T, p provider.Provider) (*mkUpstream, *httptest.Server) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	u := &mkUpstream{}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	if p.Chat != "" {
		p.Chat = up.URL + "/v1"
	}
	if p.Anthropic != "" {
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return u, up
}

func mkPost(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec
}

func TestKeyRouteNextKeyAnswersAndIsRemembered(t *testing.T) {
	u, _ := mkSetup(t, provider.Provider{ID: "multi", Name: "Multi", Chat: "x", Key: "sk-one", KeyName: "personal",
		Keys:   []provider.KeyEntry{{Name: "team", Key: "sk-two"}, {Name: "old", Key: "sk-off", Off: true}},
		Models: []string{"m1"}})
	s := New()
	rec := mkPost(t, s, "/v1/chat/completions", `{"model":"multi/m1","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(u.log(), ", "); got != "/v1/chat/completions sk-one, /v1/chat/completions sk-two" {
		t.Fatalf("first request tried %q", got)
	}
	calls := s.Recent()
	if len(calls) == 0 || calls[0].Provider != "multi" || calls[0].Status != 200 {
		t.Fatalf("unexpected call record %+v", calls)
	}
	if !strings.Contains(calls[0].Fallback, "multi") || !strings.Contains(calls[0].Fallback, "personal") {
		t.Fatalf("the call should say which key failed, got %q", calls[0].Fallback)
	}

	// the next request goes straight to the key that answered
	rec = mkPost(t, s, "/v1/chat/completions", `{"model":"multi/m1","messages":[{"role":"user","content":"again"}]}`)
	if rec.Code != 200 {
		t.Fatalf("second status %d: %s", rec.Code, rec.Body.String())
	}
	if got := u.log(); len(got) != 3 || got[2] != "/v1/chat/completions sk-two" {
		t.Fatalf("second request should use sk-two only, log %v", got)
	}
	for _, h := range u.log() {
		if strings.HasSuffix(h, "sk-off") {
			t.Fatal("a key that is off must never be tried")
		}
	}
}

func TestKeyRouteAnthropicKeyServesClaudeOnly(t *testing.T) {
	u, _ := mkSetup(t, provider.Provider{ID: "pl", Name: "PL", Chat: "x", Anthropic: "x",
		Key: "sk-chat", KeyProtocol: provider.Chat,
		Keys: []provider.KeyEntry{{Name: "ant", Key: "sk-ant", Protocol: provider.Anthropic}}})
	s := New()
	rec := mkPost(t, s, "/v1/messages", `{"model":"pl/claude-x","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(u.log(), ", "); got != "/v1/messages sk-ant" {
		t.Fatalf("a Claude model should reach only the Anthropic endpoint with the Anthropic key, got %q", got)
	}
}

func TestKeyRouteOpenAIClientClaudeModelTranslatesToAnthropicKey(t *testing.T) {
	u, _ := mkSetup(t, provider.Provider{ID: "pl", Name: "PL", Chat: "x", Anthropic: "x",
		Key: "sk-chat", KeyProtocol: provider.Chat,
		Keys: []provider.KeyEntry{{Name: "ant", Key: "sk-ant", Protocol: provider.Anthropic}}})
	s := New()
	rec := mkPost(t, s, "/v1/chat/completions", `{"model":"pl/claude-x","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	for _, h := range u.log() {
		if strings.HasPrefix(h, "/v1/chat/completions") {
			t.Fatalf("a Claude model must not reach the Chat endpoint, log %v", u.log())
		}
	}
	if got := strings.Join(u.log(), ", "); got != "/v1/messages sk-ant" {
		t.Fatalf("log %q", got)
	}
}

func TestKeyRouteClaudeClientGPTModelTranslatesToOpenAIKey(t *testing.T) {
	u, _ := mkSetup(t, provider.Provider{ID: "pl", Name: "PL", Chat: "x", Anthropic: "x",
		Key: "sk-chat", KeyProtocol: provider.Chat,
		Keys: []provider.KeyEntry{{Name: "ant", Key: "sk-ant", Protocol: provider.Anthropic}}})
	s := New()
	mkPost(t, s, "/v1/messages", `{"model":"pl/gpt-5.5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	if got := strings.Join(u.log(), ", "); got != "/v1/chat/completions sk-chat" {
		t.Fatalf("a GPT model from a Claude client should translate to the OpenAI key, got %q", got)
	}
}

func TestKeyRouteCountTokensSkipsOpenAIKey(t *testing.T) {
	u, _ := mkSetup(t, provider.Provider{ID: "pl", Name: "PL", Chat: "x", Anthropic: "x",
		Key: "sk-chat", KeyProtocol: provider.Chat})
	s := New()
	rec := mkPost(t, s, "/v1/messages/count_tokens", `{"model":"pl/claude-x","messages":[{"role":"user","content":"hello there"}]}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := u.log(); len(got) != 0 {
		t.Fatalf("an OpenAI-only key must not count Anthropic tokens, upstream saw %v", got)
	}
}
