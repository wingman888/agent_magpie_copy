package gateway

import (
	"bufio"
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

const (
	anthropicMsgStart    = `event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_A","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`
	anthropicOverloadErr = `event: error` + "\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
)

// shortHold makes the gate's hold d for the test.
func shortHold(t *testing.T, d time.Duration) {
	t.Helper()
	old := streamHold
	streamHold = d
	t.Cleanup(func() { streamHold = old })
}

// A reply that goes silent after its start is let through when the hold is
// up, without waiting for the upstream to say anything more; an error that
// comes after that is the agent's to see.
func TestSilentReplyIsPassedWhenHoldIsUp(t *testing.T) {
	fresh(t)
	shortHold(t, 50*time.Millisecond)
	var once sync.Once
	proceed := make(chan struct{})
	release := func() { once.Do(func() { close(proceed) }) }
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicMsgStart+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-proceed:
			io.WriteString(w, anthropicOverloadErr+"\n\n")
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(up.Close)
	t.Cleanup(release) // runs before up.Close, so a failed test does not hang
	saveProviderOrFail(t, provider.Provider{ID: "solo", Name: "Solo", Key: "k", Models: []string{"m"}, Anthropic: up.URL})

	gw := httptest.NewServer(New().Handler())
	t.Cleanup(gw.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", gw.URL+"/v1/messages",
		strings.NewReader(`{"model":"solo/m","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("the reply was never let through: %v", err)
	}
	defer res.Body.Close()
	br := bufio.NewReader(res.Body)
	var seen strings.Builder
	for !strings.Contains(seen.String(), "msg_A") {
		line, err := br.ReadString('\n')
		seen.WriteString(line)
		if err != nil {
			t.Fatalf("the start did not reach the agent: %v (%q)", err, seen.String())
		}
	}
	release()
	rest, _ := io.ReadAll(br)
	if got := seen.String() + string(rest); !strings.Contains(got, "Overloaded") {
		t.Fatalf("the error after the hold did not reach the agent: %q", got)
	}
}

// Once the held events are past the size the gate keeps, an error that comes
// next is the agent's: nothing is transferred, and the next member is not asked.
func TestErrorAfterHoldBytesIsNotTransferred(t *testing.T) {
	fresh(t)
	pad := strings.Repeat("x", streamHoldBytes)
	a := &scriptUp{answers: []scriptReply{{ctype: "text/event-stream", body: sse(
		anthropicMsgStart,
		`event: ping`+"\n"+`data: {"type":"ping","pad":"`+pad+`"}`,
		anthropicOverloadErr)}}}
	b := &scriptUp{answers: []scriptReply{{body: fromB}}}
	saveProviderOrFail(t, provider.Provider{ID: "a", Name: "A", Key: "ka", Models: []string{"m"}, Anthropic: upstreamAt(t, a)})
	saveProviderOrFail(t, provider.Provider{ID: "b", Name: "B", Key: "kb", Models: []string{"m"}, Chat: upstreamAt(t, b) + "/v1"})
	saveFailoverGroup(t, "a/m", "b/m")
	code, body := serveTo(t, New(), "/v1/messages", `{"model":"group/g","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "Overloaded") || b.calls() != 0 {
		t.Fatalf("%d (b asked %d) %.200s", code, b.calls(), body)
	}
}

// A non-streamed reply that the translation turned from a stream's error
// before content keeps that error's status, as a stream would.
func TestNonStreamReplyKeepsStreamErrorStatus(t *testing.T) {
	fresh(t)
	u := &scriptUp{answers: []scriptReply{{ctype: "text/event-stream", body: sse(
		`data: {"error":{"message":"rate limit exceeded, slow down"}}`, `data: [DONE]`)}}}
	saveProviderOrFail(t, provider.Provider{ID: "solo", Name: "Solo", Key: "k", Models: []string{"m"}, Chat: upstreamAt(t, u) + "/v1"})
	code, body := serveTo(t, New(), "/v1/responses", `{"model":"solo/m","input":"hi"}`)
	// a 429 with no Retry-After is not tried again
	if code != 429 || u.calls() != 1 {
		t.Fatalf("%d %s (asked %d)", code, body, u.calls())
	}
}

// A lone provider that fails transiently is retried after a wait; if the
// agent goes away during that wait, the try is recorded as canceled, with no
// wait and no further request.
func TestCancelDuringRetryWaitIsRecordedAsCanceled(t *testing.T) {
	fresh(t)
	u := &scriptUp{answers: []scriptReply{{code: 503, body: `{"error":{"message":"busy"}}`}, {body: fromB}}}
	saveProviderOrFail(t, provider.Provider{ID: "solo", Name: "Solo", Key: "k", Models: []string{"m1"}, Chat: upstreamAt(t, u) + "/v1"})
	s := New()
	s.sleep = func(context.Context, time.Duration) bool { return false } // the agent went away
	serveTo(t, s, "/v1/chat/completions", soloChat)
	r := lastRoute(t, s)
	if u.calls() != 1 {
		t.Fatalf("asked %d times", u.calls())
	}
	if r.Status != statusCanceled || r.Error != failCanceled || len(r.Tries) != 1 {
		t.Fatalf("route %+v", r)
	}
	tr := r.Tries[0]
	if tr.Kind != "canceled" || tr.Fail != failCanceled || tr.Status != statusCanceled || tr.WaitMs != 0 || tr.Error != failCanceled {
		t.Fatalf("try %+v", tr)
	}
	if rec := s.Recent(); len(rec) != 1 || rec[0].Status != statusCanceled {
		t.Fatalf("recent %+v", rec)
	}
}
