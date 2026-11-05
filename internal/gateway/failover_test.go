package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// quickRetries makes the waits before a retry short for the test.
func quickRetries(t *testing.T) {
	t.Helper()
	old := retryDelays
	retryDelays = [maxRetries]time.Duration{time.Millisecond, 2 * time.Millisecond}
	t.Cleanup(func() { retryDelays = old })
}

// scriptReply is one answer of a scripted upstream.
type scriptReply struct {
	code   int    // 0 is 200
	retry  string // Retry-After, when set
	stream bool   // an event stream, else JSON
	body   string
}

// scripted answers each request with the next reply; the last one repeats.
type scripted struct {
	replies []scriptReply
	calls   int
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	rp := s.replies[min(s.calls, len(s.replies)-1)]
	s.calls++
	if rp.stream {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	if rp.retry != "" {
		w.Header().Set("Retry-After", rp.retry)
	}
	if rp.code != 0 {
		w.WriteHeader(rp.code)
	}
	io.WriteString(w, rp.body)
}

// addProvider starts an upstream for h and saves provider id, speaking proto
// only, with the model m.
func addProvider(t *testing.T, id string, proto provider.Protocol, h http.Handler) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k", Models: []string{"m"}}
	switch proto {
	case provider.Chat:
		p.Chat = up.URL + "/v1"
	case provider.Responses:
		p.Responses = up.URL + "/v1"
	case provider.Anthropic:
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// saveGroup makes the group the tests send to: the members in order.
func saveGroup(t *testing.T, members ...string) {
	t.Helper()
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: members, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
}

const (
	chatOK     = `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"the answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`
	chatGroup  = `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`
	chatSingle = `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`
)

func TestGroupMovesOnWhenTheModelIsRefused(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []scriptReply{{code: 400, body: `{"error":{"message":"model m is not accessible via the /chat/completions endpoint"}}`}}}
	b := &scripted{replies: []scriptReply{{body: chatOK}}}
	addProvider(t, "a", provider.Chat, a)
	addProvider(t, "b", provider.Chat, b)
	saveGroup(t, "a/m", "b/m")
	s := New()
	code, out := serveAt(s.Handler(), "/v1/chat/completions", chatGroup)
	if code != 200 || !strings.Contains(out, "the answer") || strings.Contains(out, "accessible") {
		t.Fatalf("%d %s", code, out)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("a asked %d times, b %d", a.calls, b.calls)
	}
	if c := s.Recent()[0]; c.Fallback == "" {
		t.Fatalf("the move is not recorded: %+v", c)
	}
}

// Each of these answers says another member may serve the request.
func TestGroupMovesOnForOtherRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"unauthorized", 401, `{"error":{"message":"invalid api key"}}`},
		{"not found", 404, `{"error":{"message":"no such route"}}`},
		{"model not found, in Chinese", 400, `{"error":{"message":"模型不存在"}}`},
		{"model does not exist", 400, `{"error":{"message":"The model gpt-x does not exist or you do not have access to it."}}`},
		{"out of quota as 422", 422, `{"error":{"message":"您的套餐额度已用完"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []scriptReply{{code: tc.code, body: tc.body}}}
			b := &scripted{replies: []scriptReply{{body: chatOK}}}
			addProvider(t, "a", provider.Chat, a)
			addProvider(t, "b", provider.Chat, b)
			saveGroup(t, "a/m", "b/m")
			code, out := serveAt(New().Handler(), "/v1/chat/completions", chatGroup)
			if code != 200 || !strings.Contains(out, "the answer") || b.calls != 1 {
				t.Fatalf("%d %s, b asked %d times", code, out, b.calls)
			}
		})
	}
}

// Each of these is the request's own fault: the agent gets the error, and no
// other member is asked.
func TestGroupKeepsTheRequestsOwnErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"field required", `{"error":{"message":"messages: field required"}}`},
		{"context too long", `{"error":{"message":"maximum context length is 8192 tokens, exceeded"}}`},
		{"context too long, in Chinese", `{"error":{"message":"上下文超长"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []scriptReply{{code: 400, body: tc.body}}}
			b := &scripted{replies: []scriptReply{{body: chatOK}}}
			addProvider(t, "a", provider.Chat, a)
			addProvider(t, "b", provider.Chat, b)
			saveGroup(t, "a/m", "b/m")
			code, out := serveAt(New().Handler(), "/v1/chat/completions", chatGroup)
			if code != 400 || b.calls != 0 || a.calls != 1 {
				t.Fatalf("%d %s, a asked %d, b asked %d", code, out, a.calls, b.calls)
			}
		})
	}
}

