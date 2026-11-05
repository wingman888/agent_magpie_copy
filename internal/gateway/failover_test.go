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

// freshFailover resets what earlier tests may have left: rests, and the
// environment the provider files live in.
func freshFailover(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.note = map[string]Rest{}
	restingUntil.Unlock()
}

// saveAt serves h as one of p's APIs and saves p.
func saveAt(t *testing.T, p provider.Provider, h http.Handler, api string) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	p.Key = "k"
	switch api {
	case "chat":
		p.Chat = up.URL + "/v1"
	case "anthropic":
		p.Anthropic = up.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// step is one scripted reply: its status, the Retry-After it carries, its body.
type step struct {
	code       int
	retryAfter string
	body       string
}

// scripted answers one request per step, and repeats the last step after.
type scripted struct {
	mu    sync.Mutex
	steps []step
	got   int
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	s.mu.Lock()
	st := s.steps[min(s.got, len(s.steps)-1)]
	s.got++
	s.mu.Unlock()
	if st.retryAfter != "" {
		w.Header().Set("Retry-After", st.retryAfter)
	}
	if st.code == 200 {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(st.code)
	io.WriteString(w, st.body)
}

func (s *scripted) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got
}

// fastRetries shortens the waits for a test; the defaults stay as they are.
func fastRetries(t *testing.T) {
	t.Helper()
	old := retryDelays
	retryDelays = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}
	t.Cleanup(func() { retryDelays = old })
}

const (
	anthropicReq = `{"model":"plan/m1","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	soloChatReq  = `{"model":"solo/m1","messages":[{"role":"user","content":"hi"}]}`
	chatOKReply  = `data: {"id":"c1","model":"m2","choices":[{"delta":{"role":"assistant","content":"from spare"}}]}` + "\n\n" +
		`data: [DONE]` + "\n\n"
)

// Acceptance 5: a refusal that says the model is not there, or is closed to
// the key, moves the request on; a 400 about the request itself does not.
func TestRejectionsThatMoveOnAndThoseThatDoNot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  int
		body  string
		moves bool
	}{
		{"not found", 404, `{"error":{"message":"not found"}}`, true},
		{"unauthorized", 401, `{"error":{"message":"bad key"}}`, true},
		{"model does not exist", 400, `{"error":{"message":"The model m1 does not exist"}}`, true},
		{"model unavailable in Chinese", 400, `{"error":{"message":"模型不存在"}}`, true},
		{"not accessible via endpoint", 400, `{"error":{"message":"model m1 is not accessible via the /chat/completions endpoint"}}`, true},
		{"context too long", 400, `{"error":{"message":"maximum context length exceeded"}}`, false},
		{"missing field", 400, `{"error":{"message":"messages: field required"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			freshFailover(t)
			plan := &fake{t: t, ctype: "application/json", code: tc.code, reply: tc.body}
			spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
			saveAt(t, provider.Provider{ID: "plan", Name: "Plan", Models: []string{"m1"}, Fallback: []string{"spare/m2"}}, plan, "chat")
			saveAt(t, provider.Provider{ID: "spare", Name: "Spare", Models: []string{"m2"}}, spare, "chat")
			code, body := post(t, "/v1/chat/completions", chatReq)
			if tc.moves {
				if code != 200 || !strings.Contains(body, "from-spare") {
					t.Fatalf("moved: %d %s", code, body)
				}
			} else if code != tc.code || spare.got != nil {
				t.Fatalf("stayed: %d %s (spare got %s)", code, body, spare.got)
			}
		})
	}
}

// Acceptance 6: an Anthropic stream that fails before any content moves to the
// next candidate, and the agent sees only that candidate's reply.
func TestAnthropicStreamFailingBeforeContentMoves(t *testing.T) {
	freshFailover(t)
	plan := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_A","role":"assistant","content":[],"model":"m1","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: ping`+"\n"+`data: {"type":"ping"}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
	)}
	spare := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_B","role":"assistant","content":[],"model":"m2","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from spare"}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
	)}
	saveAt(t, provider.Provider{ID: "plan", Name: "Plan", Models: []string{"m1"}, Fallback: []string{"spare/m2"}}, plan, "anthropic")
	saveAt(t, provider.Provider{ID: "spare", Name: "Spare", Models: []string{"m2"}}, spare, "anthropic")
	s := New()
	code, body := postTo(s.Handler(), "/v1/messages", anthropicReq)
	if code != 200 || !strings.Contains(body, "from spare") || strings.Contains(body, "msg_A") || strings.Contains(body, "Overloaded") {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(body, "msg_B") {
		t.Fatalf("spare's stream not relayed: %s", body)
	}
	if !s.resting("plan") {
		t.Fatal("the overloaded provider should rest")
	}
}

