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

// scripted answers each request with the next of its replies, the last one
// over and over.
type scripted struct {
	mu      sync.Mutex
	n       int
	paths   []string
	replies []reply
	got     chan struct{} // told of each request, when set
}

type reply struct {
	code   int
	ctype  string
	body   string
	header map[string]string
	hang   bool // wait for the request to be given up on
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	s.mu.Lock()
	rp := s.replies[min(s.n, len(s.replies)-1)]
	s.n++
	s.paths = append(s.paths, r.URL.Path)
	s.mu.Unlock()
	if s.got != nil {
		s.got <- struct{}{}
	}
	if rp.hang {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return
	}
	ct := rp.ctype
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	for k, v := range rp.header {
		w.Header().Set(k, v)
	}
	if rp.code != 0 {
		w.WriteHeader(rp.code)
	}
	io.WriteString(w, rp.body)
}

func (s *scripted) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// member saves a provider speaking proto only, backed by h.
func member(t *testing.T, id string, proto provider.Protocol, h http.Handler) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k-" + id, Models: []string{"m"}}
	switch proto {
	case provider.Anthropic:
		p.Anthropic = up.URL
	case provider.Responses:
		p.Responses = up.URL + "/v1"
	default:
		p.Chat = up.URL + "/v1"
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func freshFailover(t *testing.T) {
	t.Helper()
	fresh(t)
	endpointsOff.Lock()
	endpointsOff.m = map[string]bool{}
	endpointsOff.Unlock()
	routed.Lock()
	routed.failures = map[string]int{}
	routed.Unlock()
	// retries are counted, not waited for
	old := retrySleep
	retrySleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	t.Cleanup(func() { retrySleep = old })
}

func orderedGroup(t *testing.T, members ...string) string {
	t.Helper()
	if err := provider.SaveGroup(provider.Group{Name: "Pair", Members: members, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	return "group/pair"
}

const chatOK = `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from B"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`

func lastRoute(s *Server) Route {
	s.trace.mu.Lock()
	defer s.trace.mu.Unlock()
	r := *s.trace.routes[len(s.trace.routes)-1]
	r.Tries = append([]Try(nil), r.Tries...)
	return r
}

// Acceptance 5: a refusal that another member may not give moves the
// request on; one that is the request's own fault does not.
func TestGroupFailsOverOnRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		move bool
	}{
		{"not on this endpoint", 400, `{"error":{"message":"model \"m\" is not accessible via the /chat/completions endpoint","code":"unsupported_api_for_model"}}`, true},
		{"unauthorized", 401, `{"error":{"message":"invalid api key"}}`, true},
		{"not found", 404, `{"error":{"message":"no such thing"}}`, true},
		{"模型不存在", 400, `{"error":{"message":"模型不存在"}}`, true},
		{"model does not exist", 400, `{"error":{"message":"The model 'm' does not exist or you do not have access to it."}}`, true},
		{"quota as 400", 400, `{"error":{"message":"You exceeded your current quota"}}`, true},
		{"field required", 400, `{"error":{"message":"messages: field required"}}`, false},
		{"上下文超长", 400, `{"error":{"message":"上下文超长"}}`, false},
		{"context length", 400, `{"error":{"message":"This model's maximum context length is 8192 tokens, you exceeded it","code":"context_length_exceeded"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			freshFailover(t)
			a := &scripted{replies: []reply{{code: tc.code, body: tc.body}}}
			b := &scripted{replies: []reply{{body: chatOK}}}
			member(t, "a", provider.Chat, a)
			member(t, "b", provider.Chat, b)
			g := orderedGroup(t, "a/m", "b/m")
			s := New()
			code, body := postTo(s.Handler(), "/v1/chat/completions", `{"model":"`+g+`","messages":[{"role":"user","content":"hi"}]}`)
			if tc.move {
				if code != 200 || !strings.Contains(body, "from B") || b.count() != 1 || a.count() != 1 {
					t.Fatalf("%d %s (a %d, b %d)", code, body, a.count(), b.count())
				}
				if tr := lastRoute(s); len(tr.Tries) != 2 || tr.Tries[0].Then != "fallback" || tr.Tries[0].Rest == nil {
					t.Fatalf("trace %+v", tr.Tries)
				}
				return
			}
			if code != tc.code || b.count() != 0 || a.count() != 1 {
				t.Fatalf("%d %s (a %d, b %d)", code, body, a.count(), b.count())
			}
		})
	}
}

func TestRetryableRules(t *testing.T) {
	for _, tc := range []struct {
		code int
		body string
		want bool
	}{
		{401, "", true}, {402, "", true}, {403, "", true}, {404, "", true}, {408, "", true}, {429, "", true},
		{500, "", true}, {503, "", true}, {529, "", true},
		{400, "bad", false}, {422, "unprocessable", false}, {409, "", false},
		{422, "rate limit reached", true}, {400, "余额不足", true}, {400, "model gpt-x is not supported", true},
		{400, "该模型未开通", true}, {400, "无权访问该模型", true},
		{400, "prompt is too long: 300000 tokens > 200000 maximum", false},
	} {
		if got := retryable(tc.code, []byte(tc.body)); got != tc.want {
			t.Errorf("retryable(%d, %q) = %v", tc.code, tc.body, got)
		}
	}
}

func anthropicSSE(lines ...string) string {
	var out []string
	for _, l := range lines {
		i := strings.Index(l, `"type":"`)
		typ := l[i+8:]
		typ = typ[:strings.Index(typ, `"`)]
		out = append(out, "event: "+typ+"\ndata: "+l)
	}
	return sse(out...)
}

const (
	aStartA = `{"type":"message_start","message":{"id":"msg_from_A","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`
	aStartB = `{"type":"message_start","message":{"id":"msg_from_B","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`
	aPing   = `{"type":"ping"}`
	aBlock  = `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	aStop   = `{"type":"content_block_stop","index":0}`
	aMsgEnd = `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`
	aDone   = `{"type":"message_stop"}`
	aOver   = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
)

func aText(s string) string {
	return `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + s + `"}}`
}

const anthropicStreamReq = `{"model":"group/pair","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`

// Acceptance 6: an Anthropic stream that fails after message_start but
// before any content is dropped for the next member; one that fails after
// content reaches the agent as it is.
func TestAnthropicStreamFailsOverBeforeContent(t *testing.T) {
	freshFailover(t)
	sseType := "text/event-stream"
	a := &scripted{replies: []reply{{ctype: sseType, body: anthropicSSE(aStartA, aPing, aOver)}}}
	b := &scripted{replies: []reply{{ctype: sseType, body: anthropicSSE(aStartB, aBlock, aText("from B"), aStop, aMsgEnd, aDone)}}}
	member(t, "a", provider.Anthropic, a)
	member(t, "b", provider.Anthropic, b)
	orderedGroup(t, "a/m", "b/m")
	s := New()
	code, body := postTo(s.Handler(), "/v1/messages", anthropicStreamReq)
	if code != 200 || !strings.Contains(body, "from B") || strings.Contains(body, "Overloaded") || strings.Contains(body, "msg_from_A") {
		t.Fatalf("%d %s", code, body)
	}
	tr := lastRoute(s)
	if len(tr.Tries) != 2 || tr.Tries[0].Status != 529 || tr.Tries[0].Rest == nil || tr.Tries[0].Then != "fallback" {
		t.Fatalf("tries %+v", tr.Tries)
	}
	if !s.resting("a") {
		t.Fatal("a is not resting")
	}

	// once content went out, the error goes too, and nobody else is asked
	freshFailover(t)
	a = &scripted{replies: []reply{{ctype: sseType, body: anthropicSSE(aStartA, aBlock, aText("partial"), aOver)}}}
	b = &scripted{replies: []reply{{ctype: sseType, body: anthropicSSE(aStartB, aBlock, aText("from B"), aStop, aMsgEnd, aDone)}}}
	member(t, "a", provider.Anthropic, a)
	member(t, "b", provider.Anthropic, b)
	orderedGroup(t, "a/m", "b/m")
	code, body = postTo(New().Handler(), "/v1/messages", anthropicStreamReq)
	if code != 200 || !strings.Contains(body, "partial") || !strings.Contains(body, "Overloaded") || b.count() != 0 {
		t.Fatalf("%d %s (b %d)", code, body, b.count())
	}
}

// A Chat stream's error chunk before content fails over too, relayed or
// translated; a role-only chunk before it is not content.
func TestChatStreamErrorChunkFailsOver(t *testing.T) {
	for _, from := range []string{"chat", "anthropic"} {
		t.Run(from, func(t *testing.T) {
			freshFailover(t)
			sseType := "text/event-stream"
			a := &scripted{replies: []reply{{ctype: sseType, body: sse(
				`data: {"id":"c-from-A","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
				`data: {"error":{"message":"upstream overloaded, try later","type":"server_error"}}`)}}}
			b := &scripted{replies: []reply{{ctype: sseType, body: sse(
				`data: {"id":"c-from-B","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from B"}}]}`,
				`data: {"id":"c-from-B","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				`data: [DONE]`)}}}
			member(t, "a", provider.Chat, a)
			member(t, "b", provider.Chat, b)
			g := orderedGroup(t, "a/m", "b/m")
			path, req := "/v1/chat/completions", `{"model":"`+g+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`
			if from == "anthropic" {
				path, req = "/v1/messages", `{"model":"`+g+`","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
			}
			code, body := postTo(New().Handler(), path, req)
			if code != 200 || !strings.Contains(body, "from B") || strings.Contains(body, "overloaded") || strings.Contains(body, "c-from-A") {
				t.Fatalf("%d %s", code, body)
			}
		})
	}
}

// A Responses stream's response.failed before any output fails over.
func TestResponsesFailedBeforeContentFailsOver(t *testing.T) {
	freshFailover(t)
	sseType := "text/event-stream"
	a := &scripted{replies: []reply{{ctype: sseType, body: sse(
		`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"resp_from_A","status":"in_progress"}}`,
		`event: response.failed`+"\n"+`data: {"type":"response.failed","response":{"id":"resp_from_A","status":"failed","error":{"code":"server_error","message":"A broke"}}}`)}}}
	b := &scripted{replies: []reply{{ctype: sseType, body: sse(
		`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"resp_from_B"}}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"from B"}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"resp_from_B","usage":{"input_tokens":1,"output_tokens":1}}}`)}}}
	member(t, "a", provider.Responses, a)
	member(t, "b", provider.Responses, b)
	g := orderedGroup(t, "a/m", "b/m")
	code, body := postTo(New().Handler(), "/v1/responses", `{"model":"`+g+`","stream":true,"input":"hi"}`)
	if code != 200 || !strings.Contains(body, "from B") || strings.Contains(body, "resp_from_A") || strings.Contains(body, "A broke") {
		t.Fatalf("%d %s", code, body)
	}
}

// Acceptance 7: the only provider is tried again on a failure that may
// pass, waiting a second, then two; not on the request's own fault.
func TestLastCandidateRetries(t *testing.T) {
	type tc struct {
		name    string
		replies []reply
		code    int
		tries   int
		waits   []int64
	}
	errBody := func(m string) string { return `{"error":{"message":"` + m + `"}}` }
	for _, c := range []tc{
		{"recovers", []reply{{code: 503, body: errBody("down")}, {code: 502, body: errBody("bad gateway")}, {body: chatOK}}, 200, 3, []int64{1000, 2000}},
		{"stays down", []reply{{code: 503, body: errBody("down")}}, 503, 3, []int64{1000, 2000}},
		{"bad request", []reply{{code: 400, body: errBody("messages: field required")}}, 400, 1, nil},
		{"rate limited, no word", []reply{{code: 429, body: errBody("slow down")}}, 429, 1, nil},
		{"rate limited, retry after", []reply{{code: 429, body: errBody("slow down"), header: map[string]string{"Retry-After": "3"}}, {body: chatOK}}, 200, 2, []int64{3000}},
		{"retry after too long", []reply{{code: 503, body: errBody("down"), header: map[string]string{"Retry-After": "30"}}}, 503, 1, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			freshFailover(t)
			var slept []time.Duration
			retrySleep = func(ctx context.Context, d time.Duration) error { slept = append(slept, d); return nil }
			up := &scripted{replies: c.replies}
			member(t, "solo", provider.Chat, up)
			s := New()
			code, body := postTo(s.Handler(), "/v1/chat/completions", `{"model":"solo/m","messages":[{"role":"user","content":"hi"}]}`)
			if code != c.code || up.count() != c.tries {
				t.Fatalf("%d %s, %d tries", code, body, up.count())
			}
			if c.code == 200 && !strings.Contains(body, "from B") {
				t.Fatalf("body %s", body)
			}
			tr := lastRoute(s)
			if len(tr.Tries) != c.tries {
				t.Fatalf("trace tries %+v", tr.Tries)
			}
			for i, w := range c.waits {
				if tr.Tries[i].Then != "retry" || tr.Tries[i].Wait != w || tr.Tries[i+1].Retry != i+1 {
					t.Fatalf("try %d: %+v", i, tr.Tries[i])
				}
				if slept[i] != time.Duration(w)*time.Millisecond {
					t.Fatalf("slept %v", slept)
				}
			}
			if len(slept) != len(c.waits) {
				t.Fatalf("slept %v", slept)
			}
			if rec := s.Recent()[0]; len(rec.Waits) != len(c.waits) || rec.Status != c.code {
				t.Fatalf("recorded %+v", rec)
			}
			if s.resting("solo") {
				t.Fatal("the only provider was rested")
			}
		})
	}
}

// Acceptance 8: an agent that hangs up while the first member works ends
// the request: nobody else is asked, nobody rests.
func TestCancelEndsWithoutBlame(t *testing.T) {
	freshFailover(t)
	a := &scripted{replies: []reply{{hang: true}}, got: make(chan struct{}, 1)}
	b := &scripted{replies: []reply{{body: chatOK}}}
	member(t, "a", provider.Chat, a)
	member(t, "b", provider.Chat, b)
	g := orderedGroup(t, "a/m", "b/m")
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+g+`","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	select {
	case <-a.got:
	case <-time.After(5 * time.Second):
		t.Fatal("a was never asked")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not end")
	}
	if b.count() != 0 {
		t.Fatal("b was asked after the agent went away")
	}
	tr := lastRoute(s)
	if len(tr.Tries) != 1 || tr.Tries[0].Fail != failCanceled || tr.Tries[0].Then != failCanceled || tr.Tries[0].Rest != nil || tr.Status != statusCanceled {
		t.Fatalf("trace %+v", tr)
	}
	if s.resting("a") || s.resting("b") {
		t.Fatal("a provider rests after a cancel")
	}
	if rec := s.Recent()[0]; rec.Status != statusCanceled || rec.Error != failCanceled {
		t.Fatalf("recorded %+v", rec)
	}
}

// A candidate that answers is no longer resting.
func TestAnswerLiftsRest(t *testing.T) {
	freshFailover(t)
	member(t, "solo", provider.Chat, &scripted{replies: []reply{{body: chatOK}}})
	s := New()
	s.restAfter(candidate{p: provider.Provider{ID: "solo"}, model: "m", rest: "solo"}, 500, http.Header{}, nil)
	if !s.resting("solo") {
		t.Fatal("not resting")
	}
	if code, body := postTo(s.Handler(), "/v1/chat/completions", `{"model":"solo/m","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if s.resting("solo") {
		t.Fatal("still resting after answering")
	}
}

// A stream held back for its first content is let through once it has
// waited long enough.
func TestHeldStreamPassesAfterWait(t *testing.T) {
	freshFailover(t)
	old := preContentWait
	preContentWait = 50 * time.Millisecond
	t.Cleanup(func() { preContentWait = old })
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicSSE(aStartA))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
		io.WriteString(w, anthropicSSE(aBlock, aText("late"), aStop, aMsgEnd, aDone))
	}))
	t.Cleanup(up.Close)
	for _, id := range []string{"a", "b"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Models: []string{"m"}, Anthropic: up.URL}); err != nil {
			t.Fatal(err)
		}
	}
	orderedGroup(t, "a/m", "b/m")
	srv := httptest.NewServer(New().Handler())
	t.Cleanup(srv.Close)
	res, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(anthropicStreamReq))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := res.Body.Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case first := <-got:
		if !strings.Contains(first, "message_start") {
			t.Fatalf("first read %q", first)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the held stream was never let through")
	}
	close(release)
	rest, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(rest), "late") {
		t.Fatalf("rest %s", rest)
	}
}
