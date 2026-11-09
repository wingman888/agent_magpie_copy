package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// s4Step is one scripted answer of a fake provider.
type s4Step struct {
	code   int // 0 is 200
	header map[string]string
	body   string
}

// s4Up answers its steps in order; the last one repeats. It counts requests.
type s4Up struct {
	mu    sync.Mutex
	steps []s4Step
	calls int
}

func (u *s4Up) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	u.mu.Lock()
	st := u.steps[min(u.calls, len(u.steps)-1)]
	u.calls++
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	for k, v := range st.header {
		w.Header().Set(k, v)
	}
	if st.code != 0 {
		w.WriteHeader(st.code)
	}
	io.WriteString(w, st.body)
}

func (u *s4Up) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

const s4OKBody = `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from %s"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`

func s4OK(who string) string { return fmt.Sprintf(s4OKBody, who) }

// s4Env gives a test its own config and cache, and forgets who is resting.
func s4Env(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
}

// s4Provider saves a Chat-only provider serving m1, answered by h.
func s4Provider(t *testing.T, id string, h http.Handler) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

// s4Group saves an ordered group over the given provider/model members.
func s4Group(t *testing.T, id string, members ...string) {
	t.Helper()
	if err := provider.SaveGroup(provider.Group{ID: id, Name: id, Members: members, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
}

func s4Post(s *Server, model string) (int, string) {
	rec := httptest.NewRecorder()
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

// s4Waits replaces the retry wait with one that only records what it was
// asked to wait.
func s4Waits(t *testing.T) *[]time.Duration {
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

// A group moves a request on from a member that refuses the model, or
// refuses the key, or does not know the route; B answers the agent.
func TestS4GroupMovesOnToTheNextMember(t *testing.T) {
	for i, tc := range []struct {
		name string
		code int
		body string
	}{
		{"not on this endpoint", 400, `{"error":{"message":"model m1 is not accessible via the /chat/completions endpoint"}}`},
		{"unauthorized", 401, `{"error":{"message":"invalid api key"}}`},
		{"not found", 404, `{"error":{"message":"no such route"}}`},
		{"no such model", 400, `{"error":{"message":"模型不存在"}}`},
		{"model not enabled", 400, `{"error":{"message":"The model does not exist or you do not have access to it"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s4Env(t)
			n := fmt.Sprint(i)
			a := &s4Up{steps: []s4Step{{code: tc.code, body: tc.body}}}
			b := &s4Up{steps: []s4Step{{body: s4OK("B")}}}
			s4Provider(t, "s4a"+n, a)
			s4Provider(t, "s4b"+n, b)
			s4Group(t, "s4g"+n, "s4a"+n+"/m1", "s4b"+n+"/m1")
			s := New()
			code, body := s4Post(s, "group/s4g"+n)
			if code != 200 || !strings.Contains(body, "from B") || strings.Contains(body, tc.body) {
				t.Fatalf("%d %s", code, body)
			}
			if a.count() != 1 || b.count() != 1 {
				t.Fatalf("a asked %d times, b %d", a.count(), b.count())
			}
			r := s.trace.routes[len(s.trace.routes)-1]
			if len(r.Tries) != 2 || r.Tries[0].Outcome != outcomeTransferred || r.Tries[0].Rest == nil || r.Tries[1].Outcome != "" {
				t.Fatalf("tries: %+v", r.Tries)
			}
		})
	}
}

// A failure that is the request's own fault goes to the agent as it is,
// and no other member is asked.
func TestS4GroupKeepsTheRequestsOwnErrors(t *testing.T) {
	for i, tc := range []struct{ msg, want string }{
		{`{"error":{"message":"messages: field required"}}`, "field required"},
		{`{"error":{"message":"上下文超长"}}`, "上下文超长"},
		{`{"error":{"message":"This model's maximum context length is 8192 tokens, however you requested 9000 tokens (context_length_exceeded)"}}`, "context_length_exceeded"},
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s4Env(t)
			n := fmt.Sprint(i)
			a := &s4Up{steps: []s4Step{{code: 400, body: tc.msg}}}
			b := &s4Up{steps: []s4Step{{body: s4OK("B")}}}
			s4Provider(t, "s4p"+n, a)
			s4Provider(t, "s4q"+n, b)
			s4Group(t, "s4h"+n, "s4p"+n+"/m1", "s4q"+n+"/m1")
			code, body := s4Post(New(), "group/s4h"+n)
			if code != 400 || !strings.Contains(body, tc.want) {
				t.Fatalf("%d %s", code, body)
			}
			if a.count() != 1 || b.count() != 0 {
				t.Fatalf("a asked %d times, b %d", a.count(), b.count())
			}
		})
	}
}

// A sole provider that fails in a way that mends is asked again, after
// waits of a second and then two, and the agent gets its answer.
func TestS4SoleProviderRetriesMendableFailures(t *testing.T) {
	s4Env(t)
	waits := s4Waits(t)
	up := &s4Up{steps: []s4Step{{code: 503, body: `{"error":{"message":"busy"}}`}, {code: 502, body: `{"error":{"message":"bad gateway"}}`}, {body: s4OK("solo")}}}
	s4Provider(t, "s4solo", up)
	s := New()
	code, body := s4Post(s, "s4solo/m1")
	if code != 200 || !strings.Contains(body, "from solo") {
		t.Fatalf("%d %s", code, body)
	}
	if up.count() != 3 {
		t.Fatalf("upstream asked %d times, want 3", up.count())
	}
	if fmt.Sprint(*waits) != "[1s 2s]" {
		t.Fatalf("waits %v", *waits)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 3 || r.Tries[0].Outcome != outcomeRetried || r.Tries[0].Wait != 1000 ||
		r.Tries[1].Outcome != outcomeRetried || r.Tries[1].Wait != 2000 || r.Tries[2].Status != 200 {
		t.Fatalf("tries: %+v", r.Tries)
	}
	if r.Tries[0].Rest != nil || r.Tries[1].Rest != nil {
		t.Fatal("a retried try rested")
	}
}

// A sole provider that keeps failing is asked three times, and the last
// error goes to the agent.
func TestS4SoleProviderGivesUpAfterTwoRetries(t *testing.T) {
	s4Env(t)
	waits := s4Waits(t)
	up := &s4Up{steps: []s4Step{{code: 503, body: `{"error":{"message":"still down"}}`}}}
	s4Provider(t, "s4down", up)
	code, body := s4Post(New(), "s4down/m1")
	if code != 503 || !strings.Contains(body, "still down") {
		t.Fatalf("%d %s", code, body)
	}
	if up.count() != 3 || len(*waits) != 2 {
		t.Fatalf("asked %d times, waited %v", up.count(), *waits)
	}
}

// Retry-After is waited as the vendor says; over eight seconds the error
// is delivered, and a 429 without it is not retried at all.
func TestS4RetryAfter(t *testing.T) {
	for _, c := range []struct {
		name      string
		steps     []s4Step
		code      int
		asked     int
		waits     string
		agentSees string
	}{
		{"short retry-after", []s4Step{{code: 503, header: map[string]string{"Retry-After": "3"}, body: `{"error":{"message":"busy"}}`}, {body: s4OK("solo")}}, 200, 2, "[3s]", "from solo"},
		{"long retry-after", []s4Step{{code: 503, header: map[string]string{"Retry-After": "9"}, body: `{"error":{"message":"busy"}}`}}, 503, 1, "[]", "busy"},
		{"429 without retry-after", []s4Step{{code: 429, body: `{"error":{"message":"slow down"}}`}}, 429, 1, "[]", "slow down"},
		{"429 with retry-after", []s4Step{{code: 429, header: map[string]string{"Retry-After": "2"}, body: `{"error":{"message":"slow down"}}`}, {body: s4OK("solo")}}, 200, 2, "[2s]", "from solo"},
		{"plain 400", []s4Step{{code: 400, body: `{"error":{"message":"messages: field required"}}`}}, 400, 1, "[]", "field required"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s4Env(t)
			waits := s4Waits(t)
			up := &s4Up{steps: c.steps}
			s4Provider(t, "s4ra", up)
			code, body := s4Post(New(), "s4ra/m1")
			if code != c.code || !strings.Contains(body, c.agentSees) {
				t.Fatalf("%d %s", code, body)
			}
			if up.count() != c.asked || fmt.Sprint(*waits) != c.waits {
				t.Fatalf("asked %d times, waited %v", up.count(), *waits)
			}
		})
	}
}

// Agent cancels while the first member hangs: the second is never asked,
// the request is recorded once as canceled, and nobody rests.
func TestS4AgentCancelRestsNobody(t *testing.T) {
	s4Env(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	a := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	b := &s4Up{steps: []s4Step{{body: s4OK("B")}}}
	s4Provider(t, "s4ca", a)
	s4Provider(t, "s4cb", b)
	s4Group(t, "s4cg", "s4ca/m1", "s4cb/m1")
	t.Cleanup(func() { close(release) })

	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"group/s4cg","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
	finished := make(chan struct{})
	go func() {
		s.Handler().ServeHTTP(rec, req)
		close(finished)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first member was never asked")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("gateway kept the canceled request going")
	}
	if b.count() != 0 {
		t.Fatalf("second member asked %d times after the agent left", b.count())
	}
	if _, resting := restOf("s4ca"); resting {
		t.Fatal("canceled member is resting")
	}
	if c := s.Recent()[0]; c.Status != statusCanceled || c.Error != "canceled" {
		t.Fatalf("recorded %+v", c)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 1 || r.Tries[0].Fail != "canceled" || r.Tries[0].Outcome != outcomeCanceled || r.Tries[0].Rest != nil {
		t.Fatalf("tries: %+v", r.Tries)
	}
}

// A member that answers again after resting is no longer resting.
func TestS4AnswerClearsResting(t *testing.T) {
	s4Env(t)
	up := &s4Up{steps: []s4Step{{body: s4OK("solo")}}}
	s4Provider(t, "s4clr", up)
	restingUntil.Lock()
	restingUntil.m["s4clr"] = time.Now().Add(time.Hour)
	restingUntil.note["s4clr"] = Rest{Why: failOther, Status: 500}
	restingUntil.Unlock()
	if code, body := s4Post(New(), "s4clr/m1"); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if _, resting := restOf("s4clr"); resting {
		t.Fatal("answering did not clear the resting")
	}
}
