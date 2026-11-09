package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// s4Env keeps config, cache and home out of the real ones.
func s4Env(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// s4Upstream answers by the model a request names. Models in fail get that
// status back, with a quota message; models in streamFail open a 200 stream
// whose first event is a rate-limit error; the rest get a reply. It records
// the model names it was asked for, in order.
type s4Upstream struct {
	mu         sync.Mutex
	asked      []string
	fail       map[string]int
	streamFail map[string]bool
}

func (u *s4Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	json.Unmarshal(b, &req)
	u.mu.Lock()
	u.asked = append(u.asked, req.Model)
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if code := u.fail[req.Model]; code != 0 {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"`+req.Model+` quota exceeded"}}`)
		return
	}
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		if u.streamFail[req.Model] {
			io.WriteString(w, "data: {\"error\":{\"message\":\"rate limit exceeded\"}}\n\n")
			return
		}
		io.WriteString(w, `data: {"id":"s4","object":"chat.completion.chunk","model":"`+req.Model+`","choices":[{"index":0,"delta":{"content":"s4 ok"},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		return
	}
	io.WriteString(w, `{"id":"s4","object":"chat.completion","model":"`+req.Model+`","choices":[{"index":0,"message":{"role":"assistant","content":"s4 ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
}

func (u *s4Upstream) seen() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return strings.Join(u.asked, ",")
}

// s4Setup saves "backup" (model m2) and "main" (model m1), both served by one
// upstream, with main's fallback list as given.
func s4Setup(t *testing.T, up *s4Upstream, fallback ...string) {
	t.Helper()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "backup", Name: "Backup", Chat: srv.URL + "/v1", Key: "sk-s4-backup", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "main", Name: "Main", Chat: srv.URL + "/v1", Key: "sk-s4-main", Models: []string{"m1"}, Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
}

// s4Post sends one request through the gateway's own handler.
func s4Post(t *testing.T, s *Server, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

const s4Chat = `{"model":"main/m1","messages":[{"role":"user","content":"hi"}]}`
const s4Stream = `{"model":"main/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func TestS4FallbackServesWhenMainIsOutOfQuota(t *testing.T) {
	s4Env(t)
	up := &s4Upstream{fail: map[string]int{"m1": 429}}
	s4Setup(t, up, "backup/m2")
	s := New()
	code, body := s4Post(t, s, s4Chat)
	if code != 200 || !strings.Contains(body, "s4 ok") {
		t.Fatalf("status %d body %s, want the fallback's reply", code, body)
	}
	if got := up.seen(); got != "m1,m2" {
		t.Fatalf("upstream asked for %s, want m1 then the fallback's own name m2", got)
	}
	c := s.Recent()[0]
	if c.Provider != "backup" || !strings.Contains(c.Fallback, "main") || c.Status != 200 {
		t.Fatalf("call record: provider %q fallback %q status %d", c.Provider, c.Fallback, c.Status)
	}
}

func TestS4FallbackStatusesAndNoFallbackForBadRequest(t *testing.T) {
	for _, code := range []int{402, 403, 429, 500, 529} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			s4Env(t)
			up := &s4Upstream{fail: map[string]int{"m1": code}}
			s4Setup(t, up, "backup/m2")
			if got, body := s4Post(t, New(), s4Chat); got != 200 {
				t.Fatalf("%d from main: status %d body %s, want the fallback's 200", code, got, body)
			}
		})
	}
	s4Env(t)
	up := &s4Upstream{fail: map[string]int{"m1": 400}}
	s4Setup(t, up, "backup/m2")
	s := New()
	if code, body := s4Post(t, s, s4Chat); code != 400 {
		t.Fatalf("status %d body %s, want the 400 passed through", code, body)
	}
	if got := up.seen(); got != "m1" {
		t.Fatalf("upstream asked for %s, want only m1: a 400 is not retried elsewhere", got)
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Fatalf("400 left fallback record %q", c.Fallback)
	}
}

func TestS4AllCandidatesFailReturnsLastError(t *testing.T) {
	s4Env(t)
	up := &s4Upstream{fail: map[string]int{"m1": 429, "m2": 503}}
	s4Setup(t, up, "backup/m2")
	s := New()
	code, body := s4Post(t, s, s4Chat)
	if code != 503 || !strings.Contains(body, "m2 quota exceeded") {
		t.Fatalf("status %d body %s, want the last candidate's 503", code, body)
	}
	if c := s.Recent()[0]; c.Provider != "backup" || c.Fallback == "" {
		t.Fatalf("call record: provider %q fallback %q", c.Provider, c.Fallback)
	}
}

func TestS4FailedProviderIsSkippedForAWhile(t *testing.T) {
	s4Env(t)
	up := &s4Upstream{fail: map[string]int{"m1": 429}}
	s4Setup(t, up, "backup/m2")
	s := New()
	if code, _ := s4Post(t, s, s4Chat); code != 200 {
		t.Fatalf("first request: %d", code)
	}
	if code, _ := s4Post(t, s, s4Chat); code != 200 {
		t.Fatalf("second request: %d", code)
	}
	if got := up.seen(); got != "m1,m2,m2" {
		t.Fatalf("upstream asked for %s, want main skipped on the second request", got)
	}
	if c := s.Recent()[0]; c.Provider != "backup" || c.Fallback != "" {
		t.Fatalf("second request: provider %q fallback %q", c.Provider, c.Fallback)
	}
}

func TestS4StreamErrorAtStartMovesToFallback(t *testing.T) {
	s4Env(t)
	up := &s4Upstream{streamFail: map[string]bool{"m1": true}}
	s4Setup(t, up, "backup/m2")
	s := New()
	code, body := s4Post(t, s, s4Stream)
	if code != 200 || !strings.Contains(body, "s4 ok") || strings.Contains(body, "rate limit") {
		t.Fatalf("status %d body %s, want the fallback's stream with no error sent to the client", code, body)
	}
	if got := up.seen(); got != "m1,m2" {
		t.Fatalf("upstream asked for %s, want m1 then m2", got)
	}
	if c := s.Recent()[0]; c.Provider != "backup" || !strings.Contains(c.Fallback, "main") {
		t.Fatalf("call record: provider %q fallback %q", c.Provider, c.Fallback)
	}
}

func TestS4UnresolvableAndDuplicateFallbackEntriesAreSkipped(t *testing.T) {
	s4Env(t)
	up := &s4Upstream{fail: map[string]int{"m1": 429}}
	s4Setup(t, up, "nope", "ghost/x", "main/m1", "backup/m2", "backup/m2")
	s := New()
	if code, body := s4Post(t, s, s4Chat); code != 200 {
		t.Fatalf("status %d body %s", code, body)
	}
	if got := up.seen(); got != "m1,m2" {
		t.Fatalf("upstream asked for %s, want m1 then m2 once", got)
	}
}
