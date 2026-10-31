package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func init() {
	retryBackoff = []time.Duration{time.Millisecond, 2 * time.Millisecond}
}

// step is one scripted upstream reply.
type step struct {
	code  int
	ctype string
	body  string
	head  map[string]string
	hang  bool // wait for the caller to go away
}

// script replies with its steps in turn, the last one for ever after.
type script struct {
	mu    sync.Mutex
	steps []step
	hits  int32
}

func (s *script) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	n := int(atomic.AddInt32(&s.hits, 1)) - 1
	s.mu.Lock()
	st := s.steps[min(n, len(s.steps)-1)]
	s.mu.Unlock()
	if st.hang {
		<-r.Context().Done()
		return
	}
	ct := st.ctype
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	for k, v := range st.head {
		w.Header().Set(k, v)
	}
	if st.code != 0 {
		w.WriteHeader(st.code)
	}
	io.WriteString(w, st.body)
}

func (s *script) count() int { return int(atomic.LoadInt32(&s.hits)) }

const okChat = `{"id":"from-ok","choices":[{"index":0,"message":{"role":"assistant","content":"fine"},"finish_reason":"stop"}]}`

func errBody(msg string) string { return `{"error":{"message":"` + msg + `"}}` }

// chain saves "plan" (models m1) falling back to "spare" (m2), each on the
// given endpoint kind, and returns where each can be reached.
func chain(t *testing.T, proto provider.Protocol, plan, spare http.Handler) {
	t.Helper()
	fresh(t)
	for _, x := range []struct {
		p provider.Provider
		h http.Handler
	}{
		{provider.Provider{ID: "plan", Name: "Plan", Key: "k", Models: []string{"m1"}, Fallback: []string{"spare/m2"}}, plan},
		{provider.Provider{ID: "spare", Name: "Spare", Key: "k", Models: []string{"m2"}}, spare},
	} {
		if x.h == nil {
			continue
		}
		up := httptest.NewServer(x.h)
		t.Cleanup(up.Close)
		if proto == provider.Anthropic {
			x.p.Anthropic = up.URL
		} else {
			x.p.Chat = up.URL + "/v1"
		}
		if err := provider.Save(x.p); err != nil {
			t.Fatal(err)
		}
	}
}

func lastRoute(t *testing.T, s *Server) Route {
	t.Helper()
	st := s.Trace(context.Background(), 0, 0)
	if len(st.Routes) == 0 {
		t.Fatal("no route traced")
	}
	return st.Routes[len(st.Routes)-1]
}

func TestFailoverStatusesAndMessages(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     int
		msg      string
		failover bool
	}{
		{"401", 401, "bad key", true},
		{"404", 404, "nothing here", true},
		{"403", 403, "forbidden", true},
		{"model endpoint 400", 400, "The model `m1` is not accessible via the /chat/completions endpoint", true},
		{"model missing 400", 400, "模型不存在", true},
		{"model missing 422", 422, "The model m1 does not exist", true},
		{"quota 400", 400, "You exceeded your current quota", true},
		{"field required", 400, "messages: field required", false},
		{"context too long", 400, "This model's maximum context length is 8192 tokens, however you requested 9000 tokens (context_length_exceeded)", false},
		{"plain 422", 422, "bad parameter", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &script{steps: []step{{code: tc.code, body: errBody(tc.msg)}}}
			spare := &script{steps: []step{{body: okChat}}}
			chain(t, provider.Chat, plan, spare)
			code, body := post(t, "/v1/chat/completions", chatReq)
			if tc.failover {
				if code != 200 || !strings.Contains(body, "from-ok") || spare.count() != 1 {
					t.Fatalf("%d %s spare=%d", code, body, spare.count())
				}
			} else if code != tc.code || spare.count() != 0 || plan.count() != 1 {
				t.Fatalf("%d %s spare=%d plan=%d", code, body, spare.count(), plan.count())
			}
		})
	}
}

const anthropicReq = `{"model":"plan/m1","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`

const anthropicOK = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_spare\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"from spare\"}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

const anthropicHead = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_plan\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
	"event: ping\ndata: {\"type\":\"ping\"}\n\n"

const anthropicOverloaded = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"

func TestStreamErrorBeforeContentFailsOver(t *testing.T) {
	plan := &script{steps: []step{{ctype: "text/event-stream", body: anthropicHead + anthropicOverloaded}}}
	spare := &script{steps: []step{{ctype: "text/event-stream", body: anthropicOK}}}
	chain(t, provider.Anthropic, plan, spare)
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(anthropicReq)))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "from spare") {
		t.Fatalf("%d %s", rec.Code, body)
	}
	for _, bad := range []string{"Overloaded", "msg_plan", "ping"} {
		if strings.Contains(body, bad) {
			t.Fatalf("agent saw %q: %s", bad, body)
		}
	}
	rt := lastRoute(t, s)
	if len(rt.Tries) != 2 || rt.Tries[0].Status != 529 || rt.Tries[0].Rest == nil || rt.Tries[0].Kind != KindFailover {
		t.Fatalf("tries %+v", rt.Tries)
	}
	if !restingNow("plan") {
		t.Fatal("plan should rest")
	}
}

