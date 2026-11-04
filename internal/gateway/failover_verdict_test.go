package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// failoverSetupNamed is failoverSetup with a chosen display name for A.
func failoverSetupNamed(t *testing.T, name string, a, b *failoverUp) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ua := httptest.NewServer(a)
	t.Cleanup(ua.Close)
	ub := httptest.NewServer(b)
	t.Cleanup(ub.Close)
	if err := provider.Save(provider.Provider{ID: "a", Name: name, Key: "k", Models: []string{"m1"},
		Chat: ua.URL + "/v1", Fallback: []string{"b/m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "k", Models: []string{"m2"},
		Chat: ub.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

func TestFallbackRecordNamesTheAnswerer(t *testing.T) {
	a := &failoverUp{code: 429, reply: `{"error":{"message":"slow down"}}`}
	b := &failoverUp{reply: failoverOKChat}
	failoverSetup(t, a, b)
	srv := New()
	if code, out := failoverPost(t, srv, failoverReq); code != 200 || !strings.Contains(out, "from B") {
		t.Fatalf("got %d %s; want B's reply", code, out)
	}
	rec := srv.Recent()[0]
	if rec.Provider != "b" || rec.Model != "b/m2" {
		t.Fatalf("record provider %q model %q; want b and b/m2", rec.Provider, rec.Model)
	}
	if rec.Fallback != "a: HTTP 429: slow down" {
		t.Fatalf("fallback %q; want %q", rec.Fallback, "a: HTTP 429: slow down")
	}
}

func TestFallbackKeepsModelWithoutSwitch(t *testing.T) {
	a := &failoverUp{reply: `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"from A"},"finish_reason":"stop"}]}`}
	b := &failoverUp{reply: failoverOKChat}
	failoverSetup(t, a, b)
	srv := New()
	failoverPost(t, srv, failoverReq)
	if rec := srv.Recent()[0]; rec.Model != "a/m1" || rec.Provider != "a" {
		t.Fatalf("record provider %q model %q; want a and a/m1", rec.Provider, rec.Model)
	}
}

func TestFallbackJudgesUpstreamBodyNotDisplayName(t *testing.T) {
	// The display name "Quota Relay" must not make a plain 400 switchable.
	a := &failoverUp{code: 400, reply: `{"error":{"message":"messages: field required"}}`}
	b := &failoverUp{reply: failoverOKChat}
	failoverSetupNamed(t, "Quota Relay", a, b)
	srv := New()
	code, out := failoverPost(t, srv, failoverReq)
	if code != 400 || !strings.Contains(out, "field required") {
		t.Fatalf("got %d %s; want the 400 as it was", code, out)
	}
	if b.calls != 0 {
		t.Fatalf("b was asked %d times; the display name must not count", b.calls)
	}
}

func TestFallbackKeywordFromUpstreamCodeSwitches(t *testing.T) {
	// The keyword is only in error.code of the raw body, not in the message
	// the client would see.
	a := &failoverUp{code: 400, reply: `{"error":{"code":"insufficient_quota","message":"request failed"}}`}
	b := &failoverUp{reply: failoverOKChat}
	failoverSetup(t, a, b)
	srv := New()
	if code, out := failoverPost(t, srv, failoverReq); code != 200 || !strings.Contains(out, "from B") {
		t.Fatalf("got %d %s; want B's reply", code, out)
	}
	if rec := srv.Recent()[0]; rec.Provider != "b" {
		t.Fatalf("record provider %q; want b", rec.Provider)
	}
}

func TestFallbackWordsIgnoreCase(t *testing.T) {
	for _, text := range []string{"Insufficient balance", "Too many requests", "RATE LIMIT hit", "额度已用完"} {
		if !hasFallbackWord(text) {
			t.Errorf("%q should be a fallback word", text)
		}
	}
	if hasFallbackWord("messages: field required") {
		t.Error("a plain request error must not be a fallback word")
	}
}

// streamEvents feeds a fixed list of events, then closes.
func streamEvents(evs ...Event) <-chan Event {
	ch := make(chan Event, len(evs))
	for _, ev := range evs {
		ch <- ev
	}
	close(ch)
	return ch
}

func TestClaudeStreamErrorFirstIsHeldAndClassified(t *testing.T) {
	rec := httptest.NewRecorder()
	aw := newAttemptWriter(rec)
	var u Usage
	streamClaude(aw, provider.Anthropic, "m1", streamEvents(
		Event{Kind: KStart, MsgID: "x", Model: "m1"},
		Event{Kind: KError, Text: "You've hit your usage limit"},
	), &u)
	if !aw.failed() || !aw.retryable() || aw.status != 429 {
		t.Fatalf("status %d failed %v retryable %v; want held 429 that may switch", aw.status, aw.failed(), aw.retryable())
	}
	if rec.Body.Len() != 0 || rec.Flushed {
		t.Fatalf("client received output %q before the verdict", rec.Body.String())
	}
}

func TestClaudeStreamErrorFirstNotRetryable(t *testing.T) {
	rec := httptest.NewRecorder()
	aw := newAttemptWriter(rec)
	var u Usage
	streamClaude(aw, provider.Anthropic, "m1", streamEvents(
		Event{Kind: KError, Text: "invalid tool schema"},
	), &u)
	if !aw.failed() || aw.retryable() || aw.status != 502 {
		t.Fatalf("status %d retryable %v; want held 502 that does not switch", aw.status, aw.retryable())
	}
}

func TestClaudeStreamOutputCommitsBeforeError(t *testing.T) {
	rec := httptest.NewRecorder()
	aw := newAttemptWriter(rec)
	var u Usage
	status, msg := streamClaude(aw, provider.Anthropic, "m1", streamEvents(
		Event{Kind: KStart, MsgID: "x", Model: "m1"},
		Event{Kind: KText, Text: "partial"},
		Event{Kind: KError, Text: "rate limit later"},
	), &u)
	if !aw.committed || status != 200 || msg != "rate limit later" {
		t.Fatalf("committed %v status %d msg %q; want committed 200 and the error text", aw.committed, status, msg)
	}
	if !strings.Contains(rec.Body.String(), "partial") {
		t.Fatalf("output lost: %q", rec.Body.String())
	}
}

func TestClaudeStreamWithoutFallbackIsUnchanged(t *testing.T) {
	rec := httptest.NewRecorder()
	var u Usage
	status, msg := streamClaude(rec, provider.Anthropic, "m1", streamEvents(
		Event{Kind: KError, Text: "boom"},
	), &u)
	if status != 200 || msg != "boom" || rec.Code != 200 {
		t.Fatalf("status %d msg %q code %d; want the error streamed as before", status, msg, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("error event not streamed: %q", rec.Body.String())
	}
}
