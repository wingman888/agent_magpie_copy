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

// modelRecorder is an upstream that notes the model field of every request
// it gets, and answers by that model.
type modelRecorder struct {
	mu     sync.Mutex
	models []string
	answer func(model string) (status int, ctype, body string)
}

func (m *modelRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	model := modelOf(b)
	m.mu.Lock()
	m.models = append(m.models, model)
	m.mu.Unlock()
	status, ctype, body := m.answer(model)
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(status)
	io.WriteString(w, body)
}

// seen lists the model field of each request the upstream got, joined by commas.
func (m *modelRecorder) seen() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(m.models, ",")
}

func newModelUpstream(t *testing.T, answer func(model string) (int, string, string)) (*modelRecorder, string) {
	t.Helper()
	mr := &modelRecorder{answer: answer}
	srv := httptest.NewServer(mr)
	t.Cleanup(srv.Close)
	return mr, srv.URL
}

const chatBody = `{"model":"main/m1","messages":[{"role":"user","content":"hi"}]}`

func TestFallbackTakesOverOnRetryableErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"rate limit", 429, `{"error":{"message":"slow down"}}`},
		{"payment required", 402, `{"error":{"message":"pay up"}}`},
		{"overloaded", 529, `{"error":{"message":"busy"}}`},
		{"quota text on a 403", 403, `{"error":{"message":"quota exceeded for this key"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keyHome(t)
			prim, primURL := newModelUpstream(t, func(string) (int, string, string) { return c.status, jsonType, c.body })
			backup, backupURL := newModelUpstream(t, func(string) (int, string, string) { return 200, streamType, chatReply })
			saveProvider(t, provider.Provider{ID: "main", Name: "Main", Chat: primURL + "/v1", Key: "sk-main",
				Models: []string{"m1"}, Fallback: []string{"backup/m2"}})
			saveProvider(t, provider.Provider{ID: "backup", Name: "Backup", Chat: backupURL + "/v1", Key: "sk-backup",
				Models: []string{"m2"}})
			s := New()
			code, body := callGateway(t, s.Handler(), "/v1/chat/completions", chatBody)
			if code != 200 || !strings.Contains(body, "ok") {
				t.Fatalf("status %d: %s", code, body)
			}
			if got := prim.seen(); got != "m1" {
				t.Errorf("primary saw models %q", got)
			}
			if got := backup.seen(); got != "m2" {
				t.Errorf("fallback saw models %q, want its own name m2", got)
			}
			if rec := s.Recent()[0]; rec.Status != 200 || rec.Provider != "backup" || rec.Fallback != "main" {
				t.Errorf("record: status %d provider %q fallback %q", rec.Status, rec.Provider, rec.Fallback)
			}
		})
	}
}

func TestFallbackTranslatesToTheFallbacksProtocol(t *testing.T) {
	keyHome(t)
	prim, primURL := newModelUpstream(t, func(string) (int, string, string) { return 429, jsonType, `{"error":{"message":"slow"}}` })
	backup, backupURL := newModelUpstream(t, func(string) (int, string, string) { return 200, streamType, chatReply })
	saveProvider(t, provider.Provider{ID: "main", Name: "Main", Anthropic: primURL, Key: "sk-main",
		Models: []string{"claude-x"}, Fallback: []string{"backup/m2"}})
	saveProvider(t, provider.Provider{ID: "backup", Name: "Backup", Chat: backupURL + "/v1", Key: "sk-backup"})
	code, body := callGateway(t, New().Handler(), "/v1/messages",
		`{"model":"main/claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := prim.seen(); got != "claude-x" {
		t.Errorf("primary saw %q", got)
	}
	if got := backup.seen(); got != "m2" {
		t.Errorf("translated fallback saw %q, want m2", got)
	}
}

func TestBadRequestDoesNotFallBack(t *testing.T) {
	keyHome(t)
	prim, primURL := newModelUpstream(t, func(string) (int, string, string) { return 400, jsonType, `{"error":{"message":"bad param"}}` })
	backup, backupURL := newModelUpstream(t, func(string) (int, string, string) { return 200, streamType, chatReply })
	saveProvider(t, provider.Provider{ID: "main", Name: "Main", Chat: primURL + "/v1", Key: "sk-main",
		Models: []string{"m1"}, Fallback: []string{"backup/m2"}})
	saveProvider(t, provider.Provider{ID: "backup", Name: "Backup", Chat: backupURL + "/v1", Key: "sk-backup"})
	s := New()
	code, body := callGateway(t, s.Handler(), "/v1/chat/completions", chatBody)
	if code != 400 || !strings.Contains(body, "bad param") {
		t.Errorf("status %d: %s", code, body)
	}
	if got := backup.seen(); got != "" {
		t.Errorf("fallback was asked on a 400: %q", got)
	}
	if got := prim.seen(); got != "m1" {
		t.Errorf("primary saw %q", got)
	}
	if f := s.Recent()[0].Fallback; f != "" {
		t.Errorf("fallback recorded on a plain error: %q", f)
	}
}

