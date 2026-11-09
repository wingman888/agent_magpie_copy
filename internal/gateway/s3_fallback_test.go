package gateway

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// s3OK is a Chat stream that answers "ok".
const s3OK = "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"

// s3Upstream answers every request with one script and records the path and
// the model name each request carried, in order.
type s3Upstream struct {
	mu     sync.Mutex
	status int // 0 means 200
	ctype  string
	reply  string
	paths  []string
	models []string
}

func (u *s3Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.paths = append(u.paths, r.URL.Path)
	u.models = append(u.models, modelOf(body))
	status, ctype, reply := u.status, u.ctype, u.reply
	u.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	if ctype == "" {
		ctype = "text/event-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(status)
	io.WriteString(w, reply)
}

func (u *s3Upstream) hits() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.paths)
}

func (u *s3Upstream) lastModel() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.models) == 0 {
		return ""
	}
	return u.models[len(u.models)-1]
}

// s3Env starts a fresh config and two upstreams: the primary (relay) and the
// fallback (backup). Each is a copy of the script given, so tests can
// script them apart.
func s3Env(t *testing.T, primary, backup *s3Upstream) (*httptest.Server, *httptest.Server) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := httptest.NewServer(primary)
	b := httptest.NewServer(backup)
	t.Cleanup(p.Close)
	t.Cleanup(b.Close)
	return p, b
}

