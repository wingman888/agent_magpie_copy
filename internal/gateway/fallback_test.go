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

// scripted is an upstream that answers with a fixed status and body, and
// records the model each request named.
type scripted struct {
	mu     sync.Mutex
	status int    // 0 answers 200
	reply  string // empty answers the Anthropic reply (200) or a generic error
	hits   int
	models []string
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
			reply = anthropicReply
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

func (s *scripted) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func (s *scripted) lastModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.models) == 0 {
		return ""
	}
	return s.models[len(s.models)-1]
}

// fallbackSetup saves "primary" (Anthropic endpoint, with the given fallback
// list) and "backup", each backed by its scripted upstream.
func fallbackSetup(t *testing.T, primary, backup *scripted, fallback ...string) *Server {
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

const primaryBody = `{"model":"primary/claude-x","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`

const primaryStreamBody = `{"model":"primary/claude-x","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`

func TestFallbackAnswersWhenPrimaryFails(t *testing.T) {
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
			primary := &scripted{status: tc.status, reply: tc.body}
			backup := &scripted{}
			// the unresolvable entry is skipped silently
			srv := fallbackSetup(t, primary, backup, "nosuch/x", "backup/m2")
			rec := postTo(t, srv, "/v1/messages", primaryBody)
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

func TestBadRequestIsNotFallenBack(t *testing.T) {
	primary := &scripted{status: 400, reply: `{"error":{"message":"bad request"}}`}
	backup := &scripted{}
	srv := fallbackSetup(t, primary, backup, "backup/m2")
	rec := postTo(t, srv, "/v1/messages", primaryBody)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "bad request") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if backup.count() != 0 {
		t.Fatalf("fallback was tried %d times for a 400", backup.count())
	}
}

func TestForbiddenWithoutQuotaIsNotFallenBack(t *testing.T) {
	primary := &scripted{status: 403, reply: `{"error":{"message":"forbidden by policy"}}`}
	backup := &scripted{}
	srv := fallbackSetup(t, primary, backup, "backup/m2")
	rec := postTo(t, srv, "/v1/messages", primaryBody)
	if rec.Code != 403 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if backup.count() != 0 {
		t.Fatalf("fallback was tried for a plain 403")
	}
}

func TestLastCandidateErrorIsReturned(t *testing.T) {
	primary := &scripted{status: 429, reply: `{"error":{"message":"rate limited"}}`}
	backup := &scripted{status: 503, reply: `{"error":{"message":"backup down"}}`}
	srv := fallbackSetup(t, primary, backup, "backup/m2")
	rec := postTo(t, srv, "/v1/messages", primaryBody)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "backup down") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if call := srv.Recent()[0]; call.Provider != "backup" || call.Status != 503 {
		t.Fatalf("call record = %+v", call)
	}
}

func TestFailedProviderRestsAfterFallback(t *testing.T) {
	primary := &scripted{status: 429, reply: `{"error":{"message":"rate limited"}}`}
	backup := &scripted{}
	srv := fallbackSetup(t, primary, backup, "backup/m2")
	postTo(t, srv, "/v1/messages", primaryBody)
	postTo(t, srv, "/v1/messages", primaryBody)
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

func TestStreamOpeningErrorFallsBack(t *testing.T) {
	primary := &scripted{reply: "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"}
	backup := &scripted{}
	srv := fallbackSetup(t, primary, backup, "backup/m2")
	rec := postTo(t, srv, "/v1/messages", primaryStreamBody)
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

func TestStreamErrorStatus(t *testing.T) {
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
