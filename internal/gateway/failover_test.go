package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// scriptReply is one answer an upstream gives; the last one repeats.
type scriptReply struct {
	code  int
	ctype string
	head  map[string]string
	body  string
}

// scriptUp is an upstream that answers in order and counts what it was asked.
// With hang set it answers nothing until the request is abandoned, and
// signals hit on the first request.
type scriptUp struct {
	mu      sync.Mutex
	answers []scriptReply
	n       int
	hang    bool
	hit     chan struct{}
}

func (u *scriptUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	u.mu.Lock()
	i := u.n
	u.n++
	u.mu.Unlock()
	if u.hang {
		select {
		case u.hit <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return
	}
	a := u.answers[min(i, len(u.answers)-1)]
	ctype := a.ctype
	if ctype == "" {
		ctype = "application/json"
	}
	w.Header().Set("Content-Type", ctype)
	for k, v := range a.head {
		w.Header().Set(k, v)
	}
	if a.code != 0 {
		w.WriteHeader(a.code)
	}
	io.WriteString(w, a.body)
}

func (u *scriptUp) calls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.n
}

// upstreamAt serves u for the test's life and returns its base URL.
func upstreamAt(t *testing.T, u *scriptUp) string {
	t.Helper()
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	return srv.URL
}

func saveProviderOrFail(t *testing.T, p provider.Provider) {
	t.Helper()
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// saveFailoverGroup makes group "g" of the members, in order.
func saveFailoverGroup(t *testing.T, members ...string) {
	t.Helper()
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: members, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
}

// recordWaits makes s's retries wait for nothing, and returns what they asked to wait.
func recordWaits(s *Server) *[]time.Duration {
	var waits []time.Duration
	s.sleep = func(ctx context.Context, d time.Duration) bool {
		waits = append(waits, d)
		return ctx.Err() == nil
	}
	return &waits
}

// lastRoute is the newest request in the trace.
func lastRoute(t *testing.T, s *Server) Route {
	t.Helper()
	rs := s.Trace(context.Background(), 0, 0).Routes
	if len(rs) == 0 {
		t.Fatal("no route traced")
	}
	return rs[len(rs)-1]
}

const (
	groupChat = `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`
	soloChat  = `{"model":"solo/m1","messages":[{"role":"user","content":"hi"}]}`
	// fromB is a Chat Completions answer from provider b, as JSON
	fromB = `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from b"},"finish_reason":"stop"}]}`
)

// A group's members are tried in order: a refusal of the model, an auth or
// not-found failure, or a model that does not exist hands the request on,
// and the agent gets the next member's answer. A request the member
// rejects for its own sake is answered as it is.
func TestGroupTransfersOnRefusals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		a        scriptReply
		transfer bool
	}{
		{"not accessible via the endpoint", scriptReply{code: 400, body: `{"error":{"message":"model m is not accessible via the /chat/completions endpoint"}}`}, true},
		{"unauthorized", scriptReply{code: 401, body: `{"error":{"message":"invalid key"}}`}, true},
		{"not found", scriptReply{code: 404, body: `{"error":{"message":"route not found"}}`}, true},
		{"model does not exist, in Chinese", scriptReply{code: 400, body: `{"error":{"message":"模型不存在"}}`}, true},
		{"messages field required", scriptReply{code: 400, body: `{"error":{"message":"messages: field required"}}`}, false},
		{"context too long", scriptReply{code: 400, body: `{"error":{"message":"maximum context length exceeded"}}`}, false},
		{"context too long, in Chinese", scriptReply{code: 400, body: `{"error":{"message":"上下文超长"}}`}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			a := &scriptUp{answers: []scriptReply{tc.a}}
			b := &scriptUp{answers: []scriptReply{{body: fromB}}}
			saveProviderOrFail(t, provider.Provider{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}, Chat: upstreamAt(t, a) + "/v1"})
			saveProviderOrFail(t, provider.Provider{ID: "b", Name: "B", Key: "kb", Models: []string{"m"}, Chat: upstreamAt(t, b) + "/v1"})
			saveFailoverGroup(t, "a/m", "b/m")
			code, body := serveTo(t, New(), "/v1/chat/completions", groupChat)
			if tc.transfer {
				if code != 200 || !strings.Contains(body, "from b") || a.calls() != 1 || b.calls() != 1 {
					t.Fatalf("%d %s (a asked %d, b asked %d)", code, body, a.calls(), b.calls())
				}
				return
			}
			if code != tc.a.code || strings.Contains(body, "from b") || b.calls() != 0 {
				t.Fatalf("%d %s (b asked %d)", code, body, b.calls())
			}
		})
	}
}

