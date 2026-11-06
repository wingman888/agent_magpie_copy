package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// l1GwScripted is an upstream that answers with a fixed status and body, and
// records the model each request named.
type l1GwScripted struct {
	mu     sync.Mutex
	status int    // 0 answers 200
	reply  string // empty answers the Anthropic reply (200) or a generic error
	hits   int
	models []string
}

func (s *l1GwScripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.hits++
	s.models = append(s.models, modelOf(b))
	status, reply := s.status, s.reply
	s.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	if reply == "" {
		if status == http.StatusOK {
			reply = l1GwAnthropicReply
		} else {
			reply = `{"error":{"message":"upstream said so"}}`
		}
	}
	if status >= 400 {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	w.WriteHeader(status)
	io.WriteString(w, reply)
}

func (s *l1GwScripted) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func (s *l1GwScripted) lastModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.models) == 0 {
		return ""
	}
	return s.models[len(s.models)-1]
}

// l1GwFallbackSetup saves "primary" (Anthropic endpoint, with the given fallback
// list) and "backup", each backed by its l1GwScripted upstream.
func l1GwFallbackSetup(t *testing.T, primary, backup *l1GwScripted, fallback ...string) *Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	pu := httptest.NewServer(primary)
	t.Cleanup(pu.Close)
	bu := httptest.NewServer(backup)
	t.Cleanup(bu.Close)
	if err := provider.Save(provider.Provider{ID: "primary", Name: "Primary", Key: "k1", Anthropic: pu.URL, Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "backup", Name: "Backup", Key: "k2", Anthropic: bu.URL}); err != nil {
		t.Fatal(err)
	}
	return New()
}

const l1GwPrimaryBody = `{"model":"primary/claude-x","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`

const l1GwPrimaryStreamBody = `{"model":"primary/claude-x","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`

func TestL1FallbackAnswersWhenPrimaryFails(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"rate limit", 429, `{"error":{"message":"rate limited"}}`},
		{"out of credit", 402, `{"error":{"message":"payment required"}}`},
		{"overloaded", 529, `{"error":{"message":"overloaded"}}`},
		{"outage", 503, `{"error":{"message":"unavailable"}}`},
		{"quota on a 403", 403, `{"error":{"message":"insufficient balance"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			primary := &l1GwScripted{status: tc.status, reply: tc.body}
			backup := &l1GwScripted{}
			// the unresolvable entry is skipped silently
			srv := l1GwFallbackSetup(t, primary, backup, "nosuch/x", "backup/m2")
			rec := l1GwPostTo(t, srv, "/v1/messages", l1GwPrimaryBody)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if got := backup.lastModel(); got != "m2" {
				t.Fatalf("fallback upstream got model %q, want m2", got)
			}
			call := srv.Recent()[0]
			if call.Provider != "backup" {
				t.Errorf("call provider = %q, want backup", call.Provider)
			}
			if want := fmt.Sprintf("primary (%d)", tc.status); !strings.Contains(call.Fallback, want) {
				t.Errorf("call fallback = %q, want it to name %q", call.Fallback, want)
			}
		})
	}
}

func TestL1BadRequestIsNotFallenBack(t *testing.T) {
	primary := &l1GwScripted{status: 400, reply: `{"error":{"message":"bad request"}}`}
	backup := &l1GwScripted{}
	srv := l1GwFallbackSetup(t, primary, backup, "backup/m2")
	rec := l1GwPostTo(t, srv, "/v1/messages", l1GwPrimaryBody)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "bad request") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if backup.count() != 0 {
		t.Fatalf("fallback was tried %d times for a 400", backup.count())
	}
}

func TestL1ForbiddenWithoutQuotaIsNotFallenBack(t *testing.T) {
	primary := &l1GwScripted{status: 403, reply: `{"error":{"message":"forbidden by policy"}}`}
	backup := &l1GwScripted{}
	srv := l1GwFallbackSetup(t, primary, backup, "backup/m2")
	rec := l1GwPostTo(t, srv, "/v1/messages", l1GwPrimaryBody)
	if rec.Code != 403 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if backup.count() != 0 {
		t.Fatalf("fallback was tried for a plain 403")
	}
}

func TestL1LastCandidateErrorIsReturned(t *testing.T) {
	primary := &l1GwScripted{status: 429, reply: `{"error":{"message":"rate limited"}}`}
	backup := &l1GwScripted{status: 503, reply: `{"error":{"message":"backup down"}}`}
	srv := l1GwFallbackSetup(t, primary, backup, "backup/m2")
	rec := l1GwPostTo(t, srv, "/v1/messages", l1GwPrimaryBody)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "backup down") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if call := srv.Recent()[0]; call.Provider != "backup" || call.Status != 503 {
		t.Fatalf("call record = %+v", call)
	}
}

func TestL1FailedProviderRestsAfterFallback(t *testing.T) {
	primary := &l1GwScripted{status: 429, reply: `{"error":{"message":"rate limited"}}`}
	backup := &l1GwScripted{}
	srv := l1GwFallbackSetup(t, primary, backup, "backup/m2")
	l1GwPostTo(t, srv, "/v1/messages", l1GwPrimaryBody)
	l1GwPostTo(t, srv, "/v1/messages", l1GwPrimaryBody)
	if primary.count() != 1 {
		t.Fatalf("primary tried %d times; it should rest after failing", primary.count())
	}
	if backup.count() != 2 {
		t.Fatalf("backup tried %d times, want 2", backup.count())
	}
	if calls := srv.Recent(); calls[0].Fallback != "" {
		t.Errorf("second request recorded a fallback: %q", calls[0].Fallback)
	}
}

func TestL1StreamOpeningErrorFallsBack(t *testing.T) {
	primary := &l1GwScripted{reply: "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"}
	backup := &l1GwScripted{}
	srv := l1GwFallbackSetup(t, primary, backup, "backup/m2")
	rec := l1GwPostTo(t, srv, "/v1/messages", l1GwPrimaryStreamBody)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Overloaded") {
		t.Fatalf("the opening error reached the client: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("fallback reply missing: %s", rec.Body.String())
	}
	if got := backup.lastModel(); got != "m2" {
		t.Fatalf("fallback upstream got model %q, want m2", got)
	}
}

func TestL1StreamErrorStatus(t *testing.T) {
	cases := map[string]int{
		`{"type":"error","error":{"type":"rate_limit_error"}}`:                    429,
		`{"error":{"type":"insufficient_quota","code":"insufficient_quota"}}`:     402,
		`{"type":"response.failed","response":{"error":{"code":"server_error"}}}`: 503,
		`{"type":"error","error":{"type":"invalid_request_error"}}`:               0,
		`{"id":"c1","choices":[]}`:                                                0,
	}
	for data, want := range cases {
		if got := streamErrorStatus(data); got != want {
			t.Errorf("streamErrorStatus(%s) = %d, want %d", data, got, want)
		}
	}
}

const l1GwAnthropicReply = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":1}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

data: {"type":"message_stop"}

`

func l1GwPostTo(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)
	return rec
}
