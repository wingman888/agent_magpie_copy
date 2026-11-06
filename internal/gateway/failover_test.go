package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// anthropicOn serves model claude-x over Anthropic's API only, from v.
func anthropicOn(t *testing.T, id string, v http.Handler) {
	t.Helper()
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k", Models: []string{"claude-x"}, Anthropic: up.URL}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// saveGroup makes the routing group "mine": members tried in order.
func saveGroup(t *testing.T, members ...string) {
	t.Helper()
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: members, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
}

// recordWaits replaces the wait before a retry with one that only records it.
func recordWaits(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	old := waitRetry
	waitRetry = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	t.Cleanup(func() { waitRetry = old })
	return &waits
}

const groupChat = `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`

// (5) A group member that turns the model away hands the request to the next
// member; errors that are the request's own fault reach the agent.
func TestGroupMovesOnFromAMemberThatTurnsItDown(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  int
		msg   string
		moves bool
	}{
		{"not accessible on chat", 400, "model m is not accessible via the /chat/completions endpoint", true},
		{"unauthorised", 401, "invalid api key", true},
		{"not found", 404, "not found", true},
		{"model does not exist, in Chinese", 400, "模型不存在", true},
		{"no quota, as a 400", 400, "您的套餐额度已用完", true},
		{"messages field required", 400, "messages: field required", false},
		{"context too long", 400, "This model's maximum context length is 8192 tokens", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			a := &fake{t: t, ctype: "application/json", code: tc.code, reply: `{"error":{"message":"` + tc.msg + `"}}`}
			b := &fake{t: t, reply: chatOK}
			serveOn(t, "a", "ka", []string{"m"}, a)
			serveOn(t, "b", "kb", []string{"m"}, b)
			saveGroup(t, "a/m", "b/m")
			code, body := postAs(t, New(), "", groupChat)
			if tc.moves {
				if code != 200 || !strings.Contains(body, "chat ok") || b.got == nil {
					t.Fatalf("%d %s (b got %s)", code, body, b.got)
				}
				return
			}
			if code != tc.code || !strings.Contains(body, tc.msg) || b.got != nil {
				t.Fatalf("%d %s (b got %s)", code, body, b.got)
			}
		})
	}
}

// (6) An Anthropic stream that fails with overloaded after message_start and
// a ping, before any content, moves on: the agent sees only the next
// member's reply. Once content has gone out, the error is the agent's.
func TestAnthropicStreamMovesOnBeforeAnyContent(t *testing.T) {
	opening := []string{
		`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_A","type":"message","role":"assistant","model":"claude-x","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: ping` + "\n" + `data: {"type":"ping"}`,
	}
	overloaded := `event: error` + "\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	bReply := sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_B","type":"message","role":"assistant","model":"claude-x","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from B"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
	const anthropicGroup = `{"model":"group/mine","max_tokens":9,"stream":true,"messages":[{"role":"user","content":"hi"}]}`

	t.Run("error before content moves on", func(t *testing.T) {
		fresh(t)
		a := &fake{t: t, reply: sse(append(opening, overloaded)...)}
		b := &fake{t: t, reply: bReply}
		anthropicOn(t, "a", a)
		anthropicOn(t, "b", b)
		saveGroup(t, "a/claude-x", "b/claude-x")
		s := New()
		code, body := postAs(t, s, "", anthropicGroup)
		if code != 200 || !strings.Contains(body, "from B") || strings.Contains(body, "Overloaded") || strings.Contains(body, "msg_A") {
			t.Fatalf("%d %s", code, body)
		}
		rs := s.Trace(context.Background(), 0, 0).Routes
		if len(rs) != 1 || len(rs[0].Tries) != 2 {
			t.Fatalf("routes %+v", rs)
		}
		if first := rs[0].Tries[0]; first.Status != 529 || first.Rest == nil || first.Rest.Status != 529 {
			t.Fatalf("A's try %+v", first)
		}
	})

	t.Run("error after content is the agent's", func(t *testing.T) {
		fresh(t)
		partial := `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`
		a := &fake{t: t, reply: sse(append(append(opening, partial), overloaded)...)}
		b := &fake{t: t, reply: bReply}
		anthropicOn(t, "a", a)
		anthropicOn(t, "b", b)
		saveGroup(t, "a/claude-x", "b/claude-x")
		code, body := postAs(t, New(), "", anthropicGroup)
		if code != 200 || !strings.Contains(body, "partial") || !strings.Contains(body, "Overloaded") {
			t.Fatalf("%d %s", code, body)
		}
		if b.got != nil {
			t.Fatal("the next member was asked after content had gone out")
		}
	})
}

// seqReply is one answer of an upstream that answers in turn.
type seqReply struct {
	code       int
	retryAfter string
	body       string
}

// seqUp answers its requests in turn; past the last reply it repeats it.
type seqUp struct {
	replies []seqReply
	calls   int
}

func (u *seqUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	i := u.calls
	if i >= len(u.replies) {
		i = len(u.replies) - 1
	}
	u.calls++
	rep := u.replies[i]
	if rep.retryAfter != "" {
		w.Header().Set("Retry-After", rep.retryAfter)
	}
	if rep.code == 200 {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, chatOK)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rep.code)
	io.WriteString(w, rep.body)
}