// An Anthropic stream that overloads before any content hands the request
// on: the agent sees only the next member's reply, and the first member is
// recorded as overloaded and resting.
func TestAnthropicStreamOverloadedBeforeContentTransfers(t *testing.T) {
	fresh(t)
	a := &scriptUp{answers: []scriptReply{{ctype: "text/event-stream", body: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_A","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: ping`+"\n"+`data: {"type":"ping"}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)}}}
	b := &scriptUp{answers: []scriptReply{{ctype: "text/event-stream", body: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_B","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from B"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}}}
	saveProviderOrFail(t, provider.Provider{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}, Anthropic: upstreamAt(t, a)})
	saveProviderOrFail(t, provider.Provider{ID: "b", Name: "B", Key: "kb", Models: []string{"m"}, Anthropic: upstreamAt(t, b)})
	saveFailoverGroup(t, "a/m", "b/m")
	s := New()
	code, body := serveTo(t, s, "/v1/messages", `{"model":"group/g","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from B") || strings.Contains(body, "Overloaded") || strings.Contains(body, "msg_A") {
		t.Fatalf("%d %s", code, body)
	}
	r := lastRoute(t, s)
	if len(r.Tries) != 2 || r.Tries[0].Status != 529 || r.Tries[0].Rest == nil || r.Tries[0].Kind != "transfer" {
		t.Fatalf("tries %+v", r.Tries)
	}
	restingUntil.Lock()
	_, resting := restingUntil.m["a"]
	restingUntil.Unlock()
	if !resting {
		t.Fatal("the overloaded member does not rest")
	}
}

// Once content has gone out, an error is the agent's to see, and no other
// member is asked.
func TestAnthropicStreamErrorAfterContentIsNotTransferred(t *testing.T) {
	fresh(t)
	a := &scriptUp{answers: []scriptReply{{ctype: "text/event-stream", body: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_A","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)}}}
	b := &scriptUp{answers: []scriptReply{{body: fromB}}}
	saveProviderOrFail(t, provider.Provider{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}, Anthropic: upstreamAt(t, a)})
	saveProviderOrFail(t, provider.Provider{ID: "b", Name: "B", Key: "kb", Models: []string{"m"}, Chat: upstreamAt(t, b) + "/v1"})
	saveFailoverGroup(t, "a/m", "b/m")
	code, body := serveTo(t, New(), "/v1/messages", `{"model":"group/g","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "partial") || !strings.Contains(body, "Overloaded") || b.calls() != 0 {
		t.Fatalf("%d %s (b asked %d)", code, body, b.calls())
	}
}

// A lone provider that fails transiently is tried again, a second and then
// two seconds later; the trace says when.
func TestLoneProviderRetriesTransientFailures(t *testing.T) {
	fresh(t)
	u := &scriptUp{answers: []scriptReply{
		{code: 503, body: `{"error":{"message":"busy"}}`},
		{code: 502, body: `{"error":{"message":"bad gateway"}}`},
		{body: fromB},
	}}
	saveProviderOrFail(t, provider.Provider{ID: "solo", Name: "Solo", Key: "k", Models: []string{"m1"}, Chat: upstreamAt(t, u) + "/v1"})
	s := New()
	waits := recordWaits(s)
	code, body := serveTo(t, s, "/v1/chat/completions", soloChat)
	if code != 200 || !strings.Contains(body, "from b") || u.calls() != 3 {
		t.Fatalf("%d %s (asked %d)", code, body, u.calls())
	}
	if want := []time.Duration{time.Second, 2 * time.Second}; !reflect.DeepEqual(*waits, want) {
		t.Fatalf("waits %v, want %v", *waits, want)
	}
	r := lastRoute(t, s)
	if len(r.Tries) != 3 || r.Tries[0].Kind != "retry" || r.Tries[0].WaitMs != 1000 ||
		r.Tries[1].Kind != "retry" || r.Tries[1].WaitMs != 2000 || r.Tries[2].Kind != "" || r.Tries[2].Status != 200 {
		t.Fatalf("tries %+v", r.Tries)
	}
}

// A failure that persists is delivered after the three tries.
func TestPersistentFailureIsDeliveredAfterThreeTries(t *testing.T) {
	fresh(t)
	u := &scriptUp{answers: []scriptReply{{code: 503, body: `{"error":{"message":"busy"}}`}}}
	saveProviderOrFail(t, provider.Provider{ID: "solo", Name: "Solo", Key: "k", Models: []string{"m1"}, Chat: upstreamAt(t, u) + "/v1"})
	s := New()
	waits := recordWaits(s)
	code, body := serveTo(t, s, "/v1/chat/completions", soloChat)
	if code != 503 || !strings.Contains(body, "busy") || u.calls() != 3 || len(*waits) != 2 {
		t.Fatalf("%d %s (asked %d, waits %v)", code, body, u.calls(), *waits)
	}
}

// Retries follow the vendor's word or the default; those that the status
// does not earn are not made.
func TestLoneProviderRetryRules(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []scriptReply
		code    int
		asked   int
		waits   []time.Duration
	}{
		{"a plain bad request is asked once", []scriptReply{{code: 400, body: `{"error":{"message":"messages: field required"}}`}}, 400, 1, nil},
		{"a long Retry-After is not waited out", []scriptReply{{code: 503, head: map[string]string{"Retry-After": "30"}, body: `{"error":{"message":"busy"}}`}}, 503, 1, nil},
		{"a 429 without Retry-After is not retried", []scriptReply{{code: 429, body: `{"error":{"message":"slow down"}}`}}, 429, 1, nil},
		{"a 429 with a short Retry-After is retried after it", []scriptReply{{code: 429, head: map[string]string{"Retry-After": "2"}, body: `{"error":{"message":"slow down"}}`}, {body: fromB}}, 200, 2, []time.Duration{2 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			u := &scriptUp{answers: tc.answers}
			saveProviderOrFail(t, provider.Provider{ID: "solo", Name: "Solo", Key: "k", Models: []string{"m1"}, Chat: upstreamAt(t, u) + "/v1"})
			s := New()
			waits := recordWaits(s)
			code, body := serveTo(t, s, "/v1/chat/completions", soloChat)
			if code != tc.code || u.calls() != tc.asked || !reflect.DeepEqual(append([]time.Duration(nil), *waits...), tc.waits) {
				t.Fatalf("%d %s (asked %d, waits %v)", code, body, u.calls(), *waits)
			}
		})
	}
}

// The agent going away while a member is still answering: no other member
// is asked, the request is recorded once as canceled, and the member does
// not rest.
func TestCanceledWhileAMemberHangs(t *testing.T) {
	fresh(t)
	hit := make(chan struct{}, 1)
	a := &scriptUp{hang: true, hit: hit}
	b := &scriptUp{answers: []scriptReply{{body: fromB}}}
	saveProviderOrFail(t, provider.Provider{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}, Chat: upstreamAt(t, a) + "/v1"})
	saveProviderOrFail(t, provider.Provider{ID: "b", Name: "B", Key: "kb", Models: []string{"m"}, Chat: upstreamAt(t, b) + "/v1"})
	saveFailoverGroup(t, "a/m", "b/m")
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(groupChat)).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	<-hit
	cancel()
	<-done
	if b.calls() != 0 {
		t.Fatalf("the next member was asked %d times", b.calls())
	}
	r := lastRoute(t, s)
	if r.Status != statusCanceled || r.Error != failCanceled || len(r.Tries) != 1 || r.Tries[0].Kind != "canceled" || r.Tries[0].Fail != failCanceled {
		t.Fatalf("route %+v", r)
	}
	if rec := s.Recent(); len(rec) != 1 || rec[0].Status != statusCanceled {
		t.Fatalf("recent %+v", rec)
	}
	restingUntil.Lock()
	_, resting := restingUntil.m["a"]
	restingUntil.Unlock()
	if resting {
		t.Fatal("a member the agent left rests")
	}
}

