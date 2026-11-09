package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// s6Upstream answers every request with one fixed reply and counts requests.
type s6Upstream struct {
	mu    sync.Mutex
	calls int
	ctype string
	body  string
}

func (u *s6Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	u.mu.Lock()
	u.calls++
	u.mu.Unlock()
	w.Header().Set("Content-Type", u.ctype)
	io.WriteString(w, u.body)
}

func (u *s6Upstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

// s6Serve points magpie at one provider "s6p" that speaks Responses only, at
// the given fake upstream, and returns magpie's handler.
func s6Serve(t *testing.T, u *s6Upstream) http.Handler {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "s6p", Name: "S6", Key: "k", Models: []string{"m1"}, Responses: up.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return New().Handler()
}

func s6Post(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// s6ContextFailed is a Responses stream that fails before any body, because
// the request is longer than the model's context window.
const s6ContextFailed = "event: response.failed\n" +
	`data: {"type":"response.failed","response":{"id":"r1","status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}}` + "\n\n"

// s6NoError is a Responses stream whose response failed without saying why.
const s6NoError = `data: {"type":"response.created","response":{"id":"r1","model":"m1"}}` + "\n\n" +
	`data: {"type":"response.failed","response":{"id":"r1","status":"failed"}}` + "\n\n"

// (1) translation order: OpenAI models go Responses first on OpenAI's API and
// on Copilot; other vendors, gpt-oss included, go Chat first.
func TestS6TranslationOrder(t *testing.T) {
	openai := provider.Provider{ID: "s6openai", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}
	copilot := provider.Provider{ID: "s6copilot", Chat: "https://api.githubcopilot.com", Responses: "https://api.githubcopilot.com", Anthropic: "https://api.githubcopilot.com"}
	other := provider.Provider{ID: "s6other", Chat: "https://openrouter.example/v1", Responses: "https://openrouter.example/v1"}
	responsesFirst := []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	chatFirst := []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}
	cases := []struct {
		p     provider.Provider
		model string
		want  []provider.Protocol
	}{
		{openai, "gpt-6-sol", responsesFirst},
		{openai, "o4-mini", responsesFirst},
		{copilot, "gpt-6-sol", responsesFirst},
		{copilot, "o4-mini", responsesFirst},
		{copilot, "claude-sonnet-5-5", chatFirst},
		{copilot, "openai/gpt-oss-120b", chatFirst},
		{openai, "openai/gpt-oss-120b", chatFirst},
		{other, "openai/gpt-oss-120b", chatFirst},
		{other, "gpt-6-sol", chatFirst},
	}
	for _, c := range cases {
		if got := translationOrder(c.p, c.model); !slices.Equal(got, c.want) {
			t.Errorf("%s %s: order %v, want %v", c.p.Host(), c.model, got, c.want)
		}
	}
}

// (2) a refusal leaves only the endpoints not refused, for that model alone.
func TestS6RefusedEndpointIsLeftOutForThatModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := provider.Provider{ID: "s6copilot", Chat: "https://api.githubcopilot.com", Responses: "https://api.githubcopilot.com"}
	s := New()
	s.markRefused(p.ID, "gpt-6-sol", provider.Responses)
	got := s.endpoints(p, "gpt-6-sol", provider.Chat, false)
	if len(got) != 1 || got[0].proto != provider.Chat {
		t.Fatalf("gpt-6-sol after Responses refused: %v, want only Chat", got)
	}
	got = s.endpoints(p, "o4-mini", provider.Chat, false)
	if len(got) != 2 || got[0].proto != provider.Responses {
		t.Fatalf("o4-mini: %v, want Responses then Chat", got)
	}
	// with every endpoint refused, they are tried after all
	s.markRefused(p.ID, "gpt-6-sol", provider.Chat)
	if got = s.endpoints(p, "gpt-6-sol", provider.Chat, false); len(got) != 2 {
		t.Fatalf("all refused: %v, want both tried", got)
	}
}

// (3) a streamed context overflow is the request's own 400: asked once, and
// the agent gets the 400.
func TestS6ResponsesStreamContextOverflowIsAgentsError(t *testing.T) {
	u := &s6Upstream{ctype: "text/event-stream", body: s6ContextFailed}
	h := s6Serve(t, u)
	code, body := s6Post(h, "/v1/responses", `{"model":"s6p/m1","input":"hi","stream":true}`)
	if code != 400 {
		t.Fatalf("status %d, body %s; want 400", code, body)
	}
	if n := u.count(); n != 1 {
		t.Fatalf("upstream asked %d times, want 1", n)
	}
}

// (3b) the same for a non-streamed Chat request translated to Responses.
func TestS6ChatNonStreamContextOverflowIsAgentsError(t *testing.T) {
	u := &s6Upstream{ctype: "text/event-stream", body: s6ContextFailed}
	h := s6Serve(t, u)
	code, body := s6Post(h, "/v1/chat/completions", `{"model":"s6p/m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 {
		t.Fatalf("status %d, body %s; want 400", code, body)
	}
	if n := u.count(); n != 1 {
		t.Fatalf("upstream asked %d times, want 1", n)
	}
}

// (4) a Responses failure with no error object is an error, not an empty
// successful reply. It is a 502, which the last provider may retry.
func TestS6ResponseFailedWithoutErrorIsNotASuccess(t *testing.T) {
	old := retrySleep
	retrySleep = func(ctx context.Context, d time.Duration) error { return nil }
	t.Cleanup(func() { retrySleep = old })

	u := &s6Upstream{ctype: "text/event-stream", body: s6NoError}
	h := s6Serve(t, u)
	code, body := s6Post(h, "/v1/responses", `{"model":"s6p/m1","input":"hi","stream":true}`)
	if code != 502 || !strings.Contains(body, "response failed") {
		t.Fatalf("status %d, body %s; want 502 carrying response failed", code, body)
	}
}

// (5) the status a stream error event stands for.
func TestS6StreamErrorStatus(t *testing.T) {
	cases := []struct {
		data string
		want int
	}{
		{`{"error":{"code":"unsupported_api_for_model","message":"x"}}`, 400},
		{`{"error":{"code":"model_not_found","message":"x"}}`, 404},
		{`{"error":{"code":"context_length_exceeded","message":"x"}}`, 400},
		{`{"error":{"message":"model m1 is not accessible via the /chat/completions endpoint"}}`, 400},
		{`{"error":{"message":"Please use v1/responses for this model"}}`, 400},
		{`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, 529},
		{`{"type":"response.failed","response":{"status":"failed"}}`, 502},
		{`{"error":{"type":"invalid_request_error","message":"messages: field required"}}`, 400},
		{`{"error":{"code":503,"message":"x"}}`, 503},
	}
	for _, c := range cases {
		if got := streamErrorStatus(c.data); got != c.want {
			t.Errorf("streamErrorStatus(%s) = %d, want %d", c.data, got, c.want)
		}
	}
}
