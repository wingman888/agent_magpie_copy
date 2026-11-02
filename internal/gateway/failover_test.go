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

// scripted answers its requests in turn from steps, the last one repeating.
type step struct {
	code  int
	ctype string
	head  map[string]string
	body  string
	hang  bool // wait for the agent to go away
}

type scripted struct {
	mu      sync.Mutex
	steps   []step
	n       int
	started chan struct{}
}

func (s *scripted) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	s.mu.Lock()
	st := s.steps[min(s.n, len(s.steps)-1)]
	s.n++
	s.mu.Unlock()
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
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

const okChat = `{"id":"ok","choices":[{"index":0,"message":{"role":"assistant","content":"from %s"},"finish_reason":"stop"}]}`

func okChatFrom(who string) string { return strings.Replace(okChat, "%s", who, 1) }

// upstream registers a provider "id" whose Chat and Anthropic APIs are h.
func upstream(t *testing.T, id string, h http.Handler) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k-" + id, Models: []string{"m"}, Chat: up.URL + "/v1", Anthropic: up.URL}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func orderedGroup(t *testing.T, members ...string) {
	t.Helper()
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: members, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
}

func quick(t *testing.T) {
	t.Helper()
	old := retryWaits
	retryWaits = []time.Duration{time.Millisecond, 2 * time.Millisecond}
	t.Cleanup(func() { retryWaits = old })
}

func lastRoute(s *Server) Route {
	s.trace.mu.Lock()
	defer s.trace.mu.Unlock()
	return *s.trace.routes[len(s.trace.routes)-1]
}

const groupChat = `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`

func TestGroupFailsOverOnTheseErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		over bool
	}{
		{"endpoint", 400, `{"error":{"message":"model m is not accessible via the /chat/completions endpoint"}}`, true},
		{"unauthorized", 401, `{"error":{"message":"bad key"}}`, true},
		{"not found", 404, `{"error":{"message":"nope"}}`, true},
		{"chinese model", 400, `{"error":{"message":"模型不存在"}}`, true},
		{"quota in 400", 400, `{"error":{"message":"insufficient balance"}}`, true},
		{"unprocessable no access", 422, `{"error":{"message":"you do not have access to this model"}}`, true},
		{"field required", 400, `{"error":{"message":"messages: field required"}}`, false},
		{"context", 400, `{"error":{"message":"maximum context length exceeded"}}`, false},
		{"chinese context", 400, `{"error":{"message":"上下文超长"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			quick(t)
			a := &scripted{steps: []step{{code: tc.code, body: tc.body}}}
			b := &scripted{steps: []step{{body: okChatFrom("b")}}}
			upstream(t, "a", a)
			upstream(t, "b", b)
			orderedGroup(t, "a/m", "b/m")
			s := New()
			code, body := postAs(t, s, "", groupChat)
			if tc.over {
				if code != 200 || !strings.Contains(body, "from b") || a.count() < 1 {
					t.Fatalf("%d %s", code, body)
				}
				tries := lastRoute(s).Tries
				if len(tries) != 2 || tries[0].Action != actionFailover || tries[0].Rest == nil {
					t.Fatalf("tries %+v", tries)
				}
			} else if code != 400 || b.count() != 0 {
				t.Fatalf("%d %s (b got %d)", code, body, b.count())
			}
		})
	}
}

func sseJoin(events ...string) string { return strings.Join(events, "") }

func ev(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }

const aStart = `{"type":"message_start","message":{"id":"msg_from_a","type":"message","role":"assistant","content":[],"model":"m","usage":{"input_tokens":1,"output_tokens":0}}}`
const bStart = `{"type":"message_start","message":{"id":"msg_from_b","type":"message","role":"assistant","content":[],"model":"m","usage":{"input_tokens":1,"output_tokens":0}}}`

func bStream() string {
	return sseJoin(ev("message_start", bStart), ev("ping", `{"type":"ping"}`),
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello from b"}}`),
		ev("content_block_stop", `{"type":"content_block_stop","index":0}`),
		ev("message_stop", `{"type":"message_stop"}`))
}

