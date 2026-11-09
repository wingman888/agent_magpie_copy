package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// fbgwUpstream is a fake Chat Completions backend that replies with a fixed
// status and body and counts what reached it.
type fbgwUpstream struct {
	code  int
	ctype string
	body  string
	hits  int
	model string
}

func (u *fbgwUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.hits++
	b, _ := io.ReadAll(r.Body)
	u.model = modelOf(b)
	ct := u.ctype
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	if u.code != 0 {
		w.WriteHeader(u.code)
	}
	io.WriteString(w, u.body)
}

const fbgwGoodReply = `{"id":"b1","object":"chat.completion","model":"m2","choices":[{"index":0,"message":{"role":"assistant","content":"from-b"},"finish_reason":"stop"}]}`

// fbgwSetup saves provider fbgwa (model m1, the given fallback list) and
// provider fbgwb (model m2), each backed by its own upstream.
func fbgwSetup(t *testing.T, a, b *fbgwUpstream, fallback ...string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ua := httptest.NewServer(a)
	t.Cleanup(ua.Close)
	ub := httptest.NewServer(b)
	t.Cleanup(ub.Close)
	if err := provider.Save(provider.Provider{ID: "fbgwa", Name: "Fbgw A", Key: "k",
		Chat: ua.URL + "/v1", Models: []string{"m1"}, Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "fbgwb", Name: "Fbgw B", Key: "k",
		Chat: ub.URL + "/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
}

func fbgwPost(t *testing.T, srv *Server, ctx context.Context, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

const fbgwReq = `{"model":"fbgwa/m1","messages":[{"role":"user","content":"hi"}]}`

func TestFbgwSwitchRules(t *testing.T) {
	cases := []struct {
		name       string
		code       int
		msg        string
		wantSwitch bool
	}{
		{"429 rate limited", 429, "slow down", true},
		{"402 payment", 402, "payment required", true},
		{"529 overloaded", 529, "busy", true},
		{"500 server", 500, "boom", true},
		{"400 quota wording chinese", 400, "额度已用完", true},
		{"403 insufficient balance, mixed case", 403, "Insufficient balance", true},
		{"400 too many requests, mixed case", 400, "Too many requests", true},
		{"400 plain parameter error stays", 400, "messages: field required", false},
		{"401 bad key stays", 401, "invalid api key", false},
		{"404 stays", 404, "no such model", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `{"error":{"message":` + jsonString(c.msg) + `}}`
			a := &fbgwUpstream{code: c.code, body: body}
			b := &fbgwUpstream{code: 200, body: fbgwGoodReply}
			fbgwSetup(t, a, b, "fbgwb/m2")
			rec := fbgwPost(t, New(), context.Background(), fbgwReq)
			if c.wantSwitch {
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-b") {
					t.Fatalf("want fallback answer, got %d %s", rec.Code, rec.Body)
				}
				if b.hits != 1 || b.model != "m2" {
					t.Fatalf("fallback hits=%d model=%q, want 1 and m2", b.hits, b.model)
				}
				if strings.Contains(rec.Body.String(), c.msg) {
					t.Fatalf("client saw the failed provider's error: %s", rec.Body)
				}
				return
			}
			if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.msg) {
				t.Fatalf("want %d with %q unchanged, got %d %s", c.code, c.msg, rec.Code, rec.Body)
			}
			if b.hits != 0 {
				t.Fatalf("fallback was tried for a request error")
			}
		})
	}
}

func TestFbgwAllFailReturnsLastError(t *testing.T) {
	a := &fbgwUpstream{code: 429, body: `{"error":{"message":"rate limited a"}}`}
	b := &fbgwUpstream{code: 503, body: `{"error":{"message":"b is down"}}`}
	fbgwSetup(t, a, b, "fbgwb/m2")
	rec := fbgwPost(t, New(), context.Background(), fbgwReq)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "b is down") {
		t.Fatalf("want last candidate's 503, got %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "rate limited a") {
		t.Fatalf("earlier error leaked: %s", rec.Body)
	}
}

func TestFbgwRestingProviderGoesLast(t *testing.T) {
	a := &fbgwUpstream{code: 429, body: `{"error":{"message":"rate limited"}}`}
	b := &fbgwUpstream{code: 200, body: fbgwGoodReply}
	fbgwSetup(t, a, b, "fbgwb/m2")
	srv := New()

	if rec := fbgwPost(t, srv, context.Background(), fbgwReq); rec.Code != 200 {
		t.Fatalf("first request: %d %s", rec.Code, rec.Body)
	}
	if a.hits != 1 {
		t.Fatalf("A hits after first request = %d, want 1", a.hits)
	}
	first := srv.Recent()[0]
	if first.Provider != "fbgwb" || !strings.HasPrefix(first.Fallback, "fbgwa: ") {
		t.Fatalf("first record provider=%q fallback=%q", first.Provider, first.Fallback)
	}

	if rec := fbgwPost(t, srv, context.Background(), fbgwReq); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-b") {
		t.Fatalf("second request: %d %s", rec.Code, rec.Body)
	}
	if a.hits != 1 {
		t.Fatalf("A was tried during its cooldown: hits=%d", a.hits)
	}
	second := srv.Recent()[0]
	if second.Provider != "fbgwb" || !strings.HasPrefix(second.Fallback, "fbgwa: ") {
		t.Fatalf("second record provider=%q fallback=%q", second.Provider, second.Fallback)
	}
}