// Once content has gone out, a stream error is the agent's, and nothing else is asked.
func TestStreamErrorAfterContentReachesTheAgent(t *testing.T) {
	freshFailover(t)
	plan := &fake{t: t, reply: sse(
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half"}}`,
		`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
	)}
	spare := &fake{t: t, reply: sse(`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"spare"}}`)}
	saveAt(t, provider.Provider{ID: "plan", Name: "Plan", Models: []string{"m1"}, Fallback: []string{"spare/m2"}}, plan, "anthropic")
	saveAt(t, provider.Provider{ID: "spare", Name: "Spare", Models: []string{"m2"}}, spare, "anthropic")
	code, body := postTo(New().Handler(), "/v1/messages", anthropicReq)
	if code != 200 || !strings.Contains(body, "half") || !strings.Contains(body, "Overloaded") || spare.got != nil {
		t.Fatalf("%d %s (spare got %s)", code, body, spare.got)
	}
}

// Acceptance 7: the only provider's transient failures are tried again.
func TestLastCandidateRetriesTransientFailures(t *testing.T) {
	freshFailover(t)
	fastRetries(t)
	up := &scripted{steps: []step{{503, "", `{"error":{"message":"busy"}}`}, {502, "", `{"error":{"message":"bad gateway"}}`}, {200, "", chatOKReply}}}
	saveAt(t, provider.Provider{ID: "solo", Name: "Solo", Models: []string{"m1"}}, up, "chat")
	s := New()
	code, body := postTo(s.Handler(), "/v1/chat/completions", soloChatReq)
	if code != 200 || !strings.Contains(body, "from spare") || up.count() != 3 {
		t.Fatalf("%d %s after %d requests", code, body, up.count())
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 3 || !r.Tries[1].Retry || r.Tries[1].WaitMs != 5 || !r.Tries[2].Retry || r.Tries[2].WaitMs != 10 {
		t.Fatalf("tries %+v", r.Tries)
	}
	if r.Tries[0].Fail != failRetry || r.Tries[1].Fail != failRetry || r.Tries[2].Rest != nil {
		t.Fatalf("tries %+v", r.Tries)
	}
}

func TestLastCandidateGivesUpAfterThreeTries(t *testing.T) {
	freshFailover(t)
	fastRetries(t)
	up := &scripted{steps: []step{{503, "", `{"error":{"message":"busy"}}`}}}
	saveAt(t, provider.Provider{ID: "solo", Name: "Solo", Models: []string{"m1"}}, up, "chat")
	code, body := postTo(New().Handler(), "/v1/chat/completions", soloChatReq)
	if code != 503 || !strings.Contains(body, "busy") || up.count() != 3 {
		t.Fatalf("%d %s after %d requests", code, body, up.count())
	}
}

func TestRetryAfterBeyondEightSecondsIsNotWaitedFor(t *testing.T) {
	freshFailover(t)
	fastRetries(t)
	up := &scripted{steps: []step{{429, "9", `{"error":{"message":"slow down"}}`}, {200, "", chatOKReply}}}
	saveAt(t, provider.Provider{ID: "solo", Name: "Solo", Models: []string{"m1"}}, up, "chat")
	code, body := postTo(New().Handler(), "/v1/chat/completions", soloChatReq)
	if code != 429 || up.count() != 1 {
		t.Fatalf("%d %s after %d requests", code, body, up.count())
	}
}

func TestRateLimitWithoutRetryAfterIsNotRetried(t *testing.T) {
	freshFailover(t)
	fastRetries(t)
	up := &scripted{steps: []step{{429, "", `{"error":{"message":"slow down"}}`}, {200, "", chatOKReply}}}
	saveAt(t, provider.Provider{ID: "solo", Name: "Solo", Models: []string{"m1"}}, up, "chat")
	code, _ := postTo(New().Handler(), "/v1/chat/completions", soloChatReq)
	if code != 429 || up.count() != 1 {
		t.Fatalf("%d after %d requests", code, up.count())
	}
}

func TestPlainBadRequestIsNotRetried(t *testing.T) {
	freshFailover(t)
	fastRetries(t)
	up := &scripted{steps: []step{{400, "", `{"error":{"message":"messages: field required"}}`}, {200, "", chatOKReply}}}
	saveAt(t, provider.Provider{ID: "solo", Name: "Solo", Models: []string{"m1"}}, up, "chat")
	code, _ := postTo(New().Handler(), "/v1/chat/completions", soloChatReq)
	if code != 400 || up.count() != 1 {
		t.Fatalf("%d after %d requests", code, up.count())
	}
}

// Acceptance 8: an agent that goes away while a candidate is still working
// blames no one, and no other candidate is asked.
func TestAgentCancelBlamesNoCandidate(t *testing.T) {
	freshFailover(t)
	hang := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		<-r.Context().Done()
	})
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	saveAt(t, provider.Provider{ID: "plan", Name: "Plan", Models: []string{"m1"}, Fallback: []string{"spare/m2"}}, hang, "chat")
	saveAt(t, provider.Provider{ID: "spare", Name: "Spare", Models: []string{"m2"}}, spare, "chat")
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)).WithContext(ctx)
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if spare.got != nil {
		t.Fatalf("spare was asked after the agent left: %s", spare.got)
	}
	if c := s.Recent()[0]; c.Status != statusClientClosed || c.Error != failCanceled {
		t.Fatalf("recorded %+v", c)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 1 || r.Tries[0].Fail != failCanceled || r.Tries[0].Rest != nil {
		t.Fatalf("tries %+v", r.Tries)
	}
	if s.resting("plan") {
		t.Fatal("a cancelled candidate must not rest")
	}
}
