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

// scripted answers each request with the next of its replies, the last
// one over and over.
type scripted struct {
	mu      sync.Mutex
	replies []reply
	n       int
}

type reply struct {
	code   int
	ctype  string
	body   string
	header map[string]string
	hang   bool // wait until the caller goes away
}

func (s *scripted) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	s.mu.Lock()
	rp := s.replies[min(s.n, len(s.replies)-1)]
	s.n++
	s.mu.Unlock()
	if rp.hang {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
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

func chatOK(text string) reply {
	return reply{body: `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"` + text + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`}
}

func chatErr(code int, msg string) reply {
	return reply{code: code, body: `{"error":{"message":"` + msg + `"}}`}
}

// pair saves providers a and b, each on its own fake, on the given
// protocol, and a group trying a then b.
func pair(t *testing.T, proto provider.Protocol, a, b http.Handler) {
	t.Helper()
	fresh(t)
	for _, x := range []struct {
		id string
		h  http.Handler
	}{{"a", a}, {"b", b}} {
		up := httptest.NewServer(x.h)
		t.Cleanup(up.Close)
		p := provider.Provider{ID: x.id, Name: strings.ToUpper(x.id), Key: "k" + x.id, Models: []string{"m"}}
		if proto == provider.Anthropic {
			p.Anthropic = up.URL
		} else {
			p.Chat = up.URL + "/v1"
		}
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
}

func noWait(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	old := retrySleep
	retrySleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	t.Cleanup(func() { retrySleep = old })
	return &waits
}

func lastRoute(s *Server) Route {
	s.trace.mu.Lock()
	defer s.trace.mu.Unlock()
	r := *s.trace.routes[len(s.trace.routes)-1]
	r.Tries = append([]Try(nil), r.Tries...)
	return r
}

const groupChat = `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`

func TestGroupFailsOverByError(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    reply
		over bool
	}{
		{"not on this endpoint", chatErr(400, "model gpt-x is not accessible via the /chat/completions endpoint"), true},
		{"unauthorized", chatErr(401, "bad key"), true},
		{"not found", chatErr(404, "no such thing"), true},
		{"forbidden", chatErr(403, "nope"), true},
		{"model missing, in Chinese", chatErr(400, "模型不存在"), true},
		{"model not supported", chatErr(400, "The model `m` does not exist or you do not have access to it."), true},
		{"422 no access", chatErr(422, "model m is not enabled for this account"), true},
		{"overloaded", chatErr(529, "Overloaded"), true},
		{"field required", chatErr(400, "messages: field required"), false},
		{"context too long", chatErr(400, "This model's maximum context length is 8192 tokens; context_length_exceeded"), false},
		{"context too long, in Chinese", chatErr(400, "上下文超长"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noWait(t)
			a, b := &scripted{replies: []reply{tc.a}}, &scripted{replies: []reply{chatOK("from-b")}}
			pair(t, provider.Chat, a, b)
			s := New()
			code, body := postAs(t, s, "", groupChat)
			if tc.over {
				if code != 200 || !strings.Contains(body, "from-b") || a.count() != 1 || b.count() != 1 {
					t.Fatalf("%d %s (a %d, b %d)", code, body, a.count(), b.count())
				}
				r := lastRoute(s)
				if len(r.Tries) != 2 || r.Tries[0].Then != thenFallback || r.Tries[0].Rest == nil {
					t.Fatalf("trace %+v", r.Tries)
				}
				return
			}
			if code != tc.a.code || b.count() != 0 || a.count() != 1 {
				t.Fatalf("%d %s (a %d, b %d)", code, body, a.count(), b.count())
			}
		})
	}
}

func TestRetryableWords(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, "messages: field required", false},
		{400, "prompt is too long: 300000 tokens > 200000 maximum", false},
		{400, "insufficient balance", true},
		{400, "model_not_found", true},
		{400, "该模型未开通", true},
		{400, "无权访问该模型", true},
		{400, "unsupported_api_for_model", true},
		{422, "rate limit reached", true},
		{422, "bad schema", false},
		{404, "", true},
		{408, "", true},
		{409, "", false},
		{500, "", true},
	} {
		if got := retryable(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%d %q: %v", c.status, c.body, got)
		}
	}
}

func anthropicStream(events ...string) reply {
	return reply{ctype: "text/event-stream", body: sse(events...)}
}

const (
	aStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_from_a\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}"
	aPing  = "event: ping\ndata: {\"type\":\"ping\"}"
	aOver  = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}"
)

func anthropicText(id, text string) reply {
	return anthropicStream(
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\""+id+"\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\""+text+"\"}}",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}")
}

func postMessages(t *testing.T, s *Server, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const groupMessages = `{"model":"group/g","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`

// An overload before any content is a failure like a 529: the next one
// answers, and the agent sees nothing of the first.
func TestStreamErrorBeforeContentFailsOver(t *testing.T) {
	noWait(t)
	a := &scripted{replies: []reply{anthropicStream(aStart, aPing, aOver)}}
	b := &scripted{replies: []reply{anthropicText("msg_from_b", "from-b")}}
	pair(t, provider.Anthropic, a, b)
	s := New()
	code, body := postMessages(t, s, groupMessages)
	if code != 200 || !strings.Contains(body, "from-b") || strings.Contains(body, "Overloaded") || strings.Contains(body, "msg_from_a") {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Count(body, "message_start") != 1 {
		t.Fatalf("two starts: %s", body)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Status != 529 || r.Tries[0].Rest == nil || r.Tries[0].Then != thenFallback {
		t.Fatalf("trace %+v", r.Tries)
	}
	if !s.resting("a") {
		t.Fatal("a is not resting")
	}

	// and translated for a Chat agent the same
	a2 := &scripted{replies: []reply{anthropicStream(aStart, aPing, aOver)}}
	b2 := &scripted{replies: []reply{anthropicText("msg_from_b", "from-b")}}
	pair(t, provider.Anthropic, a2, b2)
	code, body = postAs(t, New(), "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from-b") || strings.Contains(body, "Overloaded") || a2.count() != 1 || b2.count() != 1 {
		t.Fatalf("translated: %d %s", code, body)
	}
}

// An error after the content began goes to the agent as it came.
func TestStreamErrorAfterContentPassesThrough(t *testing.T) {
	noWait(t)
	a := &scripted{replies: []reply{anthropicStream(aStart,
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}",
		aOver)}}
	b := &scripted{replies: []reply{anthropicText("msg_from_b", "from-b")}}
	pair(t, provider.Anthropic, a, b)
	code, body := postMessages(t, New(), groupMessages)
	if code != 200 || !strings.Contains(body, "partial") || !strings.Contains(body, "Overloaded") || strings.Contains(body, "from-b") || b.count() != 0 {
		t.Fatalf("%d %s (b %d)", code, body, b.count())
	}
}

func TestStreamHoldLimits(t *testing.T) {
	oldB, oldT := streamHoldBytes, streamHoldTime
	t.Cleanup(func() { streamHoldBytes, streamHoldTime = oldB, oldT })

	streamHoldBytes = 16
	rec := httptest.NewRecorder()
	hw := newHoldWriter(rec, provider.Anthropic, true)
	hw.Header().Set("Content-Type", "text/event-stream")
	hw.WriteHeader(200)
	hw.Write([]byte(sse(aStart)))
	if !strings.Contains(rec.Body.String(), "msg_from_a") {
		t.Fatalf("held past the size limit: %q", rec.Body.String())
	}
	hw.settle()

	streamHoldBytes, streamHoldTime = 1<<20, 20*time.Millisecond
	rec = httptest.NewRecorder()
	hw = newHoldWriter(rec, provider.Anthropic, true)
	hw.Header().Set("Content-Type", "text/event-stream")
	hw.WriteHeader(200)
	hw.Write([]byte(sse(aPing)))
	time.Sleep(200 * time.Millisecond)
	hw.mu.Lock()
	got := rec.Body.String()
	hw.mu.Unlock()
	if !strings.Contains(got, "ping") {
		t.Fatalf("held past the time limit: %q", got)
	}
	hw.settle()
}

func solo(t *testing.T, h http.Handler) {
	t.Helper()
	fresh(t)
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "solo", Name: "Solo", Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

const soloChat = `{"model":"solo/m","messages":[{"role":"user","content":"hi"}]}`

func TestLastCandidateRetries(t *testing.T) {
	waits := noWait(t)
	up := &scripted{replies: []reply{chatErr(503, "busy"), chatErr(502, "bad gateway"), chatOK("at last")}}
	solo(t, up)
	s := New()
	code, body := postAs(t, s, "", soloChat)
	if code != 200 || !strings.Contains(body, "at last") || up.count() != 3 {
		t.Fatalf("%d %s after %d", code, body, up.count())
	}
	r := lastRoute(s)
	if len(r.Tries) != 3 || r.Tries[0].Then != thenRetry || r.Tries[0].Wait != 1000 || r.Tries[1].Wait != 2000 ||
		r.Tries[1].Then != thenRetry || r.Tries[2].Retry != 2 || r.Tries[2].Status != 200 || r.Tries[0].Rest != nil {
		t.Fatalf("trace %+v", r.Tries)
	}
	if len(*waits) != 2 || (*waits)[0] != time.Second || (*waits)[1] != 2*time.Second {
		t.Fatalf("waited %v", *waits)
	}
	if s.Recent()[0].Retries != 2 {
		t.Fatalf("call %+v", s.Recent()[0])
	}

	// still down: three tries, then its error
	up = &scripted{replies: []reply{chatErr(503, "still busy")}}
	solo(t, up)
	if code, body := postAs(t, New(), "", soloChat); code != 503 || !strings.Contains(body, "still busy") || up.count() != 3 {
		t.Fatalf("%d %s after %d", code, body, up.count())
	}

	for _, tc := range []struct {
		name  string
		r     reply
		tries int
		wait  time.Duration
	}{
		{"bad request", chatErr(400, "messages: field required"), 1, 0},
		{"rate limit, no word when", chatErr(429, "slow down"), 1, 0},
		{"rate limit, retry-after", reply{code: 429, body: `{"error":{"message":"slow down"}}`, header: map[string]string{"Retry-After": "3"}}, 3, 3 * time.Second},
		{"retry-after too long", reply{code: 503, body: `{"error":{"message":"busy"}}`, header: map[string]string{"Retry-After": "30"}}, 1, 0},
	} {
		*waits = nil
		up = &scripted{replies: []reply{tc.r}}
		solo(t, up)
		postAs(t, New(), "", soloChat)
		if up.count() != tc.tries || tc.wait != 0 && (len(*waits) == 0 || (*waits)[0] != tc.wait) {
			t.Fatalf("%s: %d tries, waited %v", tc.name, up.count(), *waits)
		}
	}
}

// A candidate resting after a failure that answers is resting no more.
func TestAnsweringEndsTheRest(t *testing.T) {
	noWait(t)
	up := &scripted{replies: []reply{chatErr(503, "busy"), chatOK("ok")}}
	solo(t, up)
	restingUntil.Lock()
	restingUntil.m["solo"] = time.Now().Add(time.Hour)
	restingUntil.Unlock()
	s := New()
	if code, _ := postAs(t, s, "", soloChat); code != 200 || s.resting("solo") {
		t.Fatalf("%d, resting %v", code, s.resting("solo"))
	}
}

// The agent going away mid-request is nobody's failure: nobody rests and
// nobody else is asked.
func TestCanceledRequest(t *testing.T) {
	noWait(t)
	a := &scripted{replies: []reply{{hang: true}}}
	b := &scripted{replies: []reply{chatOK("from-b")}}
	pair(t, provider.Chat, a, b)
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(groupChat)).WithContext(ctx))
	}()
	for deadline := time.Now().Add(5 * time.Second); a.count() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("a never asked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("still serving after the agent went away")
	}
	if b.count() != 0 {
		t.Fatal("b was asked")
	}
	r := lastRoute(s)
	if len(r.Tries) != 1 || r.Tries[0].Fail != failCanceled || r.Tries[0].Then != thenCanceled || r.Tries[0].Rest != nil || !r.Canceled || r.Status != 499 {
		t.Fatalf("trace %+v %+v", r, r.Tries)
	}
	if s.resting("a") {
		t.Fatal("a rests")
	}
	if c := s.Recent()[0]; c.Status != 499 || !c.Canceled {
		t.Fatalf("call %+v", c)
	}
}

// Canceled while waiting to retry: no more tries.
func TestCanceledWhileWaitingToRetry(t *testing.T) {
	up := &scripted{replies: []reply{chatErr(503, "busy")}}
	solo(t, up)
	ctx, cancel := context.WithCancel(context.Background())
	old := retrySleep
	retrySleep = func(c context.Context, d time.Duration) error { cancel(); return context.Canceled }
	t.Cleanup(func() { retrySleep = old })
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(soloChat)).WithContext(ctx))
	r := lastRoute(s)
	if up.count() != 1 || !r.Canceled || r.Tries[0].Then != thenCanceled || s.resting("solo") {
		t.Fatalf("%d tries, trace %+v", up.count(), r.Tries)
	}
}
