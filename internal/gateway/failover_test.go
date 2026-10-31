package gateway

import (
	"context"
	"fmt"
	"runtime/debug"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// step is one scripted upstream answer.
type step struct {
	code   int
	header map[string]string
	ctype  string
	body   string
	hang   bool // wait for the caller to go away
}

// script answers each request with its next step, the last one for good.
type script struct {
	mu    sync.Mutex
	steps []step
	calls int
	began chan struct{}
}

func (s *script) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	i := min(s.calls, len(s.steps)-1)
	s.calls++
	st := s.steps[i]
	s.mu.Unlock()
	if s.began != nil {
		select {
		case s.began <- struct{}{}:
		default:
		}
	}
	if st.hang {
		io.Copy(io.Discard, r.Body) // so a closed connection is noticed
		<-r.Context().Done()
		return
	}
	ct := st.ctype
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	for k, v := range st.header {
		w.Header().Set(k, v)
	}
	if st.code != 0 {
		w.WriteHeader(st.code)
	}
	w.Write([]byte(st.body))
}

func (s *script) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// pair makes "plan" (m1) fall back to "spare" (m2), both speaking proto.
func pair(t *testing.T, proto provider.Protocol, a, b http.Handler) {
	t.Helper()
	fresh(t)
	for _, x := range []struct {
		p provider.Provider
		h http.Handler
	}{
		{provider.Provider{ID: "plan", Name: "Plan", Key: "k", Models: []string{"m1"}, Fallback: []string{"spare/m2"}}, a},
		{provider.Provider{ID: "spare", Name: "Spare", Key: "k", Models: []string{"m2"}}, b},
	} {
		up := httptest.NewServer(x.h)
		t.Cleanup(up.Close)
		switch proto {
		case provider.Chat:
			x.p.Chat = up.URL + "/v1"
		case provider.Responses:
			x.p.Responses = up.URL + "/v1"
		case provider.Anthropic:
			x.p.Anthropic = up.URL
		}
		if err := provider.Save(x.p); err != nil {
			t.Fatal(err)
		}
	}
}

// solo is one provider on its own.
func solo(t *testing.T, h http.Handler) {
	t.Helper()
	fresh(t)
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

func fastRetries(t *testing.T) {
	t.Helper()
	old := retryDelays
	retryDelays = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}
	t.Cleanup(func() { retryDelays = old })
}

func send(s *Server, ctx context.Context, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body)).WithContext(ctx)
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func lastRoute(s *Server) Route {
	st := s.Trace(context.Background(), 0, 0)
	return st.Routes[len(st.Routes)-1]
}

const okChat = `{"id":"from-spare","choices":[{"index":0,"message":{"role":"assistant","content":"B says hi"},"finish_reason":"stop"}]}`

func TestFailoverToNextCandidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		msg  string
		move bool
	}{
		{"not accessible", 400, "model gpt-9 is not accessible via the /chat/completions endpoint", true},
		{"unauthorized", 401, "bad key", true},
		{"not found", 404, "no such route", true},
		{"model missing in Chinese", 400, "模型不存在", true},
		{"model does not exist", 400, "The model `gpt-9` does not exist", true},
		{"forbidden", 403, "forbidden", true},
		{"unprocessable quota", 422, "insufficient balance", true},
		{"field required", 400, "messages: field required", false},
		{"too long", 400, "This model's maximum context length is 128000 tokens. However, your messages resulted in 200000 tokens; context_length_exceeded", false},
		{"too long in Chinese", 400, "上下文超长", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &script{steps: []step{{code: tc.code, body: `{"error":{"message":"` + tc.msg + `"}}`}}}
			b := &script{steps: []step{{body: okChat}}}
			pair(t, provider.Chat, a, b)
			code, body := post(t, "/v1/chat/completions", chatReq)
			if tc.move {
				if code != 200 || !strings.Contains(body, "B says hi") || strings.Contains(body, tc.msg) || b.count() != 1 {
					t.Fatalf("%d %s (b=%d)", code, body, b.count())
				}
			} else if code != tc.code || !strings.Contains(body, tc.msg) || b.count() != 0 {
				t.Fatalf("%d %s (b=%d)", code, body, b.count())
			}
		})
	}
}

const anthropicReq = `{"model":"plan/m1","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`

func anthropicStream(events ...string) string { return sse(events...) }

