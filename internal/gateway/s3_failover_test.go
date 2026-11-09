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

// s3Reply is one answer an s3Up gives. A hang reply never answers: it waits
// until the request is gone.
type s3Reply struct {
	code  int
	ctype string
	hdr   map[string]string
	body  string
	hang  bool
}

// s3Up is an upstream that answers each path with its replies in turn; the
// last one repeats. It counts the requests it gets, by path.
type s3Up struct {
	mu      sync.Mutex
	replies map[string][]s3Reply
	hits    map[string]int
	arrived chan struct{} // told when a hang reply is reached
}

func (u *s3Up) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	u.mu.Lock()
	n := u.hits[r.URL.Path]
	u.hits[r.URL.Path]++
	list := u.replies[r.URL.Path]
	u.mu.Unlock()
	if len(list) == 0 {
		w.WriteHeader(404)
		return
	}
	rep := list[min(n, len(list)-1)]
	if rep.hang {
		select {
		case u.arrived <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return
	}
	ct := rep.ctype
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	for k, v := range rep.hdr {
		w.Header().Set(k, v)
	}
	if rep.code != 0 {
		w.WriteHeader(rep.code)
	}
	io.WriteString(w, rep.body)
}

// s3Hits is how many requests each path has had so far.
func (u *s3Up) s3Hits() map[string]int {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := map[string]int{}
	for k, v := range u.hits {
		out[k] = v
	}
	return out
}

// s3Fresh clears what earlier tests left: resting candidates, affinity, and
// the user's config and agents.
func s3Fresh(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.note = map[string]Rest{}
	restingUntil.Unlock()
	sticks.Lock()
	sticks.m = map[string]stick{}
	sticks.Unlock()
}

// s3Upstream starts an upstream with the given replies and saves provider p
// pointed at it: each base p sets gets the upstream's URL.
func s3Upstream(t *testing.T, p provider.Provider, replies map[string][]s3Reply) *s3Up {
	t.Helper()
	u := &s3Up{replies: replies, hits: map[string]int{}, arrived: make(chan struct{}, 4)}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	if p.Chat != "" {
		p.Chat = up.URL + "/v1"
	}
	if p.Responses != "" {
		p.Responses = up.URL + "/v1"
	}
	if p.Anthropic != "" {
		p.Anthropic = up.URL
	}
	p.Key = "k"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return u
}

// s3Call posts a request to a gateway, and returns the reply's code, body
// and route trace.
func s3Call(t *testing.T, ctx context.Context, s *Server, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body)).WithContext(ctx)
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// s3Route is the trace of the newest request.
func s3Route(t *testing.T, s *Server) Route {
	t.Helper()
	st := s.Trace(context.Background(), 0, 0)
	if len(st.Routes) == 0 {
		t.Fatal("no route traced")
	}
	return st.Routes[len(st.Routes)-1]
}

// s3Sooner makes the last provider's retries wait a few milliseconds.
func s3Sooner(t *testing.T) {
	t.Helper()
	old := retryDelays
	retryDelays = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}
	t.Cleanup(func() { retryDelays = old })
}

const s3ChatOK = `{"id":"c1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"from B"},"finish_reason":"stop"}]}`

const s3ChatStreamOK = "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"from B\"}}]}\n\ndata: [DONE]\n\n"

const s3ChatStreamOverloaded = "data: {\"error\":{\"message\":\"Overloaded\",\"type\":\"overloaded_error\"}}\n\n"

const s3AnthropicHead = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_%s\",\"model\":\"m\",\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n" +
	"event: ping\ndata: {\"type\":\"ping\"}\n\n"

const s3AnthropicOverloaded = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"