func TestLastCandidatesErrorIsReturned(t *testing.T) {
	keyHome(t)
	_, primURL := newModelUpstream(t, func(string) (int, string, string) { return 429, jsonType, `{"error":{"message":"primary limit"}}` })
	_, backupURL := newModelUpstream(t, func(string) (int, string, string) { return 429, jsonType, `{"error":{"message":"backup limit"}}` })
	saveProvider(t, provider.Provider{ID: "main", Name: "Main", Chat: primURL + "/v1", Key: "sk-main",
		Models: []string{"m1"}, Fallback: []string{"backup/m2"}})
	saveProvider(t, provider.Provider{ID: "backup", Name: "Backup", Chat: backupURL + "/v1", Key: "sk-backup"})
	s := New()
	code, body := callGateway(t, s.Handler(), "/v1/chat/completions", chatBody)
	if code != 429 || !strings.Contains(body, "backup limit") || strings.Contains(body, "primary limit") {
		t.Errorf("status %d: %s", code, body)
	}
	if c := s.Recent()[0]; c.Status != 429 || c.Provider != "backup" || c.Fallback != "main" {
		t.Errorf("record: status %d provider %q fallback %q", c.Status, c.Provider, c.Fallback)
	}
}

func TestFailedProviderIsSkippedForAMinute(t *testing.T) {
	keyHome(t)
	prim, primURL := newModelUpstream(t, func(string) (int, string, string) { return 429, jsonType, `{"error":{"message":"slow"}}` })
	backup, backupURL := newModelUpstream(t, func(string) (int, string, string) { return 200, streamType, chatReply })
	saveProvider(t, provider.Provider{ID: "main", Name: "Main", Chat: primURL + "/v1", Key: "sk-main",
		Models: []string{"m1"}, Fallback: []string{"backup/m2"}})
	saveProvider(t, provider.Provider{ID: "backup", Name: "Backup", Chat: backupURL + "/v1", Key: "sk-backup"})
	s := New()
	h := s.Handler()
	for i := 0; i < 2; i++ {
		if code, body := callGateway(t, h, "/v1/chat/completions", chatBody); code != 200 {
			t.Fatalf("request %d: status %d: %s", i, code, body)
		}
	}
	if got := prim.seen(); got != "m1" {
		t.Errorf("the resting primary was tried again: %q", got)
	}
	if got := backup.seen(); got != "m2,m2" {
		t.Errorf("fallback saw %q", got)
	}
	if c := s.Recent()[0]; c.Provider != "backup" || c.Fallback != "" {
		t.Errorf("second request: provider %q fallback %q", c.Provider, c.Fallback)
	}
}

func TestStreamOpeningErrorFallsBack(t *testing.T) {
	keyHome(t)
	_, primURL := newModelUpstream(t, func(string) (int, string, string) {
		return 200, streamType, sse(
			`event: error` + "\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	})
	backup, backupURL := newModelUpstream(t, func(string) (int, string, string) { return 200, streamType, anthropicReply })
	saveProvider(t, provider.Provider{ID: "main", Name: "Main", Anthropic: primURL, Key: "sk-main",
		Models: []string{"claude-x"}, Fallback: []string{"backup/claude-y"}})
	saveProvider(t, provider.Provider{ID: "backup", Name: "Backup", Anthropic: backupURL, Key: "sk-backup"})
	code, body := callGateway(t, New().Handler(), "/v1/messages",
		`{"model":"main/claude-x","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"text":"ok"`) || strings.Contains(body, "overloaded") {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := backup.seen(); got != "claude-y" {
		t.Errorf("fallback saw %q", got)
	}
}

func TestUnusableFallbackEntriesAreSkipped(t *testing.T) {
	keyHome(t)
	_, primURL := newModelUpstream(t, func(string) (int, string, string) { return 429, jsonType, `{"error":{"message":"slow"}}` })
	backup, backupURL := newModelUpstream(t, func(string) (int, string, string) { return 200, streamType, chatReply })
	saveProvider(t, provider.Provider{ID: "main", Name: "Main", Chat: primURL + "/v1", Key: "sk-main",
		Models: []string{"m1"}, Fallback: []string{"backup", "nowhere/m9", "/m", "backup/m2"}})
	saveProvider(t, provider.Provider{ID: "backup", Name: "Backup", Chat: backupURL + "/v1", Key: "sk-backup"})
	if code, body := callGateway(t, New().Handler(), "/v1/chat/completions", chatBody); code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := backup.seen(); got != "m2" {
		t.Errorf("fallback saw %q, want only m2", got)
	}
}
