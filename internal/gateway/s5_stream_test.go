package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// s5Up answers every request with the same event stream, and counts them.
type s5Up struct {
	mu    sync.Mutex
	calls int
	body  string
}

func (u *s5Up) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	u.mu.Lock()
	u.calls++
	u.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, u.body)
}

func (u *s5Up) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

// s5SSE joins events into a stream, each closed by a blank line.
func s5SSE(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString(e)
		b.WriteString("\n\n")
	}
	return b.String()
}

// s5AnthStart is Anthropic's opening of a reply, before any of its body.
func s5AnthStart(id string) []string {
	return []string{
		`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"` + id + `","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
		`event: ping` + "\n" + `data: {"type":"ping"}`,
	}
}

// s5AnthOverloaded is a reply that fails before its body begins.
func s5AnthOverloaded(id string) string {
	return s5SSE(append(s5AnthStart(id),
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)...)
}

// s5AnthOK is a reply whose body says text.
func s5AnthOK(id, text string) string {
	return s5SSE(append(s5AnthStart(id),
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+text+`"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)...)
}

// s5AnthPartial has its body begin, then fail.
func s5AnthPartial(id, text string) string {
	return s5SSE(append(s5AnthStart(id),
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+text+`"}}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)...)
}

// s5ChatOK is a Chat Completions reply whose body says text.
func s5ChatOK(text string) string {
	return s5SSE(
		`data: {"id":"c-ok","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"`+text+`"}}]}`,
		`data: {"id":"c-ok","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)
}

// s5ChatFailFirst is a Chat Completions reply that fails before its body.
func s5ChatFailFirst() string {
	return s5SSE(`data: {"error":{"message":"S5 busy","type":"server_error","code":null}}`)
}

// s5Env gives a test its own config and cache, and forgets who is resting.
func s5Env(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
}

// s5Provider saves a provider serving m1 over Anthropic or Chat, answered by up.
func s5Provider(t *testing.T, id string, up http.Handler, anthropic bool) {
	t.Helper()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: id, Name: id, Key: "k", Models: []string{"m1"}}
	if anthropic {
		p.Anthropic = srv.URL
	} else {
		p.Chat = srv.URL + "/v1"
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// s5Group saves an ordered group over the given members.
func s5Group(t *testing.T, id string, members ...string) {
	t.Helper()
	if err := provider.SaveGroup(provider.Group{ID: id, Name: id, Members: members, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
}

func s5Post(s *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const s5MessagesBody = `{"model":"group/%s","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
const s5ChatBody = `{"model":"group/%s","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func s5Body(format, group string) string { return strings.Replace(format, "%s", group, 1) }

// An Anthropic stream that reports overloaded before any of its body is
// dropped: the agent sees only B's reply, and A rests as a 529.
func TestS5AnthropicStreamOverloadedBeforeBodyMovesOn(t *testing.T) {
	s5Env(t)
	a := &s5Up{body: s5AnthOverloaded("msg_s5a")}
	b := &s5Up{body: s5AnthOK("msg_s5b", "from B")}
	s5Provider(t, "s5a1", a, true)
	s5Provider(t, "s5b1", b, true)
	s5Group(t, "s5g1", "s5a1/m1", "s5b1/m1")
	s := New()
	code, body := s5Post(s, "/v1/messages", s5Body(s5MessagesBody, "s5g1"))
	if code != 200 || !strings.Contains(body, "from B") || strings.Contains(body, "Overloaded") || strings.Contains(body, "msg_s5a") {
		t.Fatalf("%d %s", code, body)
	}
	if a.count() != 1 || b.count() != 1 {
		t.Fatalf("a asked %d times, b %d", a.count(), b.count())
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Status != 529 || r.Tries[0].Rest == nil || r.Tries[0].Outcome != outcomeTransferred || r.Tries[1].Status != 200 {
		t.Fatalf("tries: %+v", r.Tries)
	}
	if rest, ok := restOf("s5a1"); !ok || rest.Status != 529 {
		t.Fatalf("a not resting as a 529: %+v %v", rest, ok)
	}
}

// Once A's body has begun, its error goes to the agent as it is, and B is
// never asked.
func TestS5AnthropicStreamErrorAfterBodyIsKept(t *testing.T) {
	s5Env(t)
	a := &s5Up{body: s5AnthPartial("msg_s5a", "partial")}
	b := &s5Up{body: s5AnthOK("msg_s5b", "from B")}
	s5Provider(t, "s5a2", a, true)
	s5Provider(t, "s5b2", b, true)
	s5Group(t, "s5g2", "s5a2/m1", "s5b2/m1")
	s := New()
	code, body := s5Post(s, "/v1/messages", s5Body(s5MessagesBody, "s5g2"))
	if code != 200 || !strings.Contains(body, "partial") || !strings.Contains(body, "Overloaded") {
		t.Fatalf("%d %s", code, body)
	}
	if a.count() != 1 || b.count() != 0 {
		t.Fatalf("a asked %d times, b %d", a.count(), b.count())
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 1 {
		t.Fatalf("tries: %+v", r.Tries)
	}
}

// The same holds when the request is translated: a Chat client on an
// Anthropic-only group member.
func TestS5TranslatedStreamOverloadedBeforeBodyMovesOn(t *testing.T) {
	s5Env(t)
	a := &s5Up{body: s5AnthOverloaded("msg_s5a")}
	b := &s5Up{body: s5AnthOK("msg_s5b", "from B")}
	s5Provider(t, "s5a3", a, true)
	s5Provider(t, "s5b3", b, true)
	s5Group(t, "s5g3", "s5a3/m1", "s5b3/m1")
	s := New()
	code, body := s5Post(s, "/v1/chat/completions", s5Body(s5ChatBody, "s5g3"))
	if code != 200 || !strings.Contains(body, "from B") || strings.Contains(body, "Overloaded") || strings.Contains(body, "msg_s5a") {
		t.Fatalf("%d %s", code, body)
	}
	if a.count() != 1 || b.count() != 1 {
		t.Fatalf("a asked %d times, b %d", a.count(), b.count())
	}
}

// A translated stream that fails after its body began is not moved on.
func TestS5TranslatedStreamErrorAfterBodyIsKept(t *testing.T) {
	s5Env(t)
	a := &s5Up{body: s5AnthPartial("msg_s5a", "partial")}
	b := &s5Up{body: s5AnthOK("msg_s5b", "from B")}
	s5Provider(t, "s5a4", a, true)
	s5Provider(t, "s5b4", b, true)
	s5Group(t, "s5g4", "s5a4/m1", "s5b4/m1")
	code, body := s5Post(New(), "/v1/chat/completions", s5Body(s5ChatBody, "s5g4"))
	if code != 200 || !strings.Contains(body, "partial") || strings.Contains(body, "from B") {
		t.Fatalf("%d %s", code, body)
	}
	if a.count() != 1 || b.count() != 0 {
		t.Fatalf("a asked %d times, b %d", a.count(), b.count())
	}
}

// A Chat Completions error chunk before any content moves on the same way.
func TestS5ChatStreamErrorChunkBeforeBodyMovesOn(t *testing.T) {
	s5Env(t)
	a := &s5Up{body: s5ChatFailFirst()}
	b := &s5Up{body: s5ChatOK("from B")}
	s5Provider(t, "s5a5", a, false)
	s5Provider(t, "s5b5", b, false)
	s5Group(t, "s5g5", "s5a5/m1", "s5b5/m1")
	code, body := s5Post(New(), "/v1/chat/completions", s5Body(s5ChatBody, "s5g5"))
	if code != 200 || !strings.Contains(body, "from B") || strings.Contains(body, "S5 busy") {
		t.Fatalf("%d %s", code, body)
	}
	if a.count() != 1 || b.count() != 1 {
		t.Fatalf("a asked %d times, b %d", a.count(), b.count())
	}
}

// An error before the body that is the request's own fault goes to the
// agent as it is, and no other member is asked.
func TestS5StreamRequestErrorIsNotMovedOn(t *testing.T) {
	s5Env(t)
	a := &s5Up{body: s5SSE(append(s5AnthStart("msg_s5a"),
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"invalid_request_error","message":"messages: field required"}}`)...)}
	b := &s5Up{body: s5AnthOK("msg_s5b", "from B")}
	s5Provider(t, "s5a6", a, true)
	s5Provider(t, "s5b6", b, true)
	s5Group(t, "s5g6", "s5a6/m1", "s5b6/m1")
	code, body := s5Post(New(), "/v1/messages", s5Body(s5MessagesBody, "s5g6"))
	if code != 400 || !strings.Contains(body, "field required") || b.count() != 0 {
		t.Fatalf("%d %s, b asked %d", code, body, b.count())
	}
}

// A sole provider whose stream keeps failing before its body is asked three
// times, then the error goes to the agent.
func TestS5SoleStreamOverloadedIsRetried(t *testing.T) {
	s5Env(t)
	var waits []time.Duration
	old := retrySleep
	retrySleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	t.Cleanup(func() { retrySleep = old })
	up := &s5Up{body: s5AnthOverloaded("msg_s5solo")}
	s5Provider(t, "s5solo", up, true)
	s := New()
	code, body := s5Post(s, "/v1/messages", `{"model":"s5solo/m1","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 529 || !strings.Contains(body, "Overloaded") {
		t.Fatalf("%d %s", code, body)
	}
	if up.count() != 3 || len(waits) != 2 {
		t.Fatalf("asked %d times, waits %v", up.count(), waits)
	}
}