// retryableCases pins which failures another candidate may answer.
func TestRetryableStatuses(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{401, "", true}, {402, "", true}, {403, "", true}, {404, "", true}, {408, "", true},
		{429, "", true}, {500, "", true}, {503, "", true}, {529, "", true},
		{400, "messages: field required", false},
		{400, "maximum context length exceeded", false},
		{400, "上下文超长", false},
		{400, "模型不存在", true},
		{400, "model m is not accessible via the /chat/completions endpoint", true},
		{400, "Rate limit reached", true},
		{422, "您的套餐额度已用完", true},
		{200, "", false},
	} {
		if got := retryable(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("retryable(%d, %q) = %v", tc.status, tc.body, got)
		}
	}
}

const (
	anthropicStart = `event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_A","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`
	anthropicPing  = `event: ping` + "\n" + `data: {"type":"ping"}`
	anthropicOver  = `event: error` + "\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	anthropicText  = `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`
	anthropicB     = `event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_B","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`
	anthropicBText = `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from b"}}`
	anthropicBEnd  = `event: message_stop` + "\n" + `data: {"type":"message_stop"}`
	anthropicGroup = `{"model":"group/mine","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
)

func TestAnthropicStreamErrorBeforeContentMovesOn(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []scriptReply{{stream: true, body: sse(anthropicStart, anthropicPing, anthropicOver)}}}
	b := &scripted{replies: []scriptReply{{stream: true, body: sse(anthropicB, anthropicBText, anthropicBEnd)}}}
	addProvider(t, "a", provider.Anthropic, a)
	addProvider(t, "b", provider.Anthropic, b)
	saveGroup(t, "a/m", "b/m")
	s := New()
	code, out := serveAt(s.Handler(), "/v1/messages", anthropicGroup)
	if code != 200 || !strings.Contains(out, "from b") {
		t.Fatalf("%d %s", code, out)
	}
	if strings.Contains(out, "Overloaded") || strings.Contains(out, "msg_A") {
		t.Fatalf("the dropped candidate's reply reached the agent: %s", out)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("a asked %d times, b %d", a.calls, b.calls)
	}
	tr := s.trace.routes[len(s.trace.routes)-1]
	if len(tr.Tries) != 2 || tr.Tries[0].Status != 529 || tr.Tries[0].Rest == nil {
		t.Fatalf("tries: %+v", tr.Tries)
	}
	if !s.resting("a") {
		t.Fatal("a is not resting after its overload")
	}
}

func TestAnthropicStreamErrorAfterContentIsPassedOn(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []scriptReply{{stream: true, body: sse(anthropicStart, anthropicText, anthropicOver)}}}
	b := &scripted{replies: []scriptReply{{stream: true, body: sse(anthropicB, anthropicBText, anthropicBEnd)}}}
	addProvider(t, "a", provider.Anthropic, a)
	addProvider(t, "b", provider.Anthropic, b)
	saveGroup(t, "a/m", "b/m")
	code, out := serveAt(New().Handler(), "/v1/messages", anthropicGroup)
	if code != 200 || !strings.Contains(out, "partial") || !strings.Contains(out, "Overloaded") || b.calls != 0 {
		t.Fatalf("%d %s, b asked %d times", code, out, b.calls)
	}
}

func TestChatStreamErrorChunkMovesOn(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []scriptReply{{stream: true, body: sse(`data: {"error":{"message":"slow down","type":"rate_limit_error"}}`)}}}
	b := &scripted{replies: []scriptReply{{stream: true, body: sse(
		`data: {"id":"c","choices":[{"index":0,"delta":{"role":"assistant","content":"from b"}}]}`, `data: [DONE]`)}}}
	addProvider(t, "a", provider.Chat, a)
	addProvider(t, "b", provider.Chat, b)
	saveGroup(t, "a/m", "b/m")
	code, out := serveAt(New().Handler(), "/v1/chat/completions", `{"model":"group/mine","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(out, "from b") || strings.Contains(out, "slow down") {
		t.Fatalf("%d %s", code, out)
	}
}

