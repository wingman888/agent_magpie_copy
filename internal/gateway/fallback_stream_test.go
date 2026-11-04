package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// sseUp is an upstream that answers every request with a fixed event stream
// and records the model it was asked for.
type sseUp struct {
	body   string
	calls  int
	models []string
}

func (f *sseUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls++
	b, _ := io.ReadAll(r.Body)
	var m struct {
		Model string `json:"model"`
	}
	json.Unmarshal(b, &m)
	f.models = append(f.models, m.Model)
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, f.body)
}

// Provider A speaks only Anthropic, so a Chat client is translated to it.
// Its stream opens with an error event; B answers with a Chat stream.
const failoverBChatStream = "data: " + `{"id":"c1","object":"chat.completion.chunk","model":"m2","choices":[{"index":0,"delta":{"role":"assistant","content":"from B"},"finish_reason":null}]}` + "\n\ndata: [DONE]\n\n"

func failoverSetupTranslated(t *testing.T, a *sseUp, b *failoverUp) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ua := httptest.NewServer(a)
	t.Cleanup(ua.Close)
	ub := httptest.NewServer(b)
	t.Cleanup(ub.Close)
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Models: []string{"m1"},
		Anthropic: ua.URL, Fallback: []string{"b/m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "k", Models: []string{"m2"},
		Chat: ub.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

const failoverStreamReq = `{"model":"a/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func TestFallbackTranslatedStreamErrorFirstSwitches(t *testing.T) {
	a := &sseUp{body: "event: error\ndata: " + `{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit exceeded"}}` + "\n\n"}
	b := &failoverUp{reply: failoverBChatStream}
	failoverSetupTranslated(t, a, b)
	srv := New()
	code, out := failoverPost(t, srv, failoverStreamReq)
	if code != 200 || !strings.Contains(out, "from B") {
		t.Fatalf("got %d %s; want B's stream", code, out)
	}
	if strings.Contains(out, "Rate limit") {
		t.Fatalf("client saw A's error event: %s", out)
	}
	if a.calls != 1 || len(b.models) != 1 || b.models[0] != "m2" {
		t.Fatalf("a calls %d, b models %v; want 1 and [m2]", a.calls, b.models)
	}
	rec := srv.Recent()[0]
	if rec.Provider != "b" || !strings.HasPrefix(rec.Fallback, "a: HTTP 429") {
		t.Fatalf("record provider %q fallback %q", rec.Provider, rec.Fallback)
	}
}

func TestFallbackTranslatedStreamPlainErrorIsForwarded(t *testing.T) {
	a := &sseUp{body: "event: error\ndata: " + `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid tool schema"}}` + "\n\n"}
	b := &failoverUp{reply: failoverBChatStream}
	failoverSetupTranslated(t, a, b)
	srv := New()
	_, out := failoverPost(t, srv, failoverStreamReq)
	if b.calls != 0 {
		t.Fatalf("b was asked %d times for a non-retryable error", b.calls)
	}
	if !strings.Contains(out, "Invalid tool schema") {
		t.Fatalf("error event not forwarded: %s", out)
	}
}

func TestCliRetryableClassifiesCLIText(t *testing.T) {
	for _, text := range []string{"API Error: 503", "API Error: 529 overloaded", "API Error: 500", "API Error: 408", "API Error: 402", "API Error: 429",
		"Internal Server Error", "service unavailable", "Bad Gateway", "gateway timeout", "Insufficient balance"} {
		if !cliRetryable(text) {
			t.Errorf("%q should be retryable", text)
		}
	}
	for _, text := range []string{"invalid request", "API Error: 400 invalid request", "API Error: 404", "invalid tool schema"} {
		if cliRetryable(text) {
			t.Errorf("%q must not be retryable", text)
		}
	}
	if hasFallbackWord("API Error: 503") {
		t.Error("status codes must not join the shared keyword list")
	}
}

func TestClaudeStreamCLIStatusFirstIsRetryable(t *testing.T) {
	rec := httptest.NewRecorder()
	aw := newAttemptWriter(rec)
	var u Usage
	streamClaude(aw, provider.Anthropic, "m1", streamEvents(
		Event{Kind: KError, Text: "API Error: 503"},
	), &u)
	if !aw.failed() || !aw.retryable() || aw.status != 429 {
		t.Fatalf("status %d failed %v retryable %v; want held 429 that may switch", aw.status, aw.failed(), aw.retryable())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("client received output %q before the verdict", rec.Body.String())
	}
}