// (7) The only provider's transient failures are retried, the last try's
// error goes to the agent, and a non-transient 400 is tried once.
func TestLastCandidateRetriesTransientFailures(t *testing.T) {
	err503 := seqReply{503, "", `{"error":{"message":"busy"}}`}
	err502 := seqReply{502, "", `{"error":{"message":"bad gateway"}}`}
	ok := seqReply{200, "", ""}
	for _, tc := range []struct {
		name      string
		replies   []seqReply
		wantCode  int
		wantCalls int
		wantWaits []time.Duration
	}{
		{"503, 502, then 200", []seqReply{err503, err502, ok}, 200, 3, []time.Duration{time.Second, 2 * time.Second}},
		{"503 all the way", []seqReply{err503}, 503, 3, []time.Duration{time.Second, 2 * time.Second}},
		{"400 is not retried", []seqReply{{400, "", `{"error":{"message":"messages: field required"}}`}}, 400, 1, nil},
		{"503 with Retry-After is waited for", []seqReply{{503, "3", `{"error":{"message":"busy"}}`}, ok}, 200, 2, []time.Duration{3 * time.Second}},
		{"503 with a long Retry-After is not", []seqReply{{503, "60", `{"error":{"message":"busy"}}`}, ok}, 503, 1, nil},
		{"429 without Retry-After is not retried", []seqReply{{429, "", `{"error":{"message":"slow down"}}`}, ok}, 429, 1, nil},
		{"429 with Retry-After is", []seqReply{{429, "2", `{"error":{"message":"slow down"}}`}, ok}, 200, 2, []time.Duration{2 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			waits := recordWaits(t)
			up := &seqUp{replies: tc.replies}
			serveOn(t, "a", "ka", []string{"m"}, up)
			s := New()
			code, body := postAs(t, s, "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)
			if code != tc.wantCode || up.calls != tc.wantCalls {
				t.Fatalf("status %d (want %d), %d calls (want %d): %s", code, tc.wantCode, up.calls, tc.wantCalls, body)
			}
			if strings.Join(durations(*waits), ",") != strings.Join(durations(tc.wantWaits), ",") {
				t.Fatalf("waits %v, want %v", *waits, tc.wantWaits)
			}
			rs := s.Trace(context.Background(), 0, 0).Routes
			if len(rs) != 1 || len(rs[0].Tries) != tc.wantCalls {
				t.Fatalf("routes %+v", rs)
			}
			for i, try := range rs[0].Tries {
				retried := try.Retry != nil
				if retried != (i < len(tc.wantWaits)) {
					t.Fatalf("try %d retry %+v", i, try.Retry)
				}
				if retried && try.Retry.Wait != tc.wantWaits[i].Milliseconds() {
					t.Fatalf("try %d waited %dms", i, try.Retry.Wait)
				}
				if try.Rest != nil {
					t.Fatalf("try %d rests, though it is the only candidate", i)
				}
			}
		})
	}
}

func durations(ds []time.Duration) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.String()
	}
	return out
}

// (8) An agent that goes away while a member is still answering: no other
// member is asked, the call is recorded once as canceled, and nobody rests.
func TestAgentCancelBlamesNoProvider(t *testing.T) {
	fresh(t)
	hit := make(chan struct{}, 1)
	a := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		hit <- struct{}{}
		<-r.Context().Done() // hangs until the gateway lets go
	})
	b := &fake{t: t, reply: chatOK}
	serveOn(t, "a", "ka", []string{"m"}, a)
	serveOn(t, "b", "kb", []string{"m"}, b)
	saveGroup(t, "a/m", "b/m")

	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(groupChat)).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.Handler().ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-hit:
	case <-time.After(10 * time.Second):
		t.Fatal("member a was never asked")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the gateway kept waiting after the agent left")
	}
	if b.got != nil {
		t.Fatal("the next member was asked after the agent left")
	}
	rs := s.Trace(context.Background(), 0, 0).Routes
	if len(rs) != 1 || len(rs[0].Tries) != 1 {
		t.Fatalf("routes %+v", rs)
	}
	r := rs[0]
	if r.Status != 499 || r.Error != failCanceled || r.Tries[0].Fail != failCanceled || r.Tries[0].Rest != nil {
		t.Fatalf("route %+v", r)
	}
	if _, resting := restOf("a"); resting {
		t.Fatal("a rests after the agent left")
	}
}

// Which failures move a request on, and which are the request's own fault.
func TestTransferableFailures(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{401, "bad key", true},
		{403, "forbidden", true},
		{404, "no such route", true},
		{408, "", true},
		{422, "模型不存在", true},
		{400, "model gpt-x is not found", true},
		{400, "Unsupported model for this account", true},
		{400, "model m is not accessible via the /chat/completions endpoint", true},
		{400, "This model's maximum context length is 8192 tokens. context_length_exceeded", false},
		{400, "messages: field required", false},
		{400, "请求参数错误", false},
		{413, "too big", false},
		{200, "", false},
	} {
		if got := retryable(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("retryable(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
}

// count_tokens goes to the provider only where its list declares Anthropic's API
// for the model; an unknown list is not a declaration.
func TestCountTokensNeedsADeclaredAnthropicAPI(t *testing.T) {
	f := &scripted{replies: map[string]reply{
		pCount: {200, "application/json", `{"input_tokens":42}`},
	}}
	endpointSetup(t, f, []string{"claude-z"}, nil, true, false, true)
	code, _ := callOn(New().Handler(), "/v1/messages/count_tokens", `{"model":"claude-z","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || f.count(pCount) != 0 {
		t.Fatalf("status %d, calls %v", code, f.calls)
	}
}