func restingNow(id string) bool { _, ok := restOf(id); return ok }

func TestStreamErrorAfterContentIsPassedOn(t *testing.T) {
	content := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n"
	plan := &script{steps: []step{{ctype: "text/event-stream", body: anthropicHead + content + anthropicOverloaded}}}
	spare := &script{steps: []step{{ctype: "text/event-stream", body: anthropicOK}}}
	chain(t, provider.Anthropic, plan, spare)
	code, body := post(t, "/v1/messages", anthropicReq)
	if code != 200 || !strings.Contains(body, "partial") || !strings.Contains(body, "Overloaded") || spare.count() != 0 {
		t.Fatalf("%d %s spare=%d", code, body, spare.count())
	}
}

func TestChatStreamErrorChunkFailsOver(t *testing.T) {
	bad := sse(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`data: {"error":{"message":"The server is overloaded","type":"overloaded_error"}}`)
	good := sse(`data: {"id":"c2","choices":[{"index":0,"delta":{"role":"assistant","content":"from spare"}}]}`,
		`data: {"id":"c2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`)
	plan := &script{steps: []step{{ctype: "text/event-stream", body: bad}}}
	spare := &script{steps: []step{{ctype: "text/event-stream", body: good}}}
	chain(t, provider.Chat, plan, spare)
	code, body := post(t, "/v1/chat/completions", `{"model":"plan/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from spare") || strings.Contains(body, "overloaded") || strings.Contains(body, `"c1"`) {
		t.Fatalf("%d %s", code, body)
	}
}

func TestResponsesFailedEventFailsOver(t *testing.T) {
	bad := sse(`data: {"type":"response.created","response":{"id":"r1"}}`,
		`data: {"type":"response.failed","response":{"id":"r1","error":{"code":"server_error","message":"boom"}}}`)
	b, _ := classifyEvent(provider.Responses, "event: response.failed\n"+strings.TrimSpace(strings.Split(bad, "\n\n")[1]))
	if b != evError {
		t.Fatalf("kind %d", b)
	}
	if k, _ := classifyEvent(provider.Responses, strings.Split(bad, "\n\n")[0]); k != evHold {
		t.Fatalf("created is %d", k)
	}
	if k, e := classifyEvent(provider.Responses, `data: {"type":"error","message":"slow","code":"rate_limit_exceeded"}`); k != evError || e.status != 429 {
		t.Fatalf("%d %+v", k, e)
	}
	if k, _ := classifyEvent(provider.Responses, `data: {"type":"response.output_text.delta","delta":"x"}`); k != evContent {
		t.Fatal("delta is content")
	}
}

func TestStreamHoldTimeLimitStartsPassing(t *testing.T) {
	old := streamHoldTime
	streamHoldTime = 20 * time.Millisecond
	defer func() { streamHoldTime = old }()
	rec := httptest.NewRecorder()
	hw := newHoldWriter(rec, true)
	hw.proto = provider.Anthropic
	hw.Header().Set("Content-Type", "text/event-stream")
	hw.WriteHeader(200)
	hw.Write([]byte(anthropicHead))
	if rec.Body.Len() != 0 {
		t.Fatal("head should be held")
	}
	time.Sleep(100 * time.Millisecond)
	hw.mu.Lock()
	passing := hw.passing
	hw.mu.Unlock()
	if !passing || !strings.Contains(rec.Body.String(), "msg_plan") {
		t.Fatalf("not passing: %q", rec.Body.String())
	}
	hw.close()
}

func TestStreamHoldSizeLimitStartsPassing(t *testing.T) {
	old := streamHoldMax
	streamHoldMax = 100
	defer func() { streamHoldMax = old }()
	rec := httptest.NewRecorder()
	hw := newHoldWriter(rec, true)
	hw.proto = provider.Anthropic
	hw.Header().Set("Content-Type", "text/event-stream")
	hw.WriteHeader(200)
	hw.Write([]byte(anthropicHead))
	if rec.Body.Len() == 0 {
		t.Fatal("over the limit should pass")
	}
	hw.close()
}

func TestRetryThenSuccess(t *testing.T) {
	up := &script{steps: []step{{code: 503, body: errBody("busy")}, {code: 502, body: errBody("bad gw")}, {body: okChat}}}
	chain(t, provider.Chat, up, nil)
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-ok") || up.count() != 3 {
		t.Fatalf("%d %s hits=%d", rec.Code, rec.Body.String(), up.count())
	}
	rt := lastRoute(t, s)
	if len(rt.Tries) != 3 || rt.Tries[0].Kind != KindRetry || rt.Tries[0].WaitMs <= 0 || rt.Tries[1].Kind != KindRetry || rt.Tries[1].WaitMs <= 0 || rt.Tries[2].Status != 200 {
		t.Fatalf("tries %+v", rt.Tries)
	}
	if restingNow("plan") {
		t.Fatal("plan should not rest")
	}
}

func TestRetryGivesUpAfterTwo(t *testing.T) {
	up := &script{steps: []step{{code: 503, body: errBody("busy")}}}
	chain(t, provider.Chat, up, nil)
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 503 || up.count() != 3 || !strings.Contains(body, "busy") {
		t.Fatalf("%d %s hits=%d", code, body, up.count())
	}
}

func TestNoRetryCases(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   step
	}{
		{"plain 400", step{code: 400, body: errBody("bad parameter")}},
		{"429 without retry-after", step{code: 429, body: errBody("slow down")}},
		{"429 retry-after too long", step{code: 429, body: errBody("slow down"), head: map[string]string{"Retry-After": "9"}}},
		{"503 retry-after too long", step{code: 503, body: errBody("busy"), head: map[string]string{"Retry-After": "30"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &script{steps: []step{tc.st}}
			chain(t, provider.Chat, up, nil)
			code, _ := post(t, "/v1/chat/completions", chatReq)
			if code != tc.st.code || up.count() != 1 {
				t.Fatalf("%d hits=%d", code, up.count())
			}
		})
	}
}

func TestRetryAfterIsHonored(t *testing.T) {
	up := &script{steps: []step{{code: 429, body: errBody("slow"), head: map[string]string{"Retry-After": "1"}}, {body: okChat}}}
	chain(t, provider.Chat, up, nil)
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
	rt := lastRoute(t, s)
	if rec.Code != 200 || up.count() != 2 || rt.Tries[0].WaitMs != 1000 {
		t.Fatalf("%d hits=%d %+v", rec.Code, up.count(), rt.Tries)
	}
}

func TestRetryOnTheLastCandidateOfAChain(t *testing.T) {
	plan := &script{steps: []step{{code: 500, body: errBody("down")}}}
	spare := &script{steps: []step{{code: 503, body: errBody("busy")}, {body: okChat}}}
	chain(t, provider.Chat, plan, spare)
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, "from-ok") || plan.count() != 1 || spare.count() != 2 {
		t.Fatalf("%d %s plan=%d spare=%d", code, body, plan.count(), spare.count())
	}
}

func TestCancelWhileFirstHangs(t *testing.T) {
	plan := &script{steps: []step{{hang: true}}}
	spare := &script{steps: []step{{body: okChat}}}
	chain(t, provider.Chat, plan, spare)
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(80 * time.Millisecond); cancel() }()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)).WithContext(ctx)
	s.Handler().ServeHTTP(rec, req)
	if spare.count() != 0 {
		t.Fatal("spare was asked")
	}
	rt := lastRoute(t, s)
	if rt.Status != 499 || len(rt.Tries) != 1 || rt.Tries[0].Fail != "canceled" || rt.Tries[0].Kind != KindCanceled || rt.Tries[0].Rest != nil {
		t.Fatalf("route %+v", rt)
	}
	if restingNow("plan") {
		t.Fatal("plan should not rest")
	}
	if rec.Body.Len() != 0 && rec.Code == 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if calls := s.Recent(); len(calls) == 0 || calls[0].Status != 499 {
		t.Fatalf("calls %+v", calls)
	}
}

func TestCancelDuringRetryWait(t *testing.T) {
	old := retryBackoff
	retryBackoff = []time.Duration{5 * time.Second, 5 * time.Second}
	defer func() { retryBackoff = old }()
	up := &script{steps: []step{{code: 503, body: errBody("busy")}}}
	chain(t, provider.Chat, up, nil)
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(80 * time.Millisecond); cancel() }()
	start := time.Now()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)).WithContext(ctx))
	rt := lastRoute(t, s)
	if time.Since(start) > 2*time.Second || up.count() != 1 || rt.Status != 499 || rt.Tries[0].Fail != "canceled" || restingNow("plan") {
		t.Fatalf("hits=%d %+v", up.count(), rt)
	}
}

func TestSuccessEndsRest(t *testing.T) {
	up := &script{steps: []step{{body: okChat}}}
	chain(t, provider.Chat, up, nil)
	restingUntil.Lock()
	restingUntil.m["plan"] = time.Now().Add(time.Hour)
	restingUntil.Unlock()
	code, _ := post(t, "/v1/chat/completions", chatReq)
	if code != 200 || restingNow("plan") {
		t.Fatalf("%d resting=%v", code, restingNow("plan"))
	}
}

func TestOpenAIFirstSkipsGptOss(t *testing.T) {
	p := provider.Provider{ID: "copilot", Account: &provider.Account{Agent: "copilot"}}
	for model, want := range map[string]bool{"gpt-6-sol": true, "o4-mini": true, "codex-mini": true, "openai/gpt-oss-120b": false, "gpt-oss-20b": false, "claude-sonnet": false} {
		if got := openAIFirst(p, model); got != want {
			t.Errorf("%s: %v", model, got)
		}
	}
}
