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

// reply is one answer a scripted upstream gives.
type reply struct {
	code   int
	retry  string // Retry-After, when set
	stream bool   // text/event-stream, else JSON
	body   string
	hang   bool // never answers: waits until the gateway gives up
}

// scripted answers each request with the next of its replies; the last one
// repeats. It counts what it was asked.
type scripted struct {
	mu      sync.Mutex
	replies []reply
	hits    int
}

func (f *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	f.mu.Lock()
	rp := f.replies[min(f.hits, len(f.replies)-1)]
	f.hits++
	f.mu.Unlock()
	if rp.hang {
		<-r.Context().Done()
		return
	}
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

func (f *scripted) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits
}

// serveAnth is serveOn for a provider speaking Anthropic.
func serveAnth(t *testing.T, id, key string, models []string, v http.Handler) {
	t.Helper()
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: strings.ToUpper(id), Key: key, Models: models, Anthropic: up.URL}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// group makes the routing group the tests send to, in order.
func group(t *testing.T, members ...string) {
	t.Helper()
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: members, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
}

// serveReq sends one request through s; ctx, when set, is the agent's.
func serveReq(t *testing.T, s *Server, ctx context.Context, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

const (
	chatGroup = `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`
	anthGroup = `{"model":"group/mine","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
)

func chatAnswer(text string) string {
	return `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"` + text + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
}

func errJSON(msg string) string {
	return `{"error":{"message":"` + msg + `"}}`
}

// Acceptance 5: a refusal of the model or the account moves the request to
// the next member of the group; a complaint about the request itself does not.
func TestGroupMovesOnRefusalButNotOnBadRequest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		a     reply
		moves bool
		msg   string
	}{
		{"not on this endpoint", reply{code: 400, body: errJSON("model m not accessible via the /chat/completions endpoint")}, true, ""},
		{"unauthorized", reply{code: 401, body: errJSON("bad key")}, true, ""},
		{"not found", reply{code: 404, body: errJSON("no such route")}, true, ""},
		{"model missing, in Chinese", reply{code: 400, body: errJSON("模型不存在")}, true, ""},
		{"field missing", reply{code: 400, body: errJSON("messages: field required")}, false, "field required"},
		{"context too long", reply{code: 400, body: errJSON("上下文超长")}, false, "上下文超长"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []reply{tc.a}}
			b := &scripted{replies: []reply{{body: chatAnswer("from b")}}}
			serveOn(t, "a", "ka", []string{"m"}, a)
			serveOn(t, "b", "kb", []string{"m"}, b)
			group(t, "a/m", "b/m")
			s := New()
			code, body := serveReq(t, s, nil, "/v1/chat/completions", chatGroup)
			if tc.moves {
				if code != 200 || !strings.Contains(body, "from b") || b.count() != 1 {
					t.Fatalf("moved: %d %s (b asked %d)", code, body, b.count())
				}
				return
			}
			if code != 400 || !strings.Contains(body, tc.msg) || b.count() != 0 {
				t.Fatalf("not moved: %d %s (b asked %d)", code, body, b.count())
			}
		})
	}
}

// Acceptance 6: an Anthropic stream that fails before any text is replaced
// wholesale by the next member; the agent never sees the failed one's start.
func TestStreamErrorBeforeContentMovesOn(t *testing.T) {
	fresh(t)
	start := `event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_a","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`
	ping := `event: ping` + "\n" + `data: {"type":"ping"}`
	overloaded := `event: error` + "\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	a := &scripted{replies: []reply{{stream: true, body: sse(start, ping, overloaded)}}}
	b := &scripted{replies: []reply{{stream: true, body: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_b","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from b"}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}}}
	serveAnth(t, "a", "ka", []string{"m"}, a)
	serveAnth(t, "b", "kb", []string{"m"}, b)
	group(t, "a/m", "b/m")
	s := New()
	code, body := serveReq(t, s, nil, "/v1/messages", anthGroup)
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "Overloaded") || strings.Contains(body, "msg_a") {
		t.Fatalf("%d %s", code, body)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Status != 529 || r.Tries[0].Rest == nil || r.Tries[0].Kind != "moved" {
		t.Fatalf("trace: %+v", r.Tries)
	}
	if _, ok := restOf("a"); !ok {
		t.Fatal("the failed member is not resting")
	}
}

// Once text has reached the agent, a stream error is the agent's to see, and
// nothing else is tried.
func TestStreamErrorAfterContentIsNotMoved(t *testing.T) {
	fresh(t)
	overloaded := `event: error` + "\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	a := &scripted{replies: []reply{{stream: true, body: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_a","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
		overloaded)}}}
	b := &scripted{replies: []reply{{body: chatAnswer("from b")}}}
	serveAnth(t, "a", "ka", []string{"m"}, a)
	serveAnth(t, "b", "kb", []string{"m"}, b)
	group(t, "a/m", "b/m")
	s := New()
	code, body := serveReq(t, s, nil, "/v1/messages", anthGroup)
	if code != 200 || !strings.Contains(body, "partial") || !strings.Contains(body, "Overloaded") || b.count() != 0 {
		t.Fatalf("%d %s (b asked %d)", code, body, b.count())
	}
}

