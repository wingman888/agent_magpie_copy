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

// answer is one upstream reply.
type answer func(w http.ResponseWriter, r *http.Request)

// script answers a provider's requests with its replies in turn; the last
// one repeats.
type script struct {
	replies []answer
	n       int // requests received
}

func (s *script) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	i := min(s.n, len(s.replies)-1)
	s.n++
	s.replies[i](w, r)
}

func replyWith(code int, body string, header map[string]string) answer {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(code)
		io.WriteString(w, body)
	}
}

func chatText(text string) answer {
	return replyWith(200, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"`+text+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, nil)
}

func streamAnswer(frames ...string) answer {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(frames...))
	}
}

// frameText is one event of a stream: its name and its data.
func frameText(name, data string) string {
	return "event: " + name + "\n" + "data: " + data
}

// serveAnthropicOn is serveOn for a provider that speaks the Anthropic API.
func serveAnthropicOn(t *testing.T, id, key string, models []string, v http.Handler) {
	t.Helper()
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: strings.ToUpper(id), Key: key, Models: models, Anthropic: up.URL}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// chatGroup serves a routing group "mine" that tries a/m, then b/m.
func chatGroup(t *testing.T, a, b http.Handler) {
	t.Helper()
	serveOn(t, "a", "ka", []string{"m"}, a)
	serveOn(t, "b", "kb", []string{"m"}, b)
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
}

// quickRetries makes the waits between retries a millisecond each.
func quickRetries(t *testing.T) {
	t.Helper()
	old := retryDelay
	retryDelay = time.Millisecond
	t.Cleanup(func() { retryDelay = old })
}

const groupChat = `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`