const s3AnthropicTextOK = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"from B\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// s3Group saves the group s3grp over members a and b, in order.
func s3Group(t *testing.T) {
	t.Helper()
	if err := provider.SaveGroup(provider.Group{ID: "s3grp", Name: "S3 group", Members: []string{"s3a/m1", "s3b/m2"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
}

// Group [A, B]: A refuses with a model-level error, B answers; the agent
// gets B's answer, and the transfer shows in the trace.
func TestS3GroupTransfersOnModelRefusal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     int
		body     string
		want     string // in A's error, as the agent would see it when A's is delivered
		transfer bool
	}{
		{"not accessible on this endpoint", 400, `{"error":{"message":"model m1 is not accessible via the /chat/completions endpoint"}}`, "", true},
		{"unauthorized", 401, `{"error":{"message":"invalid key"}}`, "", true},
		{"not found", 404, `{"error":{"message":"not found"}}`, "", true},
		{"model does not exist in Chinese", 400, `{"error":{"message":"模型不存在"}}`, "", true},
		{"field required", 400, `{"error":{"message":"messages: field required"}}`, "field required", false},
		{"context too long", 400, `{"error":{"message":"上下文超长，请缩短输入 context_length_exceeded"}}`, "上下文超长", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s3Fresh(t)
			a := s3Upstream(t, provider.Provider{ID: "s3a", Name: "S3A", Chat: "x", Models: []string{"m1"}},
				map[string][]s3Reply{"/v1/chat/completions": {{code: tc.code, body: tc.body}}})
			b := s3Upstream(t, provider.Provider{ID: "s3b", Name: "S3B", Chat: "x", Models: []string{"m2"}},
				map[string][]s3Reply{"/v1/chat/completions": {{body: s3ChatOK}}})
			s3Group(t)
			s := New()
			code, body := s3Call(t, context.Background(), s, "/v1/chat/completions", `{"model":"group/s3grp","messages":[{"role":"user","content":"hi"}]}`)
			if tc.transfer {
				if code != 200 || !strings.Contains(body, "from B") || b.s3Hits()["/v1/chat/completions"] != 1 {
					t.Fatalf("want B's answer: %d %s (B hits %v)", code, body, b.s3Hits())
				}
				if r := s3Route(t, s); len(r.Tries) != 2 || r.Tries[0].Rest == nil {
					t.Fatalf("trace should show A moved on from: %+v", r.Tries)
				}
				return
			}
			if code != tc.code || !strings.Contains(body, tc.want) {
				t.Fatalf("want A's %d delivered: %d %s", tc.code, code, body)
			}
			if b.s3Hits()["/v1/chat/completions"] != 0 || a.s3Hits()["/v1/chat/completions"] != 1 {
				t.Fatalf("nothing should move on: A %v, B %v", a.s3Hits(), b.s3Hits())
			}
		})
	}
}