// Acceptance 7: the last candidate retries a transient failure twice, waiting
// a second and then two, and delivers the last error when they all fail.
func TestLastCandidateRetriesTransientFailures(t *testing.T) {
	fresh(t)
	sc := &scripted{replies: []reply{{code: 503, body: errJSON("busy")}, {code: 502, body: errJSON("bad gateway")}, {body: chatAnswer("from solo")}}}
	serveOn(t, "solo", "k", []string{"m"}, sc)
	s := New()
	code, body := serveReq(t, s, nil, "/v1/chat/completions", `{"model":"solo/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from solo") || sc.count() != 3 {
		t.Fatalf("%d %s (asked %d)", code, body, sc.count())
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 3 || r.Tries[0].Kind != "retry" || r.Tries[0].Wait != 1000 || r.Tries[1].Kind != "retry" || r.Tries[1].Wait != 2000 || r.Tries[2].Status != 200 {
		t.Fatalf("trace: %+v", r.Tries)
	}
	if _, ok := restOf("solo"); ok {
		t.Fatal("a provider that answered after a retry is resting")
	}

	fresh(t)
	down := &scripted{replies: []reply{{code: 503, body: errJSON("down")}}}
	serveOn(t, "solo", "k", []string{"m"}, down)
	s = New()
	code, body = serveReq(t, s, nil, "/v1/chat/completions", `{"model":"solo/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 503 || !strings.Contains(body, "down") || down.count() != 3 {
		t.Fatalf("persistent: %d %s (asked %d)", code, body, down.count())
	}

	fresh(t)
	bad := &scripted{replies: []reply{{code: 400, body: errJSON("messages: field required")}}}
	serveOn(t, "solo", "k", []string{"m"}, bad)
	s = New()
	code, body = serveReq(t, s, nil, "/v1/chat/completions", `{"model":"solo/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || bad.count() != 1 {
		t.Fatalf("bad request: %d %s (asked %d)", code, body, bad.count())
	}

	fresh(t)
	limited := &scripted{replies: []reply{{code: 429, body: errJSON("slow down")}}}
	serveOn(t, "solo", "k", []string{"m"}, limited)
	s = New()
	code, _ = serveReq(t, s, nil, "/v1/chat/completions", `{"model":"solo/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 429 || limited.count() != 1 {
		t.Fatalf("429 without Retry-After: %d (asked %d)", code, limited.count())
	}
}

// Acceptance 8: an agent that goes away mid-request is no fault of the
// provider it was waiting on: the next member is not asked, and nothing rests.
func TestAgentCancelMovesAndRestsNothing(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{hang: true}}}
	b := &scripted{replies: []reply{{body: chatAnswer("from b")}}}
	serveOn(t, "a", "ka", []string{"m"}, a)
	serveOn(t, "b", "kb", []string{"m"}, b)
	group(t, "a/m", "b/m")
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	serveReq(t, s, ctx, "/v1/chat/completions", chatGroup)
	if b.count() != 0 {
		t.Fatalf("the next member was asked %d times", b.count())
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 1 || r.Tries[0].Fail != "canceled" || r.Tries[0].Kind != "canceled" || r.Tries[0].Rest != nil || r.Status != 499 || r.Error != "canceled" {
		t.Fatalf("trace: status %d, tries %+v", r.Status, r.Tries)
	}
	if c := s.Recent()[0]; c.Status != 499 || c.Error != "canceled" || c.Fallback != "" {
		t.Fatalf("recorded %+v", c)
	}
	if s.resting("a") {
		t.Fatal("the member the agent left is resting")
	}
}
