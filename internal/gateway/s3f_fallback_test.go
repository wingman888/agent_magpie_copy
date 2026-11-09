package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// s3fFail is how the upstream answers a model it has been told to fail.
type s3fFail struct {
	status int
	msg    string
}

// s3fUpstream answers by the model a request names. A model in fail gets that
// error; a model in lead gets its text (an SSE event sequence) and nothing
// more; any other gets a short chat stream. It keeps "path model" per request.
type s3fUpstream struct {
	mu   sync.Mutex
	seen []string
	fail map[string]s3fFail
	lead map[string]string
	base string // the upstream's address
}

const s3fChatReply = `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}

data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`

func (u *s3fUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	m := modelOf(b)
	u.mu.Lock()
	u.seen = append(u.seen, r.URL.Path+" "+m)
	f, failing := u.fail[m]
	lead := u.lead[m]
	u.mu.Unlock()
	if failing {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		io.WriteString(w, `{"error":{"message":"`+f.msg+`"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if lead != "" {
		io.WriteString(w, lead)
		return
	}
	io.WriteString(w, s3fChatReply)
}

// take returns the requests seen since the last call and forgets them.
func (u *s3fUpstream) take() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := strings.Join(u.seen, "|")
	u.seen = nil
	return out
}

// s3fSetup saves a primary "s3fp" (model m1, key k1, falling back to
// "s3fb/m2") and a backup "s3fb" (model m2, key k2) on one upstream.
func s3fSetup(t *testing.T, fail map[string]s3fFail, lead map[string]string) *s3fUpstream {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	u := &s3fUpstream{fail: fail, lead: lead}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	u.base = up.URL
	if err := provider.Save(provider.Provider{ID: "s3fb", Name: "s3fb", Key: "k2",
		Chat: up.URL + "/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "s3fp", Name: "s3fp", Key: "k1",
		Chat: up.URL + "/v1", Models: []string{"m1"}, Fallback: []string{"s3fb/m2"}}); err != nil {
		t.Fatal(err)
	}
	return u
}

func s3fPost(s *Server, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const s3fBody = `{"model":"s3fp/m1","messages":[{"role":"user","content":"hi"}]}`
const s3fStreamBody = `{"model":"s3fp/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func TestS3FRetryableStatusesFallBackToModel(t *testing.T) {
	for _, f := range []s3fFail{
		{429, "slow down"},
		{402, "payment required"},
		{529, "overloaded"},
		{403, "额度已用完"},
		{403, "quota exceeded for this key"},
	} {
		u := s3fSetup(t, map[string]s3fFail{"m1": f}, nil)
		s := New()
		code, body := s3fPost(s, s3fBody)
		if code != 200 {
			t.Fatalf("%d %q: status %d: %s", f.status, f.msg, code, body)
		}
		if !strings.Contains(body, `"hi"`) {
			t.Errorf("%d %q: client did not get the fallback reply: %s", f.status, f.msg, body)
		}
		if got := u.take(); got != "/chat/completions m1|/chat/completions m2" {
			t.Errorf("%d %q: upstream saw %s", f.status, f.msg, got)
		}
		c := s.Recent()[0]
		if c.Provider != "s3fb" || c.Status != 200 || c.Fallback != "s3fp" {
			t.Errorf("%d %q: call provider %q status %d fallback %q", f.status, f.msg, c.Provider, c.Status, c.Fallback)
		}
	}
}

func TestS3FBadRequestIsNotRetried(t *testing.T) {
	u := s3fSetup(t, map[string]s3fFail{"m1": {400, "bad request"}}, nil)
	s := New()
	code, body := s3fPost(s, s3fBody)
	if code != 400 || !strings.Contains(body, "bad request") {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := u.take(); got != "/chat/completions m1" {
		t.Errorf("a 400 should not fall back; upstream saw %s", got)
	}
	if c := s.Recent()[0]; c.Fallback != "" || c.Provider != "s3fp" {
		t.Errorf("call: provider %q fallback %q", c.Provider, c.Fallback)
	}
}

func TestS3FLastCandidateErrorReachesClient(t *testing.T) {
	u := s3fSetup(t, map[string]s3fFail{"m1": {429, "m1 is busy"}, "m2": {503, "m2 is down"}}, nil)
	s := New()
	code, body := s3fPost(s, s3fBody)
	if code != 503 || !strings.Contains(body, "m2 is down") {
		t.Fatalf("want the last candidate's 503, got %d: %s", code, body)
	}
	if got := u.take(); got != "/chat/completions m1|/chat/completions m2" {
		t.Errorf("upstream saw %s", got)
	}
	if c := s.Recent()[0]; c.Status != 503 || c.Fallback != "s3fp" {
		t.Errorf("call: status %d fallback %q", c.Status, c.Fallback)
	}
}

func TestS3FFailedProviderRestsForAMinute(t *testing.T) {
	u := s3fSetup(t, map[string]s3fFail{"m1": {429, "slow down"}}, nil)
	s := New()
	if code, body := s3fPost(s, s3fBody); code != 200 {
		t.Fatalf("first status %d: %s", code, body)
	}
	if got := u.take(); got != "/chat/completions m1|/chat/completions m2" {
		t.Errorf("first request saw %s", got)
	}
	// the primary is skipped while it rests: the backup answers first
	if code, body := s3fPost(s, s3fBody); code != 200 {
		t.Fatalf("second status %d: %s", code, body)
	}
	if got := u.take(); got != "/chat/completions m2" {
		t.Errorf("second request should skip the resting provider; upstream saw %s", got)
	}
	if c := s.Recent()[0]; c.Provider != "s3fb" || c.Fallback != "" {
		t.Errorf("second call: provider %q fallback %q", c.Provider, c.Fallback)
	}
}

func TestS3FFallbackLabelNamesTheKey(t *testing.T) {
	u := s3fSetup(t, map[string]s3fFail{"m1": {429, "slow down"}}, nil)
	if err := provider.Save(provider.Provider{ID: "s3fp", Name: "s3fp", Key: "k1", KeyName: "personal",
		Chat: u.base + "/v1", Models: []string{"m1"}, Fallback: []string{"s3fb/m2"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, body := s3fPost(s, s3fBody); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if c := s.Recent()[0]; c.Fallback != "s3fp (personal)" || c.Provider != "s3fb" {
		t.Errorf("call: provider %q fallback %q", c.Provider, c.Fallback)
	}
}

func TestS3FUnreadableFallbackEntriesAreSkipped(t *testing.T) {
	u := s3fSetup(t, map[string]s3fFail{"m1": {429, "slow down"}}, nil)
	if err := provider.Save(provider.Provider{ID: "s3fp", Name: "s3fp", Key: "k1",
		Chat: u.base + "/v1", Models: []string{"m1"},
		Fallback: []string{"no-slash", "s3fnone/m9", "s3fb/m2"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if code, body := s3fPost(s, s3fBody); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := u.take(); got != "/chat/completions m1|/chat/completions m2" {
		t.Errorf("upstream saw %s", got)
	}
}

func TestS3FStreamOpeningErrorFallsBack(t *testing.T) {
	lead := map[string]string{
		"m1": "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n",
	}
	u := s3fSetup(t, nil, lead)
	s := New()
	code, body := s3fPost(s, s3fStreamBody)
	if code != 200 || !strings.Contains(body, `"hi"`) || strings.Contains(body, "Overloaded") {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := u.take(); got != "/chat/completions m1|/chat/completions m2" {
		t.Errorf("upstream saw %s", got)
	}
	if c := s.Recent()[0]; c.Provider != "s3fb" || c.Fallback != "s3fp" {
		t.Errorf("call: provider %q fallback %q", c.Provider, c.Fallback)
	}
}

func TestS3FStreamOpeningBadRequestIsNotRetried(t *testing.T) {
	lead := map[string]string{
		"m1": "data: {\"error\":{\"message\":\"invalid request body\"}}\n\n",
	}
	u := s3fSetup(t, nil, lead)
	s := New()
	code, body := s3fPost(s, s3fStreamBody)
	if code != 200 || !strings.Contains(body, "invalid request body") {
		t.Fatalf("the error should reach the client as it was: %d %s", code, body)
	}
	if got := u.take(); got != "/chat/completions m1" {
		t.Errorf("upstream saw %s", got)
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Errorf("fallback on a 400: %q", c.Fallback)
	}
}

func TestS3FStreamedReplyPassesThroughUnchanged(t *testing.T) {
	u := s3fSetup(t, nil, nil)
	s := New()
	code, body := s3fPost(s, s3fStreamBody)
	if code != 200 || body != s3fChatReply {
		t.Fatalf("status %d, body %q", code, body)
	}
	if got := u.take(); got != "/chat/completions m1" {
		t.Errorf("upstream saw %s", got)
	}
}