// Anthropic stream: A opens with message_start and ping, then reports
// overloaded before any content. The agent sees only B's answer, and A rests.
func TestS3StreamFailingBeforeContentMovesOn(t *testing.T) {
	s3Fresh(t)
	a := s3Upstream(t, provider.Provider{ID: "s3a", Name: "S3A", Anthropic: "x", Models: []string{"m1"}, Fallback: []string{"s3b/m2"}},
		map[string][]s3Reply{"/v1/messages": {{ctype: "text/event-stream", body: strings.ReplaceAll(s3AnthropicHead, "%s", "A") + s3AnthropicOverloaded}}})
	b := s3Upstream(t, provider.Provider{ID: "s3b", Name: "S3B", Anthropic: "x", Models: []string{"m2"}},
		map[string][]s3Reply{"/v1/messages": {{ctype: "text/event-stream", body: strings.ReplaceAll(s3AnthropicHead, "%s", "B") + s3AnthropicTextOK}}})
	s := New()
	code, body := s3Call(t, context.Background(), s, "/v1/messages", `{"model":"s3a/m1","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from B") || !strings.Contains(body, "msg_B") {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(body, "Overloaded") || strings.Contains(body, "msg_A") {
		t.Fatalf("the agent heard the dropped candidate: %s", body)
	}
	if a.s3Hits()["/v1/messages"] != 1 || b.s3Hits()["/v1/messages"] != 1 {
		t.Fatalf("hits A %v B %v", a.s3Hits(), b.s3Hits())
	}
	r := s3Route(t, s)
	if len(r.Tries) != 2 || r.Tries[0].Status != 529 || r.Tries[0].Rest == nil || r.Tries[1].Status != 200 {
		t.Fatalf("trace: %+v", r.Tries)
	}
	if _, resting := restOf("s3a"); !resting {
		t.Fatal("A should rest after overloaded")
	}
}

// Once A has sent content, its error is the agent's and B is not asked.
func TestS3StreamFailingAfterContentIsNotMoved(t *testing.T) {
	s3Fresh(t)
	a := s3Upstream(t, provider.Provider{ID: "s3a", Name: "S3A", Anthropic: "x", Models: []string{"m1"}, Fallback: []string{"s3b/m2"}},
		map[string][]s3Reply{"/v1/messages": {{ctype: "text/event-stream", body: strings.ReplaceAll(s3AnthropicHead, "%s", "A") +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n" + s3AnthropicOverloaded}}})
	b := s3Upstream(t, provider.Provider{ID: "s3b", Name: "S3B", Anthropic: "x", Models: []string{"m2"}},
		map[string][]s3Reply{"/v1/messages": {{ctype: "text/event-stream", body: s3AnthropicTextOK}}})
	s := New()
	code, body := s3Call(t, context.Background(), s, "/v1/messages", `{"model":"s3a/m1","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "partial") || !strings.Contains(body, "Overloaded") {
		t.Fatalf("the agent should get A's text and error: %d %s", code, body)
	}
	if b.s3Hits()["/v1/messages"] != 0 || a.s3Hits()["/v1/messages"] != 1 {
		t.Fatalf("B must not be asked: A %v B %v", a.s3Hits(), b.s3Hits())
	}
}

// A Chat client to a provider that only speaks Anthropic: the translated
// stream also waits, and hands on when the first event is an overloaded one.
func TestS3TranslatedStreamFailingBeforeContentMovesOn(t *testing.T) {
	s3Fresh(t)
	s3Upstream(t, provider.Provider{ID: "s3a", Name: "S3A", Anthropic: "x", Models: []string{"m1"}, Fallback: []string{"s3b/m2"}},
		map[string][]s3Reply{"/v1/messages": {{ctype: "text/event-stream", body: strings.ReplaceAll(s3AnthropicHead, "%s", "A") + s3AnthropicOverloaded}}})
	s3Upstream(t, provider.Provider{ID: "s3b", Name: "S3B", Anthropic: "x", Models: []string{"m2"}},
		map[string][]s3Reply{"/v1/messages": {{ctype: "text/event-stream", body: s3AnthropicTextOK}}})
	s := New()
	code, body := s3Call(t, context.Background(), s, "/v1/chat/completions", `{"model":"s3a/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from B") || strings.Contains(body, "Overloaded") {
		t.Fatalf("%d %s", code, body)
	}
}

// A Chat stream whose first chunk is an overloaded error moves on too.
func TestS3ChatStreamErrorChunkMovesOn(t *testing.T) {
	s3Fresh(t)
	s3Upstream(t, provider.Provider{ID: "s3a", Name: "S3A", Chat: "x", Models: []string{"m1"}, Fallback: []string{"s3b/m2"}},
		map[string][]s3Reply{"/v1/chat/completions": {{ctype: "text/event-stream", body: s3ChatStreamOverloaded}}})
	b := s3Upstream(t, provider.Provider{ID: "s3b", Name: "S3B", Chat: "x", Models: []string{"m2"}},
		map[string][]s3Reply{"/v1/chat/completions": {{ctype: "text/event-stream", body: s3ChatStreamOK}}})
	s := New()
	code, body := s3Call(t, context.Background(), s, "/v1/chat/completions", `{"model":"s3a/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from B") || strings.Contains(body, "Overloaded") || b.s3Hits()["/v1/chat/completions"] != 1 {
		t.Fatalf("%d %s", code, body)
	}
}

// A lone provider that answers 503, then 502, then 200: the agent gets the
// 200, after three requests, and the trace shows the two waits.
func TestS3LastProviderRetriesRecoverableFailures(t *testing.T) {
	s3Fresh(t)
	s3Sooner(t)
	u := s3Upstream(t, provider.Provider{ID: "s3a", Name: "S3A", Chat: "x", Models: []string{"m1"}},
		map[string][]s3Reply{"/v1/chat/completions": {
			{code: 503, body: `{"error":{"message":"down"}}`},
			{code: 502, body: `{"error":{"message":"bad gateway"}}`},
			{ctype: "text/event-stream", body: s3ChatStreamOK},
		}})
	s := New()
	code, body := s3Call(t, context.Background(), s, "/v1/chat/completions", `{"model":"s3a/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from B") || u.s3Hits()["/v1/chat/completions"] != 3 {
		t.Fatalf("%d %s (hits %v)", code, body, u.s3Hits())
	}
	r := s3Route(t, s)
	if len(r.Tries) != 1 || len(r.Tries[0].Retries) != 2 {
		t.Fatalf("want one try with two retries: %+v", r.Tries)
	}
	if got := r.Tries[0].Retries; got[0].Status != 503 || got[0].Wait != 5 || got[1].Status != 502 || got[1].Wait != 10 {
		t.Fatalf("retries: %+v", got)
	}
	if _, resting := restOf("s3a"); resting {
		t.Fatal("a provider that answered must not rest")
	}
}

// A lone provider that keeps answering 503 gets three requests, then the 503.
func TestS3LastProviderGivesUpAfterThree(t *testing.T) {
	s3Fresh(t)
	s3Sooner(t)
	u := s3Upstream(t, provider.Provider{ID: "s3a", Name: "S3A", Chat: "x", Models: []string{"m1"}},
		map[string][]s3Reply{"/v1/chat/completions": {{code: 503, body: `{"error":{"message":"still down"}}`}}})
	s := New()
	code, body := s3Call(t, context.Background(), s, "/v1/chat/completions", `{"model":"s3a/m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 503 || !strings.Contains(body, "still down") || u.s3Hits()["/v1/chat/completions"] != 3 {
		t.Fatalf("%d %s (hits %v)", code, body, u.s3Hits())
	}
}

// A plain bad request is not retried, and reaches the agent at once.
func TestS3PlainBadRequestIsNotRetried(t *testing.T) {
	s3Fresh(t)
	s3Sooner(t)
	u := s3Upstream(t, provider.Provider{ID: "s3a", Name: "S3A", Chat: "x", Models: []string{"m1"}},
		map[string][]s3Reply{"/v1/chat/completions": {{code: 400, body: `{"error":{"message":"messages: field required"}}`}}})
	s := New()
	code, body := s3Call(t, context.Background(), s, "/v1/chat/completions", `{"model":"s3a/m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || !strings.Contains(body, "field required") || u.s3Hits()["/v1/chat/completions"] != 1 {
		t.Fatalf("%d %s (hits %v)", code, body, u.s3Hits())
	}
}

// A long Retry-After is not waited for; a 429 without one is not retried.
func TestS3RetryAfterRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		rep  s3Reply
		hits int
	}{
		{"long Retry-After is not waited for", s3Reply{code: 503, hdr: map[string]string{"Retry-After": "10"}, body: `{"error":{"message":"busy"}}`}, 1},
		{"429 without Retry-After is not retried", s3Reply{code: 429, body: `{"error":{"message":"slow down"}}`}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s3Fresh(t)
			s3Sooner(t)
			u := s3Upstream(t, provider.Provider{ID: "s3a", Name: "S3A", Chat: "x", Models: []string{"m1"}},
				map[string][]s3Reply{"/v1/chat/completions": {tc.rep}})
			s := New()
			start := time.Now()
			s3Call(t, context.Background(), s, "/v1/chat/completions", `{"model":"s3a/m1","messages":[{"role":"user","content":"hi"}]}`)
			if got := u.s3Hits()["/v1/chat/completions"]; got != tc.hits {
				t.Fatalf("hits %d, want %d", got, tc.hits)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("it waited")
			}
		})
	}
}

// A 429 with Retry-After: 0 is retried at once, and then answered.
func TestS3RateLimitWithRetryAfterIsRetried(t *testing.T) {
	s3Fresh(t)
	s3Sooner(t)
	u := s3Upstream(t, provider.Provider{ID: "s3a", Name: "S3A", Chat: "x", Models: []string{"m1"}},
		map[string][]s3Reply{"/v1/chat/completions": {
			{code: 429, hdr: map[string]string{"Retry-After": "0"}, body: `{"error":{"message":"slow down"}}`},
			{body: s3ChatOK},
		}})
	s := New()
	code, body := s3Call(t, context.Background(), s, "/v1/chat/completions", `{"model":"s3a/m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from B") || u.s3Hits()["/v1/chat/completions"] != 2 {
		t.Fatalf("%d %s (hits %v)", code, body, u.s3Hits())
	}
}

// A candidate that answers lifts its rest at once, even after a retry.
func TestS3SuccessLiftsTheRest(t *testing.T) {
	s3Fresh(t)
	s3Sooner(t)
	s3Upstream(t, provider.Provider{ID: "s3a", Name: "S3A", Chat: "x", Models: []string{"m1"}},
		map[string][]s3Reply{"/v1/chat/completions": {
			{code: 429, body: `{"error":{"message":"slow down"}}`},
			{body: s3ChatOK},
		}})
	s3Upstream(t, provider.Provider{ID: "s3b", Name: "S3B", Chat: "x", Models: []string{"m2"}},
		map[string][]s3Reply{"/v1/chat/completions": {
			{body: s3ChatOK},
			{code: 503, body: `{"error":{"message":"down"}}`},
		}})
	s3Group(t)
	s := New()
	send := func() (int, string) {
		return s3Call(t, context.Background(), s, "/v1/chat/completions", `{"model":"group/s3grp","messages":[{"role":"user","content":"hi"}]}`)
	}
	if code, body := send(); code != 200 {
		t.Fatalf("first: %d %s", code, body)
	}
	if _, resting := restOf("s3a"); !resting {
		t.Fatal("A should rest after its 429")
	}
	if code, body := send(); code != 200 || !strings.Contains(body, "from B") {
		t.Fatalf("second: %d %s", code, body)
	}
	if _, resting := restOf("s3a"); resting {
		t.Fatal("A's rest should lift when it answers")
	}
	if _, resting := restOf("s3b"); !resting {
		t.Fatal("B should rest after its 503")
	}
}

// The agent goes away while A is still working: B is not asked, the trace
// shows one canceled try, and no provider rests.
func TestS3CancelByAgentRestsNobody(t *testing.T) {
	s3Fresh(t)
	a := s3Upstream(t, provider.Provider{ID: "s3a", Name: "S3A", Chat: "x", Models: []string{"m1"}},
		map[string][]s3Reply{"/v1/chat/completions": {{hang: true}}})
	b := s3Upstream(t, provider.Provider{ID: "s3b", Name: "S3B", Chat: "x", Models: []string{"m2"}},
		map[string][]s3Reply{"/v1/chat/completions": {{body: s3ChatOK}}})
	s3Group(t)
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s3Call(t, ctx, s, "/v1/chat/completions", `{"model":"group/s3grp","messages":[{"role":"user","content":"hi"}]}`)
	}()
	select {
	case <-a.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("A was never asked")
	}
	cancel()
	<-done
	if b.s3Hits()["/v1/chat/completions"] != 0 {
		t.Fatalf("B was asked after the agent left: %v", b.s3Hits())
	}
	r := s3Route(t, s)
	if len(r.Tries) != 1 || r.Tries[0].Fail != failCanceled || r.Tries[0].Rest != nil || r.Status != 499 {
		t.Fatalf("trace: status %d tries %+v", r.Status, r.Tries)
	}
	if _, resting := restOf("s3a"); resting {
		t.Fatal("A must not rest when the agent cancels")
	}
}
