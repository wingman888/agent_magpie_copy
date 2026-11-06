package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// An agent that leaves while a transient failure is waiting to be retried is
// recorded on that try as canceled; the provider is not blamed or rested, and
// it is not asked again.
func TestAgentLeavingDuringRetryWaitIsRecordedOnTheTry(t *testing.T) {
	fresh(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := waitRetry
	waitRetry = func(c context.Context, d time.Duration) error {
		cancel() // the agent goes away during the wait
		<-c.Done()
		return c.Err()
	}
	t.Cleanup(func() { waitRetry = old })

	up := &seqUp{replies: []seqReply{{503, "", `{"error":{"message":"busy"}}`}}}
	serveOn(t, "a", "ka", []string{"m"}, up)
	s := New()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if up.calls != 1 {
		t.Fatalf("upstream was asked %d times, want 1", up.calls)
	}
	if _, resting := restOf("a"); resting {
		t.Fatal("a rests after the agent left")
	}
	rs := s.Trace(context.Background(), 0, 0).Routes
	if len(rs) != 1 || len(rs[0].Tries) != 1 {
		t.Fatalf("routes %+v", rs)
	}
	try := rs[0].Tries[0]
	if try.Status != 499 || try.Fail != failCanceled || try.Rest != nil || !try.Done {
		t.Fatalf("try %+v", try)
	}
	if rs[0].Status != 499 || rs[0].Error != failCanceled {
		t.Fatalf("route %+v", rs[0])
	}
}

// Once every API the model has is remembered as refused, nothing is sent
// upstream and the endpoint list is empty.
func TestRefusedAPIsAreNotSentAgain(t *testing.T) {
	fresh(t)
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		hits.Add(1)
		w.WriteHeader(400)
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := New()
	s.markRefused("fake", "m", provider.Chat)
	s.markRefused("fake", "m", provider.Responses)
	if got := s.endpoints(p, "m", provider.Chat, true); len(got) != 0 {
		t.Fatalf("endpoints = %v, want none", got)
	}
	code, _ := postAs(t, s, "", `{"model":"fake/m","messages":[{"role":"user","content":"hi"}]}`)
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d upstream requests were sent to refused endpoints", n)
	}
	if code != 502 {
		t.Fatalf("status %d, want 502", code)
	}
}

// "use v1/completions" is not a refusal phrase; a 5xx naming another API is
// never a refusal.
func TestOtherAPIPhrasesNeedARefusal(t *testing.T) {
	if endpointRefused(400, []byte("use /v1/completions")) {
		t.Fatal("use /v1/completions is not a refusal phrase")
	}
	if endpointRefused(500, []byte("use v1/responses")) {
		t.Fatal("a 500 is never a refusal")
	}
	if !endpointRefused(400, []byte("use v1/responses")) {
		t.Fatal("a 400 naming another endpoint is a refusal")
	}
}