func TestAnthropicStreamErrorBeforeContentMovesOn(t *testing.T) {
	a := &script{steps: []step{{ctype: "text/event-stream", body: anthropicStream(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_A","type":"message","role":"assistant","content":[],"model":"m1","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: ping`+"\n"+`data: {"type":"ping"}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)}}}
	b := &script{steps: []step{{ctype: "text/event-stream", body: anthropicStream(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_B","type":"message","role":"assistant","content":[],"model":"m2","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello from B"}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}}}
	pair(t, provider.Anthropic, a, b)
	s := New()
	code, body := send(s, context.Background(), "/v1/messages", anthropicReq)
	if code != 200 || !strings.Contains(body, "hello from B") || strings.Contains(body, "Overloaded") || strings.Contains(body, "msg_A") {
		t.Fatalf("%d %s", code, body)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Status != 529 || r.Tries[0].Rest == nil || r.Tries[0].Kind != TryFailover || r.Tries[1].Status != 200 {
		t.Fatalf("tries %+v", r.Tries)
	}
	if !s.resting("plan") {
		t.Fatal("plan is not resting")
	}
}

func TestAnthropicStreamErrorAfterContentIsDelivered(t *testing.T) {
	a := &script{steps: []step{{ctype: "text/event-stream", body: anthropicStream(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_A","type":"message","role":"assistant","content":[],"model":"m1","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)}}}
	b := &script{steps: []step{{body: okChat}}}
	pair(t, provider.Anthropic, a, b)
	code, body := post(t, "/v1/messages", anthropicReq)
	if code != 200 || !strings.Contains(body, "partial") || !strings.Contains(body, "Overloaded") || b.count() != 0 {
		t.Fatalf("%d %s (b=%d)", code, body, b.count())
	}
}

func TestChatStreamErrorBeforeContentMovesOn(t *testing.T) {
	a := &script{steps: []step{{ctype: "text/event-stream", body: sse(
		`data: {"id":"chatcmpl-A","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`data: {"error":{"message":"server is overloaded","type":"overloaded_error"}}`)}}}
	b := &script{steps: []step{{ctype: "text/event-stream", body: sse(
		`data: {"id":"chatcmpl-B","choices":[{"index":0,"delta":{"role":"assistant","content":"hello from B"}}]}`,
		`data: {"id":"chatcmpl-B","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}}}
	pair(t, provider.Chat, a, b)
	req := `{"model":"plan/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	code, body := post(t, "/v1/chat/completions", req)
	if code != 200 || !strings.Contains(body, "hello from B") || strings.Contains(body, "overloaded") || strings.Contains(body, "chatcmpl-A") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestResponsesFailedBeforeContentMovesOn(t *testing.T) {
	a := &script{steps: []step{{ctype: "text/event-stream", body: sse(
		"event: response.created\n"+`data: {"type":"response.created","response":{"id":"resp_A","status":"in_progress"}}`,
		"event: response.failed\n"+`data: {"type":"response.failed","response":{"id":"resp_A","status":"failed","error":{"code":"server_error","message":"A broke"}}}`)}}}
	b := &script{steps: []step{{ctype: "text/event-stream", body: sse(
		"event: response.created\n"+`data: {"type":"response.created","response":{"id":"resp_B","status":"in_progress"}}`,
		"event: response.output_text.delta\n"+`data: {"type":"response.output_text.delta","delta":"hello from B"}`,
		"event: response.completed\n"+`data: {"type":"response.completed","response":{"id":"resp_B","status":"completed"}}`)}}}
	pair(t, provider.Responses, a, b)
	req := `{"model":"plan/m1","stream":true,"input":"hi"}`
	code, body := post(t, "/v1/responses", req)
	if code != 200 || !strings.Contains(body, "hello from B") || strings.Contains(body, "A broke") || strings.Contains(body, "resp_A") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestStreamHeldOnlyBriefly(t *testing.T) {
	old, oldSize := holdTime, holdSize
	holdSize = 200
	t.Cleanup(func() { holdTime, holdSize = old, oldSize })
	big := strings.Repeat("x", 300)
	a := &script{steps: []step{{ctype: "text/event-stream", body: sse(
		`data: {"id":"chatcmpl-A","choices":[{"index":0,"delta":{"role":"assistant"}}],"pad":"`+big+`"}`,
		`data: {"error":{"message":"late failure","type":"overloaded_error"}}`)}}}
	b := &script{steps: []step{{body: okChat}}}
	pair(t, provider.Chat, a, b)
	req := `{"model":"plan/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	_, body := post(t, "/v1/chat/completions", req)
	if !strings.Contains(body, "late failure") || b.count() != 0 {
		t.Fatalf("past the limit the stream passes: %s (b=%d)", body, b.count())
	}
}

func TestSoleProviderRetriesTransientFailures(t *testing.T) {
	fastRetries(t)
	a := &script{steps: []step{{code: 503, body: `{"error":{"message":"down"}}`}, {code: 502, body: `{"error":{"message":"bad gateway"}}`}, {body: okChat}}}
	solo(t, a)
	s := New()
	code, body := send(s, context.Background(), "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, "B says hi") || a.count() != 3 {
		t.Fatalf("%d %s (calls=%d)", code, body, a.count())
	}
	r := lastRoute(s)
	if len(r.Tries) != 3 || r.Tries[0].Kind != TryRetry || r.Tries[0].WaitMs != 5 || r.Tries[1].Kind != TryRetry || r.Tries[1].WaitMs != 10 || r.Tries[2].Status != 200 {
		t.Fatalf("tries %+v", r.Tries)
	}
	if s.resting("plan") {
		t.Fatal("plan rests after answering")
	}
}

func TestSoleProviderGivesUpAfterTwoRetries(t *testing.T) {
	fastRetries(t)
	a := &script{steps: []step{{code: 503, body: `{"error":{"message":"down"}}`}}}
	solo(t, a)
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 503 || !strings.Contains(body, "down") || a.count() != 3 {
		t.Fatalf("%d %s (calls=%d)", code, body, a.count())
	}
}

func TestNoRetryFor(t *testing.T) {
	fastRetries(t)
	for _, tc := range []struct {
		name string
		st   step
	}{
		{"parameter error", step{code: 400, body: `{"error":{"message":"messages: field required"}}`}},
		{"429 without Retry-After", step{code: 429, body: `{"error":{"message":"slow"}}`}},
		{"429 waiting too long", step{code: 429, header: map[string]string{"Retry-After": "20"}, body: `{"error":{"message":"slow"}}`}},
		{"503 waiting too long", step{code: 503, header: map[string]string{"Retry-After": "9"}, body: `{"error":{"message":"down"}}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &script{steps: []step{tc.st}}
			solo(t, a)
			code, _ := post(t, "/v1/chat/completions", chatReq)
			if code != tc.st.code || a.count() != 1 {
				t.Fatalf("%d (calls=%d)", code, a.count())
			}
		})
	}
}

func TestRetryAfterIsTheWait(t *testing.T) {
	fastRetries(t)
	a := &script{steps: []step{{code: 429, header: map[string]string{"Retry-After": "0"}, body: `{"error":{"message":"slow"}}`}, {body: okChat}}}
	solo(t, a)
	s := New()
	code, _ := send(s, context.Background(), "/v1/chat/completions", chatReq)
	if code != 200 || a.count() != 2 || lastRoute(s).Tries[0].Kind != TryRetry {
		t.Fatalf("%d (calls=%d)", code, a.count())
	}
}

func TestRetryWait(t *testing.T) {
	h := func(v string) http.Header {
		if v == "" {
			return http.Header{}
		}
		return http.Header{"Retry-After": {v}}
	}
	for _, tc := range []struct {
		status int
		ra     string
		n      int
		want   time.Duration
		ok     bool
	}{
		{503, "", 0, time.Second, true},
		{503, "", 1, 2 * time.Second, true},
		{503, "", 2, 0, false},
		{429, "", 0, 0, false},
		{429, "3", 0, 3 * time.Second, true},
		{529, "8", 0, 8 * time.Second, true},
		{529, "9", 0, 0, false},
		{408, "", 0, time.Second, true},
		{501, "", 0, 0, false},
		{400, "", 0, 0, false},
	} {
		if d, ok := retryWait(tc.status, h(tc.ra), tc.n); d != tc.want || ok != tc.ok {
			t.Errorf("%d ra=%q n=%d: %v %v", tc.status, tc.ra, tc.n, d, ok)
		}
	}
}

func TestCancelStopsEverything(t *testing.T) {
	a := &script{steps: []step{{hang: true}}, began: make(chan struct{}, 1)}
	b := &script{steps: []step{{body: okChat}}}
	pair(t, provider.Chat, a, b)
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	crash := make(chan string, 1)
	go func() {
		defer func() {
			if e := recover(); e != nil {
				crash <- fmt.Sprint(e, " ", strings.ReplaceAll(string(debug.Stack()), "\n", " | "))
				done <- 0
			}
		}()
		code, _ := send(s, ctx, "/v1/chat/completions", chatReq)
		done <- code
	}()
	select {
	case <-a.began:
	case <-time.After(5 * time.Second):
		t.Fatal("A was never asked")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not end")
	}
	select {
	case c := <-crash:
		t.Fatalf("PANIC %s", c)
	default:
	}
	if b.count() != 0 {
		t.Fatal("B was asked after the agent left")
	}
	r := lastRoute(s)
	if len(r.Tries) != 1 || r.Tries[0].Fail != failCanceled || r.Tries[0].Kind != TryCanceled || r.Tries[0].Rest != nil || r.Status != 499 {
		t.Fatalf("route %+v tries %+v", r, r.Tries)
	}
	if s.resting("plan") {
		t.Fatal("plan rests after the agent canceled")
	}
	if c := s.Recent()[0]; c.Status != 499 {
		t.Fatalf("recorded %+v", c)
	}
}

func TestSuccessWakesRestingCandidate(t *testing.T) {
	fastRetries(t)
	solo(t, &script{steps: []step{{body: okChat}}})
	restingUntil.Lock()
	restingUntil.m["plan"] = time.Now().Add(time.Hour)
	restingUntil.Unlock()
	if code, _ := post(t, "/v1/chat/completions", chatReq); code != 200 {
		t.Fatal(code)
	}
	if _, ok := restOf("plan"); ok {
		t.Fatal("still resting after an answer")
	}
}