func TestFbgwStreamNeverSwitchesAfterSuccessHeader(t *testing.T) {
	first := `data: {"id":"a1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"content":"from-a"},"finish_reason":null}]}`
	a := &fbgwUpstream{code: 200, ctype: "text/event-stream",
		body: first + "\n\ndata: {\"error\":{\"message\":\"overloaded mid-stream\"}}\n\n"}
	b := &fbgwUpstream{code: 200, body: fbgwGoodReply}
	fbgwSetup(t, a, b, "fbgwb/m2")
	req := `{"model":"fbgwa/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := fbgwPost(t, New(), context.Background(), req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-a") {
		t.Fatalf("want A's stream, got %d %s", rec.Code, rec.Body)
	}
	if b.hits != 0 || strings.Contains(rec.Body.String(), "from-b") {
		t.Fatalf("fallback leaked into a started stream: hits=%d body=%s", b.hits, rec.Body)
	}
}

func TestFbgwSkipsUnknownAndDuplicateEntries(t *testing.T) {
	a := &fbgwUpstream{code: 429, body: `{"error":{"message":"rate limited"}}`}
	b := &fbgwUpstream{code: 200, body: fbgwGoodReply}
	fbgwSetup(t, a, b, "fbgwnowhere/x", "fbgwb/m2", "fbgwb/m2", "fbgwa/m1")
	rec := fbgwPost(t, New(), context.Background(), fbgwReq)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-b") || b.hits != 1 {
		t.Fatalf("want one fallback to B, got %d hits=%d %s", rec.Code, b.hits, rec.Body)
	}
}

func TestFbgwNoFallbackRecordHasNoField(t *testing.T) {
	a := &fbgwUpstream{code: 200, body: fbgwGoodReply}
	b := &fbgwUpstream{code: 200, body: fbgwGoodReply}
	fbgwSetup(t, a, b)
	srv := New()
	if rec := fbgwPost(t, srv, context.Background(), fbgwReq); rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	call := srv.Recent()[0]
	if call.Fallback != "" || call.Provider != "fbgwa" {
		t.Fatalf("record provider=%q fallback=%q", call.Provider, call.Fallback)
	}
	b2, _ := json.Marshal(call)
	if strings.Contains(string(b2), `"fallback"`) {
		t.Fatalf("fallback key serialized without a switch: %s", b2)
	}
}

func TestFbgwStopsWhenClientIsGone(t *testing.T) {
	a := &fbgwUpstream{code: 429, body: `{"error":{"message":"rate limited"}}`}
	b := &fbgwUpstream{code: 200, body: fbgwGoodReply}
	fbgwSetup(t, a, b, "fbgwb/m2")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fbgwPost(t, New(), ctx, fbgwReq)
	if b.hits != 0 {
		t.Fatalf("fallback tried after the client went away: hits=%d", b.hits)
	}
}

func TestFbgwQuotaWordingInWholeBody(t *testing.T) {
	// the quota word sits in error.type, not in the message the client sees
	a := &fbgwUpstream{code: 400, body: `{"error":{"message":"Request failed","type":"insufficient_quota"}}`}
	b := &fbgwUpstream{code: 200, body: fbgwGoodReply}
	fbgwSetup(t, a, b, "fbgwb/m2")
	rec := fbgwPost(t, New(), context.Background(), fbgwReq)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from-b") || b.hits != 1 {
		t.Fatalf("want fallback to B, got %d hits=%d %s", rec.Code, b.hits, rec.Body)
	}
}

func TestFbgwPrimaryAnswerListsNoRestingFallbacks(t *testing.T) {
	a := &fbgwUpstream{code: 200, body: fbgwGoodReply}
	b := &fbgwUpstream{code: 200, body: fbgwGoodReply}
	fbgwSetup(t, a, b, "fbgwb/m2")
	srv := New()
	srv.markCooling(provider.Provider{ID: "fbgwb", Name: "Fbgw B"}, 429, "Fbgw B: 429 Too Many Requests")
	if rec := fbgwPost(t, srv, context.Background(), fbgwReq); rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	call := srv.Recent()[0]
	if call.Provider != "fbgwa" || call.Fallback != "" || b.hits != 0 {
		t.Fatalf("primary answer: provider=%q fallback=%q bHits=%d", call.Provider, call.Fallback, b.hits)
	}
}

func TestFbgwClientGoneDoesNotCoolProvider(t *testing.T) {
	a := &fbgwUpstream{code: 429, body: `{"error":{"message":"rate limited"}}`}
	b := &fbgwUpstream{code: 200, body: fbgwGoodReply}
	fbgwSetup(t, a, b, "fbgwb/m2")
	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fbgwPost(t, srv, ctx, fbgwReq)
	if reason := srv.coolingReason("fbgwa"); reason != "" {
		t.Fatalf("provider cooled by a request whose client had gone: %q", reason)
	}
}

func TestFbgwReasonLeadsWithStatusOnce(t *testing.T) {
	p := provider.Provider{ID: "fbgwa", Name: "Fbgw A"}
	if got := reasonOf(p, 429, "Fbgw A: 429 Too Many Requests"); got != "429 Too Many Requests" {
		t.Fatalf("got %q", got)
	}
	if got := reasonOf(p, 429, "Fbgw A: rate limited"); got != "429 rate limited" {
		t.Fatalf("got %q", got)
	}
	if got := reasonOf(p, 502, "Fbgw A has no endpoint configured"); got != "502 has no endpoint configured" {
		t.Fatalf("got %q", got)
	}
}

// jsonString quotes s as a JSON string literal.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