// s3Save saves the primary "relay" and the fallback "backup" provider. The
// relay falls back to backup/gpt-alt unless fallback is given.
func s3Save(t *testing.T, relay, backup *httptest.Server, fallback []string) {
	t.Helper()
	if fallback == nil {
		fallback = []string{"backup/gpt-alt"}
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-s3-main",
		Chat: relay.URL + "/v1", Anthropic: relay.URL, Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "backup", Name: "Backup", Key: "sk-s3-backup",
		Chat: backup.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

// s3Chat is a streaming Chat request for the relay's model.
const s3Chat = `{"model":"relay/gpt-5.5","stream":true,"messages":[{"role":"user","content":"hi"}]}`

// s3Post sends one request and returns the status and body.
func s3Post(t *testing.T, h http.Handler, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func TestS3FallbackOnRetryableStatuses(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"rate limited", http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`},
		{"payment required", http.StatusPaymentRequired, `{"error":{"message":"pay up"}}`},
		{"overloaded", 529, `{"error":{"message":"overloaded"}}`},
		{"unavailable", http.StatusServiceUnavailable, `{"error":{"message":"down"}}`},
		{"quota forbidden", http.StatusForbidden, `{"error":{"message":"额度已用完"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			primary := &s3Upstream{status: c.status, ctype: "application/json", reply: c.body}
			backup := &s3Upstream{reply: s3OK}
			up, bk := s3Env(t, primary, backup)
			s3Save(t, up, bk, nil)
			srv := New()
			code, body := s3Post(t, srv.Handler(), "/v1/chat/completions", s3Chat)
			if code != 200 || !strings.Contains(body, "ok") {
				t.Fatalf("status %d body %q, want the fallback's answer", code, body)
			}
			if primary.hits() != 1 || backup.hits() != 1 {
				t.Fatalf("hits primary=%d backup=%d", primary.hits(), backup.hits())
			}
			if got := backup.lastModel(); got != "gpt-alt" {
				t.Errorf("fallback received model %q, want its own name gpt-alt", got)
			}
			calls := srv.Recent()
			if len(calls) != 1 || calls[0].Provider != "backup" || calls[0].Fallback != "relay" || calls[0].Status != 200 {
				t.Fatalf("call record: %+v", calls)
			}
		})
	}
}

func TestS3BadRequestIsNotRetried(t *testing.T) {
	primary := &s3Upstream{status: http.StatusBadRequest, ctype: "application/json", reply: `{"error":{"message":"bad field"}}`}
	backup := &s3Upstream{reply: s3OK}
	up, bk := s3Env(t, primary, backup)
	s3Save(t, up, bk, nil)
	srv := New()
	code, body := s3Post(t, srv.Handler(), "/v1/chat/completions", s3Chat)
	if code != 400 || !strings.Contains(body, "bad field") {
		t.Fatalf("status %d body %q, want the primary's 400 as it was", code, body)
	}
	if backup.hits() != 0 {
		t.Fatalf("a 400 fell back to the backup")
	}
	if calls := srv.Recent(); calls[0].Fallback != "" || calls[0].Provider != "relay" {
		t.Fatalf("call record: %+v", calls[0])
	}
}

func TestS3LastCandidateErrorReturned(t *testing.T) {
	primary := &s3Upstream{status: http.StatusTooManyRequests, ctype: "application/json", reply: `{"error":{"message":"primary busy"}}`}
	backup := &s3Upstream{status: http.StatusServiceUnavailable, ctype: "application/json", reply: `{"error":{"message":"backup down"}}`}
	up, bk := s3Env(t, primary, backup)
	s3Save(t, up, bk, nil)
	srv := New()
	code, body := s3Post(t, srv.Handler(), "/v1/chat/completions", s3Chat)
	if code != 503 || !strings.Contains(body, "backup down") {
		t.Fatalf("status %d body %q, want the last candidate's 503", code, body)
	}
	if strings.Contains(body, "primary busy") {
		t.Errorf("client saw the first candidate's error: %q", body)
	}
	calls := srv.Recent()
	if len(calls) != 1 || calls[0].Status != 503 || calls[0].Provider != "backup" || calls[0].Fallback != "relay" {
		t.Fatalf("call record: %+v", calls)
	}
}

func TestS3FailedProviderRestsForAMinute(t *testing.T) {
	primary := &s3Upstream{status: http.StatusTooManyRequests, ctype: "application/json", reply: `{"error":{"message":"busy"}}`}
	backup := &s3Upstream{reply: s3OK}
	up, bk := s3Env(t, primary, backup)
	s3Save(t, up, bk, nil)
	srv := New()
	if code, _ := s3Post(t, srv.Handler(), "/v1/chat/completions", s3Chat); code != 200 {
		t.Fatalf("first status %d", code)
	}
	if code, _ := s3Post(t, srv.Handler(), "/v1/chat/completions", s3Chat); code != 200 {
		t.Fatalf("second status %d", code)
	}
	if primary.hits() != 1 {
		t.Fatalf("the resting primary was tried again: %d hits", primary.hits())
	}
	if backup.hits() != 2 {
		t.Fatalf("backup hits %d, want 2", backup.hits())
	}
	if calls := srv.Recent(); calls[0].Fallback != "" || calls[0].Provider != "backup" {
		t.Fatalf("second call record: %+v", calls[0])
	}
}

func TestS3UnreachableProviderFallsBack(t *testing.T) {
	backup := &s3Upstream{reply: s3OK}
	up, bk := s3Env(t, &s3Upstream{}, backup)
	up.Close() // the primary's address refuses connections
	s3Save(t, up, bk, nil)
	srv := New()
	if code, _ := s3Post(t, srv.Handler(), "/v1/chat/completions", s3Chat); code != 200 {
		t.Fatalf("status %d, want the fallback's answer", code)
	}
	if calls := srv.Recent(); calls[0].Provider != "backup" || calls[0].Fallback != "relay" {
		t.Fatalf("call record: %+v", calls[0])
	}
}

func TestS3StreamErrorBeforeOutputFallsBack(t *testing.T) {
	// the primary answers 200, but its stream opens with an error
	primary := &s3Upstream{reply: "data: {\"error\":{\"message\":\"overloaded\"}}\n\n"}
	backup := &s3Upstream{reply: s3OK}
	up, bk := s3Env(t, primary, backup)
	s3Save(t, up, bk, nil)
	srv := New()
	code, body := s3Post(t, srv.Handler(), "/v1/chat/completions", s3Chat)
	if code != 200 || strings.Contains(body, "overloaded") || !strings.Contains(body, "ok") {
		t.Fatalf("status %d body %q, want only the fallback's stream", code, body)
	}
	if backup.hits() != 1 || backup.lastModel() != "gpt-alt" {
		t.Fatalf("backup hits %d model %q", backup.hits(), backup.lastModel())
	}
}

func TestS3TranslatedStreamErrorBeforeOutputFallsBack(t *testing.T) {
	// an Anthropic client reaches a Chat-only relay: the translated stream
	// opens with an error, so the fallback answers instead
	primary := &s3Upstream{reply: "data: {\"error\":{\"message\":\"overloaded\"}}\n\n"}
	backup := &s3Upstream{reply: s3OK}
	up, bk := s3Env(t, primary, backup)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-s3-main",
		Chat: up.URL + "/v1", Fallback: []string{"backup/gpt-alt"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "backup", Name: "Backup", Key: "sk-s3-backup", Chat: bk.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	body := `{"model":"relay/claude-sonnet-4","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	code, out := s3Post(t, srv.Handler(), "/v1/messages", body)
	if code != 200 || strings.Contains(out, "overloaded") || !strings.Contains(out, "ok") {
		t.Fatalf("status %d body %q, want only the fallback's answer", code, out)
	}
	if calls := srv.Recent(); calls[0].Provider != "backup" || calls[0].Fallback != "relay" {
		t.Fatalf("call record: %+v", calls[0])
	}
}

func TestS3UnresolvableFallbackSkipped(t *testing.T) {
	primary := &s3Upstream{status: http.StatusTooManyRequests, ctype: "application/json", reply: `{"error":{"message":"busy"}}`}
	backup := &s3Upstream{reply: s3OK}
	up, bk := s3Env(t, primary, backup)
	s3Save(t, up, bk, []string{"nowhere/x", "backup/gpt-alt"})
	srv := New()
	if code, _ := s3Post(t, srv.Handler(), "/v1/chat/completions", s3Chat); code != 200 {
		t.Fatalf("status %d", code)
	}
	if calls := srv.Recent(); calls[0].Provider != "backup" || calls[0].Fallback != "relay" {
		t.Fatalf("call record: %+v", calls[0])
	}
}

func TestS3ProtocolLimitedKeyNeverLeavesItsEndpoint(t *testing.T) {
	// a key limited to chat, on a vendor that also has Responses: when the
	// chat endpoint says the model is Responses-only, the key must not move
	// there; the upstream's error goes back to the client instead
	primary := &s3Upstream{status: http.StatusBadRequest, ctype: "application/json",
		reply: `{"error":{"message":"This model is not supported in the v1/chat/completions endpoint. Use v1/responses."}}`}
	responses := &s3Upstream{reply: s3OK}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/responses" {
			responses.ServeHTTP(w, r)
			return
		}
		primary.ServeHTTP(w, r)
	}))
	t.Cleanup(up.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-s3-chat", KeyProtocol: provider.Chat,
		Chat: up.URL + "/v1", Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	// an Anthropic client, so the request is translated
	body := `{"model":"relay/gpt-5.5","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	code, _ := s3Post(t, New().Handler(), "/v1/messages", body)
	if code != 400 {
		t.Fatalf("status %d, want the chat endpoint's 400", code)
	}
	if responses.hits() != 0 {
		t.Fatalf("a chat-limited key reached /responses")
	}
	if primary.hits() != 1 {
		t.Fatalf("chat endpoint hits %d", primary.hits())
	}
}

func TestS3FallbackOnPolicy(t *testing.T) {
	quota := []byte(`{"error":{"message":"You exceeded your current quota"}}`)
	for status, want := range map[int]bool{
		400: false, 401: false, 404: false, 422: false,
		402: true, 429: true, 500: true, 502: true, 503: true, 504: true, 529: true,
	} {
		if got := fallbackOn(status, nil); got != want {
			t.Errorf("fallbackOn(%d) = %v, want %v", status, got, want)
		}
	}
	if !fallbackOn(http.StatusForbidden, quota) {
		t.Error("a quota 403 should fall back")
	}
	if fallbackOn(http.StatusForbidden, []byte(`{"error":{"message":"forbidden model"}}`)) {
		t.Error("a plain 403 should not fall back")
	}
}

func TestS3ErrorEventDetection(t *testing.T) {
	cases := map[string]bool{
		"event: error\ndata: {\"type\":\"error\"}\n\n":                     true,
		"data: {\"error\":{\"message\":\"x\"}}\n\n":                        true,
		"event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n": true,
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n":     false,
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n":       false,
	}
	for in, want := range cases {
		if got := errorEvent(firstEvent(bufio.NewReader(strings.NewReader(in)))); got != want {
			t.Errorf("errorEvent(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestS3CandidateOrderKeepsKeysAheadOfFallbacks(t *testing.T) {
	primary := &s3Upstream{status: http.StatusTooManyRequests, ctype: "application/json", reply: `{"error":{"message":"busy"}}`}
	backup := &s3Upstream{reply: s3OK}
	up, bk := s3Env(t, primary, backup)
	s3Save(t, up, bk, nil)
	if err := provider.AddKey("relay", "team", "sk-s3-team", ""); err != nil {
		t.Fatal(err)
	}
	srv := New()
	if code, _ := s3Post(t, srv.Handler(), "/v1/chat/completions", s3Chat); code != 200 {
		t.Fatalf("status %d", code)
	}
	// both relay keys fail before the fallback answers, in the key order
	calls := srv.Recent()
	if calls[0].Provider != "backup" || calls[0].Fallback != "relay, relay (team)" {
		t.Fatalf("call record: %+v", calls[0])
	}
	if primary.hits() != 2 || backup.hits() != 1 {
		t.Fatalf("hits primary=%d backup=%d, want both relay keys before the backup", primary.hits(), backup.hits())
	}
}
