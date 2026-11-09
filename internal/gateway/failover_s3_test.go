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

// s3Upstream answers each request as respond says, given how many it has had.
type s3Upstream struct {
	mu      sync.Mutex
	hits    int
	respond func(n int, w http.ResponseWriter, r *http.Request)
}

func (u *s3Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	u.mu.Lock()
	u.hits++
	n := u.hits
	u.mu.Unlock()
	u.respond(n, w, r)
}

func (u *s3Upstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

// s3Env points config, cache and home at fresh places and forgets any rest.
func s3Env(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.note = map[string]Rest{}
	restingUntil.Unlock()
}

// s3Serve saves p pointed at the scripted upstream, on the endpoints p names.
func s3Serve(t *testing.T, p provider.Provider, u *s3Upstream) {
	t.Helper()
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	p.Key = "k"
	if p.Chat != "" {
		p.Chat = up.URL + "/v1"
	}
	if p.Anthropic != "" {
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// s3Group saves an ordered group of the members, and returns the model name
// that asks for it.
func s3Group(t *testing.T, name string, members ...string) string {
	t.Helper()
	if err := provider.SaveGroup(provider.Group{Name: name, Members: members, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	return "group/" + name
}

// s3Ask sends one body to the gateway's handler and returns the reply.
func s3Ask(h http.Handler, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const s3ChatBody = `{"model":"%s","messages":[{"role":"user","content":"hi"}]}`

// s3ChatOK answers a Chat Completions request with "from-<who>".
func s3ChatOK(who string) func(int, http.ResponseWriter, *http.Request) {
	return func(n int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"s3-`+who+`","model":"s3m","choices":[{"index":0,"message":{"role":"assistant","content":"from-`+who+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}
}

// s3Fail answers with the status and a message in the error shape most vendors use.
func s3Fail(status int, msg string) func(int, http.ResponseWriter, *http.Request) {
	return func(n int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, `{"error":{"message":"`+msg+`"}}`)
	}
}

// s3Codes answers the n-th request with the n-th status, and the last one after.
func s3Codes(codes ...int) func(int, http.ResponseWriter, *http.Request) {
	return func(n int, w http.ResponseWriter, r *http.Request) {
		c := codes[len(codes)-1]
		if n <= len(codes) {
			c = codes[n-1]
		}
		if c == 200 {
			s3ChatOK("d")(n, w, r)
			return
		}
		s3Fail(c, "boom")(n, w, r)
	}
}

func TestS3RetryableClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{404, `{"error":{"message":"no such route"}}`, true},
		{401, `{"error":{"message":"bad key"}}`, true},
		{403, `{"error":{"message":"forbidden"}}`, true},
		{422, `{"error":{"message":"模型不存在"}}`, true},
		{400, `{"error":{"message":"model x is not accessible via the /chat/completions endpoint"}}`, true},
		{400, `{"error":{"message":"Insufficient Balance"}}`, true},
		{400, `{"error":{"message":"messages: field required"}}`, false},
		{400, `{"error":{"message":"This model's maximum context length is 8192 tokens, context_length_exceeded"}}`, false},
		{400, `{"error":{"message":"上下文超长"}}`, false},
		{429, `{}`, true},
		{503, `{}`, true},
		{500, `{"error":{"message":"use v1/responses"}}`, true},
		{200, `{}`, false},
	} {
		if got := retryable(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("retryable(%d, %s) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

func TestS3StreamErrorStatus(t *testing.T) {
	p := provider.Provider{Name: "S"}
	for _, tc := range []struct {
		data string
		want int
	}{
		{`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, 529},
		{`{"type":"error","error":{"type":"invalid_request_error","message":"messages: field required"}}`, 400},
		{`{"error":{"message":"boom"}}`, 502},
		{`{"type":"error","code":"server_error","message":"x"}`, 500},
		{`{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"slow"}}}`, 429},
		{`{"error":{"type":"rate_limit_error","message":"x"}}`, 429},
	} {
		got, msg := streamFailure(p, tc.data)
		if got != tc.want || !strings.HasPrefix(msg, "S: ") {
			t.Errorf("streamFailure(%s) = %d %q, want %d", tc.data, got, msg, tc.want)
		}
	}
}

// A provider that refuses a model in a way that says another one serves it
// hands the request on, and the group's next member answers.
func TestS3GroupMovesOnFailures(t *testing.T) {
	for i, tc := range []struct {
		name   string
		status int
		msg    string
		moves  bool
	}{
		{"refused on the endpoint", 400, "model s3m is not accessible via the /chat/completions endpoint", true},
		{"unauthorized", 401, "invalid api key", true},
		{"not found", 404, "no such route", true},
		{"model missing, in Chinese", 400, "模型不存在", true},
		{"bad request", 400, "messages: field required", false},
		{"context too long", 400, "上下文超长", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s3Env(t)
			a := &s3Upstream{respond: s3Fail(tc.status, tc.msg)}
			b := &s3Upstream{respond: s3ChatOK("b")}
			idA, idB := "s3ma"+string(rune('a'+i)), "s3mb"+string(rune('a'+i))
			s3Serve(t, provider.Provider{ID: idA, Name: "A", Models: []string{"s3m"}, Chat: "x"}, a)
			s3Serve(t, provider.Provider{ID: idB, Name: "B", Models: []string{"s3m"}, Chat: "x"}, b)
			model := s3Group(t, "s3mg"+string(rune('a'+i)), idA+"/s3m", idB+"/s3m")

			code, body := s3Ask(New().Handler(), "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
			if tc.moves {
				if code != 200 || !strings.Contains(body, "from-b") || b.count() != 1 {
					t.Fatalf("moved: %d %s (b asked %d times)", code, body, b.count())
				}
				return
			}
			if code != tc.status || !strings.Contains(body, tc.msg) || b.count() != 0 {
				t.Fatalf("stayed: %d %s (b asked %d times)", code, body, b.count())
			}
		})
	}
}

// An Anthropic stream that fails before any text is replaced by the next
// member; the agent sees only that member's reply, and the failed one rests.
func TestS3AnthropicStreamOpeningFailureMovesOn(t *testing.T) {
	s3Env(t)
	a := &s3Upstream{respond: func(n int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_from_a\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"s3m\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
			"event: ping\ndata: {\"type\":\"ping\"}\n\n"+
			"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n")
	}}
	b := &s3Upstream{respond: func(n int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_from_b\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"s3m\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"from-b\"}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}}
	s3Serve(t, provider.Provider{ID: "s3aa", Name: "A", Models: []string{"s3m"}, Anthropic: "x"}, a)
	s3Serve(t, provider.Provider{ID: "s3ab", Name: "B", Models: []string{"s3m"}, Anthropic: "x"}, b)
	model := s3Group(t, "s3anth", "s3aa/s3m", "s3ab/s3m")

	s := New()
	code, body := s3Ask(s.Handler(), "/v1/messages", `{"model":"`+model+`","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from-b") || strings.Contains(body, "Overloaded") || strings.Contains(body, "msg_from_a") {
		t.Fatalf("agent got %d %s", code, body)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Status != 529 || r.Tries[0].Rest == nil || r.Tries[1].Status != 200 {
		t.Fatalf("tries: %+v", r.Tries)
	}
	if !s.resting("s3aa") {
		t.Fatal("the failed member does not rest")
	}
}

// Once text has gone to the agent, an error is the agent's to see, and no
// other member is asked.
func TestS3AnthropicStreamErrorAfterTextStays(t *testing.T) {
	s3Env(t)
	a := &s3Upstream{respond: func(n int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_a\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"s3m\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"from-a\"}}\n\n"+
			"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n")
	}}
	b := &s3Upstream{respond: s3ChatOK("b")}
	s3Serve(t, provider.Provider{ID: "s3ca", Name: "A", Models: []string{"s3m"}, Anthropic: "x"}, a)
	s3Serve(t, provider.Provider{ID: "s3cb", Name: "B", Models: []string{"s3m"}, Chat: "x"}, b)
	model := s3Group(t, "s3after", "s3ca/s3m", "s3cb/s3m")

	code, body := s3Ask(New().Handler(), "/v1/messages", `{"model":"`+model+`","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from-a") || !strings.Contains(body, "Overloaded") || b.count() != 0 {
		t.Fatalf("%d %s (b asked %d times)", code, body, b.count())
	}
}

// A Chat stream that fails before its text, read into an Anthropic client's
// shape, is replaced by the next member too: the translated path.
func TestS3TranslatedStreamOpeningFailureMovesOn(t *testing.T) {
	s3Env(t)
	a := &s3Upstream{respond: func(n int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c-a\",\"model\":\"s3m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"+
			"data: {\"error\":{\"message\":\"Overloaded\"}}\n\n")
	}}
	b := &s3Upstream{respond: func(n int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c-b\",\"model\":\"s3m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"from-b\"}}]}\n\n"+
			"data: {\"id\":\"c-b\",\"model\":\"s3m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n"+
			"data: [DONE]\n\n")
	}}
	s3Serve(t, provider.Provider{ID: "s3ta", Name: "A", Models: []string{"s3m"}, Chat: "x"}, a)
	s3Serve(t, provider.Provider{ID: "s3tb", Name: "B", Models: []string{"s3m"}, Chat: "x"}, b)
	model := s3Group(t, "s3trans", "s3ta/s3m", "s3tb/s3m")

	code, body := s3Ask(New().Handler(), "/v1/messages", `{"model":"`+model+`","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from-b") || strings.Contains(body, "Overloaded") || a.count() != 1 || b.count() != 1 {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.count(), b.count())
	}
}

// The last candidate is tried again while its failure may pass: a 503 and a
// 502 are waited out, a third try answers; a failure that persists is handed
// over after three tries; a bad request is not tried again.
func TestS3LastCandidateRetries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		codes  []int
		status int
		hits   int
		waits  []int64
		body   string
	}{
		{"passes after two", []int{503, 502, 200}, 200, 3, []int64{1000, 2000}, "from-d"},
		{"persists", []int{503}, 503, 3, []int64{1000, 2000}, "boom"},
		{"bad request is not retried", []int{400}, 400, 1, nil, "boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s3Env(t)
			u := &s3Upstream{respond: s3Codes(tc.codes...)}
			s3Serve(t, provider.Provider{ID: "s3rd", Name: "D", Models: []string{"s3m"}, Chat: "x"}, u)
			s := New()
			code, body := s3Ask(s.Handler(), "/v1/chat/completions", `{"model":"s3rd/s3m","messages":[{"role":"user","content":"hi"}]}`)
			if code != tc.status || !strings.Contains(body, tc.body) || u.count() != tc.hits {
				t.Fatalf("%d %s (asked %d times)", code, body, u.count())
			}
			r := s.trace.routes[len(s.trace.routes)-1]
			if len(r.Tries) != 1 || len(r.Tries[0].Retries) != len(tc.waits) {
				t.Fatalf("tries: %+v", r.Tries)
			}
			for i, w := range tc.waits {
				if got := r.Tries[0].Retries[i].WaitMs; got != w {
					t.Fatalf("retry %d waited %dms, want %d", i, got, w)
				}
			}
		})
	}
}

// An agent that goes away while its candidate hangs: nobody else is asked,
// the record says canceled, and the candidate does not rest.
func TestS3CancelledAgentRestsNobody(t *testing.T) {
	s3Env(t)
	started := make(chan struct{}, 1)
	a := &s3Upstream{respond: func(n int, w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}}
	b := &s3Upstream{respond: s3ChatOK("b")}
	s3Serve(t, provider.Provider{ID: "s3xa", Name: "A", Models: []string{"s3m"}, Chat: "x"}, a)
	s3Serve(t, provider.Provider{ID: "s3xb", Name: "B", Models: []string{"s3m"}, Chat: "x"}, b)
	model := s3Group(t, "s3cancel", "s3xa/s3m", "s3xb/s3m")

	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first member was never asked")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the request did not end after the agent went away")
	}

	if b.count() != 0 {
		t.Fatalf("the next member was asked %d times", b.count())
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if r.Status != statusCanceled || len(r.Tries) != 1 || r.Tries[0].Fail != failCanceled || r.Tries[0].Rest != nil {
		t.Fatalf("route: status %d tries %+v", r.Status, r.Tries)
	}
	if s.resting("s3xa") {
		t.Fatal("the hanging member rests")
	}
}
