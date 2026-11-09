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

// s2kUpstream answers each request by the key it carries: a key in fail gets
// that status, the others get a stream in the shape of the path's protocol.
// It keeps "path key" for every request, in order.
type s2kUpstream struct {
	mu   sync.Mutex
	seen []string
	fail map[string]int // Authorization value -> status
}

const s2kChatReply = `data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}

data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`

var s2kAnthropicReply = sse(
	"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"m1\",\"usage\":{\"input_tokens\":7}}}",
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
	`data: {"type":"content_block_stop","index":0}`,
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
	`data: {"type":"message_stop"}`)

func (u *s2kUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	u.mu.Lock()
	u.seen = append(u.seen, r.URL.Path+" "+auth)
	code := u.fail[auth]
	u.mu.Unlock()
	if code != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"upstream said no"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if r.URL.Path == "/v1/messages" {
		io.WriteString(w, s2kAnthropicReply)
		return
	}
	io.WriteString(w, s2kChatReply)
}

// take returns the requests seen since the last call and forgets them.
func (u *s2kUpstream) take() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := u.seen
	u.seen = nil
	return out
}

// s2kSetup saves a chat-speaking provider "s2kfake" with key k1 against an
// upstream that fails the keys in fail.
func s2kSetup(t *testing.T, fail map[string]int) (*s2kUpstream, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	u := &s2kUpstream{fail: fail}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "s2kfake", Name: "s2kfake", Key: "k1",
		Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	return u, up.URL
}

func s2kPost(s *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func s2kMustAdd(t *testing.T, id, name, key string, proto provider.Protocol) {
	t.Helper()
	if err := provider.AddKey(id, name, key, proto); err != nil {
		t.Fatal(err)
	}
}

const s2kChatBody = `{"model":"s2kfake/m1","messages":[{"role":"user","content":"hi"}]}`

func TestS2KSecondKeyAnswersAfterRateLimit(t *testing.T) {
	u, _ := s2kSetup(t, map[string]int{"Bearer k1": 429})
	if err := provider.RenameKey("s2kfake", provider.KeyRef("k1"), "personal"); err != nil {
		t.Fatal(err)
	}
	s2kMustAdd(t, "s2kfake", "backup", "k2", "")
	s2kMustAdd(t, "s2kfake", "", "k3", "")
	if err := provider.SetKeyOn("s2kfake", provider.KeyRef("k3"), false); err != nil {
		t.Fatal(err)
	}

	s := New()
	code, body := s2kPost(s, "/v1/chat/completions", s2kChatBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := strings.Join(u.take(), "|"); got != "/v1/chat/completions Bearer k1|/v1/chat/completions Bearer k2" {
		t.Errorf("upstream saw %s", got)
	}
	c := s.Recent()[0]
	if c.Status != 200 || c.Provider != "s2kfake" {
		t.Errorf("call: status %d provider %q", c.Status, c.Provider)
	}
	if c.Fallback != "s2kfake (personal)" {
		t.Errorf("fallback: %q", c.Fallback)
	}
	if c.KeyName != "backup" {
		t.Errorf("key used: %q", c.KeyName)
	}

	// the next request goes straight to the key that answered
	if code, body := s2kPost(s, "/v1/chat/completions", s2kChatBody); code != 200 {
		t.Fatalf("second status %d: %s", code, body)
	}
	if got := strings.Join(u.take(), "|"); got != "/v1/chat/completions Bearer k2" {
		t.Errorf("second request saw %s", got)
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Errorf("second call should not fall back: %q", c.Fallback)
	}
}

func TestS2KBadRequestIsNotRetried(t *testing.T) {
	u, _ := s2kSetup(t, map[string]int{"Bearer k1": 400})
	s2kMustAdd(t, "s2kfake", "", "k2", "")
	s := New()
	code, body := s2kPost(s, "/v1/chat/completions", s2kChatBody)
	if code != 400 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := strings.Join(u.take(), "|"); got != "/v1/chat/completions Bearer k1" {
		t.Errorf("a 400 should not move on; upstream saw %s", got)
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Errorf("fallback on a 400: %q", c.Fallback)
	}
}

func TestS2KLastKeysErrorReachesClient(t *testing.T) {
	u, _ := s2kSetup(t, map[string]int{"Bearer k1": 429, "Bearer k2": 429})
	s2kMustAdd(t, "s2kfake", "", "k2", "")
	s := New()
	code, body := s2kPost(s, "/v1/chat/completions", s2kChatBody)
	if code != 429 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := len(u.take()); got != 2 {
		t.Errorf("upstream calls: %d", got)
	}
	if c := s.Recent()[0]; c.Status != 429 || !strings.Contains(c.Fallback, "s2kfake") {
		t.Errorf("call: status %d fallback %q", c.Status, c.Fallback)
	}
}

func TestS2KKeysStayOnTheirEndpoints(t *testing.T) {
	u, base := s2kSetup(t, nil)
	if err := provider.Save(provider.Provider{ID: "s2kmix", Name: "s2kmix", Key: "ok",
		Chat: base + "/v1", Anthropic: base, Models: []string{"claude-x", "gpt-x"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyProtocol("s2kmix", provider.KeyRef("ok"), provider.Chat); err != nil {
		t.Fatal(err)
	}
	s2kMustAdd(t, "s2kmix", "anthropic", "ak", provider.Anthropic)
	s := New()

	// Claude Code asks for a Claude model: the Anthropic key, Anthropic endpoint
	code, body := s2kPost(s, "/v1/messages", `{"model":"s2kmix/claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("claude via anthropic: status %d: %s", code, body)
	}
	if got := strings.Join(u.take(), "|"); got != "/v1/messages Bearer ak" {
		t.Errorf("claude via anthropic: upstream saw %s", got)
	}

	// an OpenAI-style client asks for a Claude model: translated to the Anthropic endpoint
	code, body = s2kPost(s, "/v1/chat/completions", `{"model":"s2kmix/claude-x","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("claude from chat: status %d: %s", code, body)
	}
	if got := strings.Join(u.take(), "|"); got != "/v1/messages Bearer ak" {
		t.Errorf("claude from chat: upstream saw %s", got)
	}

	// Claude Code asks for a GPT model: translated to the OpenAI endpoint with the OpenAI key
	code, body = s2kPost(s, "/v1/messages", `{"model":"s2kmix/gpt-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("gpt from anthropic: status %d: %s", code, body)
	}
	if got := strings.Join(u.take(), "|"); got != "/v1/chat/completions Bearer ok" {
		t.Errorf("gpt from anthropic: upstream saw %s", got)
	}
}

func TestS2KRetryableStatuses(t *testing.T) {
	for _, c := range []struct {
		status int
		msg    string
		want   bool
	}{
		{429, "", true},
		{402, "", true},
		{529, "", true},
		{503, "", true},
		{403, "quota exceeded for this key", true},
		{403, "额度已用完", true},
		{403, "forbidden", false},
		{400, "bad request", false},
	} {
		if got := rsiRetryable(c.status, c.msg); got != c.want {
			t.Errorf("retryable(%d, %q) = %v", c.status, c.msg, got)
		}
	}
}