// A candidate that turns the request away with one of these moves it on to
// the next candidate, and the agent gets that one's answer.
func TestGroupMovesOnTurnedAwayRequests(t *testing.T) {
	for _, c := range []struct {
		name string
		code int
		body string
		move bool
	}{
		{"unauthorized", 401, `{"error":{"message":"invalid key"}}`, true},
		{"not found", 404, `{"error":{"message":"no such route"}}`, true},
		{"no model in 400", 400, `{"error":{"message":"模型不存在"}}`, true},
		{"not accessible on this endpoint", 400, `{"error":{"message":"model m is not accessible via the /chat/completions endpoint"}}`, true},
		{"missing field", 400, `{"error":{"message":"messages: field required"}}`, false},
		{"context too long", 400, `{"error":{"message":"maximum context length exceeded"}}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			a := &script{replies: []answer{replyWith(c.code, c.body, nil)}}
			b := &script{replies: []answer{chatText("from b")}}
			chatGroup(t, a, b)
			s := New()
			code, body := postAs(t, s, "", groupChat)
			if c.move {
				if code != 200 || !strings.Contains(body, "from b") || b.n != 1 {
					t.Fatalf("%d %s (b asked %d times)", code, body, b.n)
				}
				return
			}
			if code != c.code || b.n != 0 || a.n != 1 {
				t.Fatalf("%d %s (b asked %d times, a %d)", code, body, b.n, a.n)
			}
		})
	}
}

// An Anthropic stream that fails before any content moves to the next
// candidate; the agent sees only that one's reply, none of the failed one's
// message_start, ping or error.
func TestAnthropicStreamMovesOnBeforeContent(t *testing.T) {
	fresh(t)
	a := &script{replies: []answer{streamAnswer(
		frameText("message_start", `{"type":"message_start","message":{"id":"msg_a","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`),
		frameText("ping", `{"type":"ping"}`),
		frameText("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))}}
	b := &script{replies: []answer{streamAnswer(
		frameText("message_start", `{"type":"message_start","message":{"id":"msg_b","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`),
		frameText("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		frameText("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from b"}}`),
		frameText("content_block_stop", `{"type":"content_block_stop","index":0}`),
		frameText("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`),
		frameText("message_stop", `{"type":"message_stop"}`))}}
	serveAnthropicOn(t, "a", "ka", []string{"m"}, a)
	serveAnthropicOn(t, "b", "kb", []string{"m"}, b)
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"group/mine","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "Overloaded") || strings.Contains(body, "msg_a") {
		t.Fatalf("%d %s", rec.Code, body)
	}
	if a.n != 1 || b.n != 1 {
		t.Fatalf("a asked %d, b asked %d", a.n, b.n)
	}
	rt := s.trace.routes[len(s.trace.routes)-1]
	if len(rt.Tries) != 2 || rt.Tries[0].Status != 529 || rt.Tries[0].Rest == nil || rt.Tries[0].Kind != "moved" {
		t.Fatalf("trace %+v", rt.Tries)
	}
	if _, resting := restOf("a"); !resting {
		t.Fatal("a failed with overloaded and is not resting")
	}
}

// Once content has gone out, an error is the agent's to see, and no other
// candidate is asked.
func TestAnthropicStreamErrorAfterContentIsKept(t *testing.T) {
	fresh(t)
	a := &script{replies: []answer{streamAnswer(
		frameText("message_start", `{"type":"message_start","message":{"id":"msg_a","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`),
		frameText("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		frameText("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`),
		frameText("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))}}
	b := &script{replies: []answer{chatText("from b")}}
	serveAnthropicOn(t, "a", "ka", []string{"m"}, a)
	serveOn(t, "b", "kb", []string{"m"}, b)
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages",
		strings.NewReader(`{"model":"group/mine","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "partial") || !strings.Contains(body, "Overloaded") || b.n != 0 {
		t.Fatalf("%d %s (b asked %d)", rec.Code, body, b.n)
	}
}

// A lone provider's transient failures are tried again, after waits the
// trace shows: 503, then 502, then the answer.
func TestLoneProviderRetriesTransientFailures(t *testing.T) {
	fresh(t)
	quickRetries(t)
	sc := &script{replies: []answer{
		replyWith(503, `{"error":{"message":"busy"}}`, nil),
		replyWith(502, `{"error":{"message":"bad gateway"}}`, nil),
		chatText("from solo"),
	}}
	serveOn(t, "solo", "k", []string{"m"}, sc)
	s := New()
	code, body := postAs(t, s, "", `{"model":"solo/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from solo") || sc.n != 3 {
		t.Fatalf("%d %s (asked %d)", code, body, sc.n)
	}
	rt := s.trace.routes[len(s.trace.routes)-1]
	if len(rt.Tries) != 3 || rt.Tries[0].Kind != "retried" || rt.Tries[0].Wait != 1 ||
		rt.Tries[1].Kind != "retried" || rt.Tries[1].Wait != 2 || rt.Tries[2].Status != 200 {
		t.Fatalf("trace %+v", rt.Tries)
	}
}

// A failure that persists is given up after the retries, as the last
// upstream answer.
func TestLoneProviderGivesUpAfterRetries(t *testing.T) {
	fresh(t)
	quickRetries(t)
	sc := &script{replies: []answer{replyWith(503, `{"error":{"message":"still busy"}}`, nil)}}
	serveOn(t, "solo", "k", []string{"m"}, sc)
	s := New()
	code, body := postAs(t, s, "", `{"model":"solo/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 503 || !strings.Contains(body, "still busy") || sc.n != 3 {
		t.Fatalf("%d %s (asked %d)", code, body, sc.n)
	}
	if rt := s.trace.routes[len(s.trace.routes)-1]; len(rt.Tries) != 3 || rt.Tries[2].Kind != "" {
		t.Fatalf("trace %+v", rt.Tries)
	}
}

// A bad request is the agent's own fault: asked once, answered as it was.
func TestBadRequestIsNotRetried(t *testing.T) {
	fresh(t)
	quickRetries(t)
	sc := &script{replies: []answer{replyWith(400, `{"error":{"message":"messages: field required"}}`, nil)}}
	serveOn(t, "solo", "k", []string{"m"}, sc)
	code, body := postAs(t, New(), "", `{"model":"solo/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || !strings.Contains(body, "field required") || sc.n != 1 {
		t.Fatalf("%d %s (asked %d)", code, body, sc.n)
	}
}

// A vendor asking to wait longer than a retry is worth is not retried.
func TestRetryAfterTooLongIsNotRetried(t *testing.T) {
	fresh(t)
	quickRetries(t)
	sc := &script{replies: []answer{replyWith(429, `{"error":{"message":"slow down"}}`, map[string]string{"Retry-After": "60"})}}
	serveOn(t, "solo", "k", []string{"m"}, sc)
	code, body := postAs(t, New(), "", `{"model":"solo/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 429 || sc.n != 1 {
		t.Fatalf("%d %s (asked %d)", code, body, sc.n)
	}
}

// A 429 that says when to try again is retried after that, and the answer
// that follows is the agent's.
func TestRetryAfterWithinLimitIsRetried(t *testing.T) {
	fresh(t)
	quickRetries(t)
	sc := &script{replies: []answer{
		replyWith(429, `{"error":{"message":"slow down"}}`, map[string]string{"Retry-After": "0"}),
		chatText("from solo"),
	}}
	serveOn(t, "solo", "k", []string{"m"}, sc)
	code, body := postAs(t, New(), "", `{"model":"solo/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from solo") || sc.n != 2 {
		t.Fatalf("%d %s (asked %d)", code, body, sc.n)
	}
}

// The agent going away mid-request: the candidate it was waiting on is not
// blamed, the next is not asked, and the call is recorded as canceled.
func TestAgentCancelLeavesCandidatesAlone(t *testing.T) {
	fresh(t)
	started := make(chan struct{})
	var once sync.Once
	a := &script{replies: []answer{func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}}}
	b := &script{replies: []answer{chatText("from b")}}
	chatGroup(t, a, b)
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(groupChat)).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.Handler().ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("a was never asked")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not end when the agent went away")
	}
	if b.n != 0 {
		t.Fatalf("b was asked %d times after the agent went away", b.n)
	}
	rt := s.trace.routes[len(s.trace.routes)-1]
	if len(rt.Tries) != 1 || rt.Tries[0].Kind != "canceled" || rt.Status != 499 || rt.Error != "canceled" {
		t.Fatalf("trace %+v status %d %s", rt.Tries, rt.Status, rt.Error)
	}
	if c := s.Recent()[0]; c.Status != 499 || c.Error != "canceled" {
		t.Fatalf("recorded %+v", c)
	}
	if _, resting := restOf("a"); resting {
		t.Fatal("a rests after the agent went away")
	}
}

func TestRetryableFailures(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   bool
	}{
		{401, "bad key", true},
		{402, "", true},
		{403, "forbidden", true},
		{404, "not found", true},
		{408, "", true},
		{429, "", true},
		{500, "", true},
		{502, "", true},
		{503, "", true},
		{504, "", true},
		{529, "", true},
		{422, "", false},
		{422, "额度不足", true},
		{400, "messages: field required", false},
		{400, "maximum context length exceeded", false},
		{400, "上下文超长", false},
		{400, "模型不存在", true},
		{400, "model m is not accessible via the /chat/completions endpoint", true},
		{400, "The model `x` does not exist or you do not have access to it", true},
		{400, "You've hit your usage limit", true},
		{200, "", false},
	} {
		if got := retryable(c.status, []byte(c.body)); got != c.want {
			t.Errorf("retryable(%d, %q) = %v", c.status, c.body, got)
		}
	}
}

// The first content, or the first error, decides a stream; a reply that
// ends with neither is passed on whole.
func TestStreamFailureBeforeContent(t *testing.T) {
	for _, c := range []struct {
		name  string
		proto provider.Protocol
		in    string
		want  int // 0: no failure before content
	}{
		{"chat error chunk", provider.Chat,
			sse(`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
				`data: {"error":{"message":"Model overloaded","type":"server_error","code":503}}`), 503},
		{"responses failed", provider.Responses,
			sse(frameText("response.created", `{"type":"response.created","response":{"id":"r"}}`),
				frameText("response.failed", `{"type":"response.failed","response":{"error":{"code":"server_error","message":"boom"}}}`)), 500},
		{"responses error event", provider.Responses,
			sse(frameText("error", `{"type":"error","code":"rate_limit_exceeded","message":"slow down"}`)), 429},
		{"anthropic overloaded after pings", provider.Anthropic,
			sse(frameText("message_start", `{"type":"message_start","message":{"id":"m"}}`),
				frameText("ping", `{"type":"ping"}`),
				frameText("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)), 529},
		{"anthropic invalid request", provider.Anthropic,
			sse(frameText("error", `{"type":"error","error":{"type":"invalid_request_error","message":"messages: field required"}}`)), 400},
		{"chat content, then an error", provider.Chat,
			sse(`data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
				`data: {"error":{"message":"late"}}`), 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			rd, bad := openStream(strings.NewReader(c.in), c.proto)
			switch {
			case c.want == 0 && bad != nil:
				t.Fatalf("failed: %+v", bad)
			case c.want != 0 && (bad == nil || bad.status != c.want):
				t.Fatalf("got %+v, want status %d", bad, c.want)
			case c.want == 0:
				if b, _ := io.ReadAll(rd); string(b) != c.in {
					t.Fatalf("reply altered: %q", b)
				}
			}
		})
	}
}

// A stream with nothing failing and nothing said is passed on whole.
func TestOpenStreamKeepsWholeReply(t *testing.T) {
	in := sse(frameText("message_start", `{"type":"message_start"}`), frameText("ping", `{"type":"ping"}`))
	rd, bad := openStream(strings.NewReader(in), provider.Anthropic)
	if bad != nil {
		t.Fatalf("failed: %+v", bad)
	}
	if b, _ := io.ReadAll(rd); string(b) != in {
		t.Fatalf("reply altered: %q", b)
	}
}

// A sole provider's 500 is tried again: it is a failure that may pass on its
// own, and there is no other candidate to take the request.
func TestSoleProvider500IsRetried(t *testing.T) {
	fresh(t)
	quickRetries(t)
	a := &script{replies: []answer{
		replyWith(500, `{"error":{"message":"boom"}}`, nil),
		replyWith(500, `{"error":{"message":"boom"}}`, nil),
		chatText("from a"),
	}}
	serveOn(t, "a", "ka", []string{"m"}, a)
	code, body := post(t, "/v1/chat/completions", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from a") || a.n != 3 {
		t.Fatalf("status %d, %d requests: %s", code, a.n, body)
	}
}