func anthropicPost(s *Server, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const groupMsg = `{"model":"group/mine","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`

func TestStreamErrorBeforeContentFailsOver(t *testing.T) {
	fresh(t)
	quick(t)
	a := &scripted{steps: []step{{ctype: "text/event-stream", body: sseJoin(ev("message_start", aStart), ev("ping", `{"type":"ping"}`),
		ev("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))}}}
	b := &scripted{steps: []step{{ctype: "text/event-stream", body: bStream()}}}
	upstream(t, "a", a)
	upstream(t, "b", b)
	orderedGroup(t, "a/m", "b/m")
	s := New()
	code, body := anthropicPost(s, groupMsg)
	if code != 200 || !strings.Contains(body, "hello from b") || strings.Contains(body, "Overloaded") || strings.Contains(body, "msg_from_a") {
		t.Fatalf("%d %s", code, body)
	}
	tries := lastRoute(s).Tries
	if len(tries) != 2 || tries[0].Status != 529 || tries[0].Rest == nil || tries[0].Action != actionFailover {
		t.Fatalf("tries %+v", tries)
	}
	if !restingNow("a") {
		t.Fatal("a does not rest")
	}
	if restingNow("b") {
		t.Fatal("b rests")
	}
}

func restingNow(id string) bool { _, ok := restOf(id); return ok }

func TestStreamErrorAfterContentIsDelivered(t *testing.T) {
	fresh(t)
	quick(t)
	a := &scripted{steps: []step{{ctype: "text/event-stream", body: sseJoin(ev("message_start", aStart),
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`),
		ev("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))}}}
	b := &scripted{steps: []step{{ctype: "text/event-stream", body: bStream()}}}
	upstream(t, "a", a)
	upstream(t, "b", b)
	orderedGroup(t, "a/m", "b/m")
	code, body := anthropicPost(New(), groupMsg)
	if code != 200 || !strings.Contains(body, "partial") || !strings.Contains(body, "Overloaded") || b.count() != 0 {
		t.Fatalf("%d %s (b got %d)", code, body, b.count())
	}
}

func TestStreamHeldOnlyUpToTheLimit(t *testing.T) {
	fresh(t)
	quick(t)
	old := firstContentMax
	firstContentMax = 10
	t.Cleanup(func() { firstContentMax = old })
	a := &scripted{steps: []step{{ctype: "text/event-stream", body: sseJoin(ev("message_start", aStart),
		ev("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))}}}
	b := &scripted{steps: []step{{ctype: "text/event-stream", body: bStream()}}}
	upstream(t, "a", a)
	upstream(t, "b", b)
	orderedGroup(t, "a/m", "b/m")
	code, body := anthropicPost(New(), groupMsg)
	if code != 200 || !strings.Contains(body, "msg_from_a") || !strings.Contains(body, "Overloaded") || b.count() != 0 {
		t.Fatalf("%d %s (b got %d)", code, body, b.count())
	}
}

func TestChatStreamErrorChunkFailsOver(t *testing.T) {
	fresh(t)
	quick(t)
	a := &scripted{steps: []step{{ctype: "text/event-stream", body: `data: {"id":"a1","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}` + "\n\n" +
		`data: {"error":{"message":"server busy","type":"server_error"}}` + "\n\n"}}}
	b := &scripted{steps: []step{{ctype: "text/event-stream", body: `data: {"id":"b1","choices":[{"index":0,"delta":{"content":"hi from b"}}]}` + "\n\ndata: [DONE]\n\n"}}}
	upstream(t, "a", a)
	upstream(t, "b", b)
	orderedGroup(t, "a/m", "b/m")
	code, body := postAs(t, New(), "", `{"model":"group/mine","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hi from b") || strings.Contains(body, "a1") || strings.Contains(body, "server busy") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestLastCandidateIsRetried(t *testing.T) {
	fresh(t)
	quick(t)
	a := &scripted{steps: []step{{code: 503, body: `{"error":{"message":"down"}}`}, {code: 502, body: `{"error":{"message":"bad gw"}}`}, {body: okChatFrom("a")}}}
	upstream(t, "a", a)
	s := New()
	code, body := postAs(t, s, "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from a") || a.count() != 3 {
		t.Fatalf("%d %s (%d requests)", code, body, a.count())
	}
	tries := lastRoute(s).Tries
	if len(tries) != 3 || tries[0].Action != actionRetry || tries[0].WaitMs != 1 || tries[1].Action != actionRetry || tries[1].WaitMs != 2 || tries[2].Status != 200 {
		t.Fatalf("tries %+v", tries)
	}
	if restingNow("a") {
		t.Fatal("a rests")
	}
}

func TestRetryDefaults(t *testing.T) {
	if len(retryWaits) != 2 || retryWaits[0] != time.Second || retryWaits[1] != 2*time.Second {
		t.Fatalf("waits %v", retryWaits)
	}
}

func TestRetryGivesUp(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   step
		want int
	}{
		{"503 stays", step{code: 503, body: `{"error":{"message":"down"}}`}, 3},
		{"bad request", step{code: 400, body: `{"error":{"message":"bad param"}}`}, 1},
		{"429 without a time", step{code: 429, body: `{"error":{"message":"slow"}}`}, 1},
		{"429 with a time", step{code: 429, head: map[string]string{"Retry-After": "0"}, body: `{"error":{"message":"slow"}}`}, 3},
		{"too long a wait", step{code: 503, head: map[string]string{"Retry-After": "9"}, body: `{"error":{"message":"down"}}`}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			quick(t)
			a := &scripted{steps: []step{tc.st}}
			upstream(t, "a", a)
			code, _ := postAs(t, New(), "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)
			if code != tc.st.code || a.count() != tc.want {
				t.Fatalf("%d after %d requests, want %d", code, a.count(), tc.want)
			}
		})
	}
}

func TestSuccessEndsTheRest(t *testing.T) {
	fresh(t)
	quick(t)
	a := &scripted{steps: []step{{body: okChatFrom("a")}}}
	upstream(t, "a", a)
	s := New()
	s.restAfter(candidate{p: provider.Provider{ID: "a"}, model: "m", rest: "a"}, 500, http.Header{}, nil)
	if !restingNow("a") {
		t.Fatal("not resting")
	}
	if code, _ := postAs(t, s, "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatal(code)
	}
	if restingNow("a") {
		t.Fatal("still resting")
	}
}

func TestAgentCancelPenalizesNobody(t *testing.T) {
	fresh(t)
	quick(t)
	a := &scripted{steps: []step{{hang: true}}, started: make(chan struct{}, 1)}
	b := &scripted{steps: []step{{body: okChatFrom("b")}}}
	upstream(t, "a", a)
	upstream(t, "b", b)
	orderedGroup(t, "a/m", "b/m")
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-a.started
		cancel()
	}()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(groupChat)).WithContext(ctx)
	s.Handler().ServeHTTP(rec, req)
	if b.count() != 0 {
		t.Fatal("b was asked")
	}
	r := lastRoute(s)
	if len(r.Tries) != 1 || r.Tries[0].Fail != failCanceled || r.Tries[0].Status != 499 || r.Tries[0].Rest != nil || r.Tries[0].Action != actionCanceled || r.Status != 499 {
		t.Fatalf("route %+v", r)
	}
	if restingNow("a") || restingNow("b") {
		t.Fatal("someone rests")
	}
	if c := s.Recent()[0]; c.Status != 499 {
		t.Fatalf("call %+v", c)
	}
}

func TestClassifyEvent(t *testing.T) {
	for _, tc := range []struct {
		event, data string
		want        streamStep
	}{
		{"message_start", aStart, stepNeutral},
		{"ping", `{"type":"ping"}`, stepNeutral},
		{"error", `{"type":"error","error":{"type":"overloaded_error"}}`, stepError},
		{"", `{"error":{"message":"x"}}`, stepError},
		{"response.failed", `{"type":"response.failed","response":{"error":{"code":"server_error"}}}`, stepError},
		{"error", `{"type":"error","code":"x","message":"y"}`, stepError},
		{"response.created", `{"type":"response.created","response":{}}`, stepNeutral},
		{"response.output_text.delta", `{"type":"response.output_text.delta","delta":"x"}`, stepContent},
		{"", `{"choices":[{"delta":{"role":"assistant","content":""}}]}`, stepNeutral},
		{"", `{"choices":[{"delta":{"content":"x"}}]}`, stepContent},
		{"", `[DONE]`, stepContent},
		{"content_block_start", `{"type":"content_block_start"}`, stepContent},
	} {
		if got := classifyEvent(tc.event, tc.data); got != tc.want {
			t.Errorf("%s %s: %v, want %v", tc.event, tc.data, got, tc.want)
		}
	}
}