// A member that answers after resting is no longer resting.
func TestSuccessLiftsRest(t *testing.T) {
	fresh(t)
	u := &scriptUp{answers: []scriptReply{{body: fromB}}}
	saveProviderOrFail(t, provider.Provider{ID: "solo", Name: "Solo", Key: "k", Models: []string{"m1"}, Chat: upstreamAt(t, u) + "/v1"})
	restingUntil.Lock()
	restingUntil.m["solo"] = time.Now().Add(time.Hour)
	restingUntil.note["solo"] = Rest{Why: failOther, Status: 503, By: "backoff"}
	restingUntil.Unlock()
	if code, body := serveTo(t, New(), "/v1/chat/completions", soloChat); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	restingUntil.Lock()
	_, resting := restingUntil.m["solo"]
	restingUntil.Unlock()
	if resting {
		t.Fatal("still resting after an answer")
	}
}

func TestRetryableRules(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{401, `invalid key`, true},
		{403, `forbidden`, true},
		{404, `not found`, true},
		{408, ``, true},
		{402, ``, true},
		{429, ``, true},
		{500, ``, true},
		{503, ``, true},
		{529, `overloaded`, true},
		{400, `模型不存在`, true},
		{400, `model gpt-x does not exist`, true},
		{400, `model m is not accessible via the /chat/completions endpoint`, true},
		{422, `insufficient balance`, true},
		{400, `messages: field required`, false},
		{400, `maximum context length exceeded`, false},
		{400, `上下文超长`, false},
		{400, `unsupported parameter: temperature`, false},
		{200, ``, false},
	} {
		if got := retryable(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("retryable(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

func TestRetryDelay(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) // HTTP dates have no sub-second part
	with := func(v string) http.Header {
		h := http.Header{}
		if v != "" {
			h.Set("Retry-After", v)
		}
		return h
	}
	for _, tc := range []struct {
		name   string
		status int
		h      http.Header
		n      int
		want   time.Duration
		ok     bool
	}{
		{"503 first wait", 503, with(""), 0, time.Second, true},
		{"503 second wait", 503, with(""), 1, 2 * time.Second, true},
		{"503 out of tries", 503, with(""), 2, 0, false},
		{"Retry-After counts", 503, with("8"), 0, 8 * time.Second, true},
		{"Retry-After over 8s is not waited", 503, with("30"), 0, 0, false},
		{"Retry-After as a date", 503, with(now.Add(3 * time.Second).Format(http.TimeFormat)), 0, 3 * time.Second, true},
		{"429 needs a Retry-After", 429, with(""), 0, 0, false},
		{"429 with a short Retry-After", 429, with("2"), 0, 2 * time.Second, true},
		{"400 is not retried", 400, with(""), 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := retryDelay(tc.status, tc.h, tc.n, now)
			if ok != tc.ok || (ok && d.Round(time.Second) != tc.want) {
				t.Fatalf("retryDelay = %v, %v; want %v, %v", d, ok, tc.want, tc.ok)
			}
		})
	}
}

// A reply is held back until its first content, unless it has been held too
// long or too far.
func TestGateHoldsUntilContentOrCap(t *testing.T) {
	rec := httptest.NewRecorder()
	g := newGate(rec)
	g.WriteHeader(200)
	g.Write([]byte("message start"))
	if rec.Body.Len() != 0 || g.full() {
		t.Fatalf("held reply went out: %q", rec.Body.String())
	}
	g.started = time.Now().Add(-streamHold - time.Second)
	if !g.full() {
		t.Fatal("a reply held past the time is not full")
	}
	g.settle()
	if rec.Body.String() != "message start" || rec.Code != 200 {
		t.Fatalf("settled: %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	g = newGate(rec)
	g.Write(make([]byte, streamHoldBytes+1))
	if !g.full() {
		t.Fatal("a reply held past the size is not full")
	}
}

// gpt-oss is another vendor's model, whatever its name shares with OpenAI's.
func TestGptOssIsAnotherVendorsModel(t *testing.T) {
	openai := provider.Provider{ID: "openai", Name: "OpenAI", Key: "k",
		Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1"}
	copilot := provider.Provider{ID: "copilot", Name: "Copilot", Key: "k",
		Chat: "https://copilot.test", Responses: "https://copilot.test", Anthropic: "https://copilot.test",
		Account: &provider.Account{Agent: "copilot"}}
	for _, tc := range []struct {
		name string
		p    provider.Provider
		want []provider.Protocol
	}{
		{"copilot serves gpt-oss chat first", copilot, []provider.Protocol{provider.Chat, provider.Responses, provider.Anthropic}},
		{"openai api serves gpt-oss chat first", openai, []provider.Protocol{provider.Chat, provider.Responses}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := orderOf(t, tc.p, "openai/gpt-oss-120b", provider.Gemini, true); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("order = %v, want %v", got, tc.want)
			}
		})
	}
}