func TestResponsesStreamFailedBeforeContentMovesOn(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []scriptReply{{stream: true, body: sse(
		`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1"}}`,
		`event: response.failed`+"\n"+`data: {"type":"response.failed","response":{"id":"r1","error":{"code":"server_error","message":"internal failure"}}}`)}}}
	b := &scripted{replies: []scriptReply{{stream: true, body: sse(
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"from b"}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r2","status":"completed"}}`)}}}
	addProvider(t, "a", provider.Responses, a)
	addProvider(t, "b", provider.Responses, b)
	saveGroup(t, "a/m", "b/m")
	code, out := serveAt(New().Handler(), "/v1/responses", `{"model":"group/mine","stream":true,"input":"hi"}`)
	if code != 200 || !strings.Contains(out, "from b") || strings.Contains(out, "internal failure") {
		t.Fatalf("%d %s", code, out)
	}
}

// The gate lets go of a stream once it has passed its size limit, and what it
// has held is passed on first.
func TestStreamHoldGivesUpAtItsSizeLimit(t *testing.T) {
	defer func(d time.Duration, n int) { streamHoldFor, streamHoldSize = d, n }(streamHoldFor, streamHoldSize)
	streamHoldSize = 1
	pr, pw := io.Pipe()
	go func() {
		io.WriteString(pw, anthropicStart+"\n\n")
		io.WriteString(pw, anthropicOver+"\n\n")
		pw.Close()
	}()
	out, stop, fail := holdStream(pr, provider.Anthropic)
	defer stop()
	if fail != nil {
		t.Fatalf("held past its size limit: %+v", fail)
	}
	b, _ := io.ReadAll(out)
	if !bytes.Contains(b, []byte("msg_A")) || !bytes.Contains(b, []byte("overloaded_error")) {
		t.Fatalf("passed on %q", b)
	}
}

// Nor does it wait past its time limit for the first content.
func TestStreamHoldGivesUpAtItsTimeLimit(t *testing.T) {
	defer func(d time.Duration, n int) { streamHoldFor, streamHoldSize = d, n }(streamHoldFor, streamHoldSize)
	streamHoldFor = time.Millisecond
	pr, pw := io.Pipe()
	out, stop, fail := holdStream(pr, provider.Anthropic)
	defer stop()
	if fail != nil {
		t.Fatalf("failed with nothing read: %+v", fail)
	}
	go func() {
		io.WriteString(pw, anthropicOver+"\n\n")
		pw.Close()
	}()
	b, _ := io.ReadAll(out)
	if !bytes.Contains(b, []byte("overloaded_error")) {
		t.Fatalf("passed on %q", b)
	}
}

func TestRetryTheLastCandidate(t *testing.T) {
	fresh(t)
	quickRetries(t)
	a := &scripted{replies: []scriptReply{
		{code: 503, body: `{"error":{"message":"busy"}}`},
		{code: 502, body: `{"error":{"message":"bad gateway"}}`},
		{body: chatOK},
	}}
	addProvider(t, "a", provider.Chat, a)
	s := New()
	code, out := serveAt(s.Handler(), "/v1/chat/completions", chatSingle)
	if code != 200 || !strings.Contains(out, "the answer") || a.calls != 3 {
		t.Fatalf("%d %s, asked %d times", code, out, a.calls)
	}
	tr := s.trace.routes[len(s.trace.routes)-1]
	if len(tr.Tries) != 3 || !tr.Tries[0].Retry || tr.Tries[0].Wait != 1 || !tr.Tries[1].Retry || tr.Tries[1].Wait != 2 || tr.Tries[2].Retry {
		t.Fatalf("tries: %+v", tr.Tries)
	}
}

func TestRetryGivesUpWithTheLastError(t *testing.T) {
	fresh(t)
	quickRetries(t)
	a := &scripted{replies: []scriptReply{{code: 503, body: `{"error":{"message":"spare down"}}`}}}
	addProvider(t, "a", provider.Chat, a)
	code, out := serveAt(New().Handler(), "/v1/chat/completions", chatSingle)
	if code != 503 || !strings.Contains(out, "spare down") || a.calls != 3 {
		t.Fatalf("%d %s, asked %d times", code, out, a.calls)
	}
}

func TestPlainBadRequestIsNotRetried(t *testing.T) {
	fresh(t)
	quickRetries(t)
	a := &scripted{replies: []scriptReply{{code: 400, body: `{"error":{"message":"messages: field required"}}`}}}
	addProvider(t, "a", provider.Chat, a)
	code, out := serveAt(New().Handler(), "/v1/chat/completions", chatSingle)
	if code != 400 || !strings.Contains(out, "field required") || a.calls != 1 {
		t.Fatalf("%d %s, asked %d times", code, out, a.calls)
	}
}

// Retry-After decides: a short one is waited out, a long one is not, and a
// 429 without one is not retried at all.
func TestRetryAfterDecidesWhetherToRetry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replies []scriptReply
		code    int
		calls   int
	}{
		{"long Retry-After", []scriptReply{{code: 503, retry: "9", body: `{"error":{"message":"busy"}}`}, {body: chatOK}}, 503, 1},
		{"429 without Retry-After", []scriptReply{{code: 429, body: `{"error":{"message":"slow"}}`}, {body: chatOK}}, 429, 1},
		{"429 with Retry-After 0", []scriptReply{{code: 429, retry: "0", body: `{"error":{"message":"slow"}}`}, {body: chatOK}}, 200, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			quickRetries(t)
			a := &scripted{replies: tc.replies}
			addProvider(t, "a", provider.Chat, a)
			code, out := serveAt(New().Handler(), "/v1/chat/completions", chatSingle)
			if code != tc.code || a.calls != tc.calls {
				t.Fatalf("%d %s, asked %d times", code, out, a.calls)
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	quickRetries(t)
	header := func(v string) http.Header {
		h := http.Header{}
		if v != "" {
			h.Set("Retry-After", v)
		}
		return h
	}
	for _, tc := range []struct {
		status  int
		retry   string
		attempt int
		want    time.Duration
		ok      bool
	}{
		{503, "", 0, time.Millisecond, true},
		{503, "", 1, 2 * time.Millisecond, true},
		{503, "", 2, 0, false},
		{500, "", 0, time.Millisecond, true},
		{529, "", 0, time.Millisecond, true},
		{408, "", 0, time.Millisecond, true},
		{502, "", 0, time.Millisecond, true},
		{429, "", 0, 0, false},
		{429, "3", 0, 3 * time.Second, true},
		{503, "9", 0, 0, false},
		{503, "8", 0, 8 * time.Second, true},
		{400, "", 0, 0, false},
		{501, "", 0, 0, false},
	} {
		got, ok := retryDelay(tc.status, header(tc.retry), tc.attempt)
		if ok != tc.ok || got != tc.want {
			t.Errorf("retryDelay(%d, %q, %d) = %v, %v", tc.status, tc.retry, tc.attempt, got, ok)
		}
	}
}

// The agent going away is no failure of any candidate: it is recorded as
// canceled, nothing rests, and the next candidate is not asked.
func TestCancelDoesNotMoveOrRest(t *testing.T) {
	fresh(t)
	started := make(chan struct{}, 1)
	a := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
			w.WriteHeader(503)
		}
	})
	b := &scripted{replies: []scriptReply{{body: chatOK}}}
	addProvider(t, "a", provider.Chat, a)
	addProvider(t, "b", provider.Chat, b)
	saveGroup(t, "a/m", "b/m")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-started
		cancel()
	}()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatGroup)).WithContext(ctx)
	s := New()
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)

	c := s.Recent()[0]
	if c.Status != 499 || c.Error != "canceled" || c.Fallback != "" {
		t.Fatalf("recorded %+v", c)
	}
	if b.calls != 0 {
		t.Fatalf("the next candidate was asked %d times", b.calls)
	}
	if s.resting("a") {
		t.Fatal("a rests after the agent went away")
	}
	tr := s.trace.routes[len(s.trace.routes)-1]
	if len(tr.Tries) != 1 || !tr.Tries[0].Canceled {
		t.Fatalf("tries: %+v", tr.Tries)
	}
}
