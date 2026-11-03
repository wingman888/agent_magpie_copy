package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// modelUpstream answers by the model name it is asked for: a model mapped to
// a status gets that status, anything else streams a reply that names this
// upstream. It records "path model" per call, so a test can see which model
// name each upstream received.
type modelUpstream struct {
	label  string
	status map[string]int
	open   string // when set, a stream starts with this event instead of a reply
	mu     sync.Mutex
	seen   []string
}

func (u *modelUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &req)
	u.mu.Lock()
	u.seen = append(u.seen, r.URL.Path+" "+req.Model)
	u.mu.Unlock()
	if code := u.status[req.Model]; code >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"`+u.label+` refused"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if u.open != "" {
		io.WriteString(w, sse(u.open))
		return
	}
	io.WriteString(w, sse(
		`data: {"id":"c1","model":"`+req.Model+`","choices":[{"delta":{"content":"`+u.label+`"}}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`data: [DONE]`))
}

func (u *modelUpstream) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

// fallbackSetup saves a primary provider "relay" that serves model m1 and,
// with a fallback list, a second provider "backup" that serves m2.
func fallbackSetup(t *testing.T, primary, backup *modelUpstream, fallback ...string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p1 := httptest.NewServer(primary)
	t.Cleanup(p1.Close)
	p2 := httptest.NewServer(backup)
	t.Cleanup(p2.Close)
	must(t, provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1",
		Chat: p1.URL + "/v1", Models: []string{"m1"}, Fallback: fallback}))
	must(t, provider.Save(provider.Provider{ID: "backup", Name: "Backup", Key: "kb",
		Chat: p2.URL + "/v1", Models: []string{"m2"}}))
}

const m1Req = `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`

// A quota or rate-limit error from the main provider is answered by the
// fallback model, which the upstream receives under its own name; the client
// sees no error, and the call log names both the provider and the failure.
func TestQuotaErrorFallsBackToFallbackModel(t *testing.T) {
	primary := &modelUpstream{label: "relay", status: map[string]int{"m1": 429}}
	backup := &modelUpstream{label: "backup"}
	fallbackSetup(t, primary, backup, "backup/m2")
	s := New()

	code, body := do(t, s, "/v1/chat/completions", m1Req)
	if code != 200 || !strings.Contains(body, "backup") {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := backup.calls(); strings.Join(got, ",") != "/v1/chat/completions m2" {
		t.Errorf("fallback upstream saw %v", got)
	}
	c := lastCall(t, s)
	if c.Provider != "backup" {
		t.Errorf("call provider %q, want backup", c.Provider)
	}
	if !strings.Contains(c.Fallback, "relay") || !strings.Contains(c.Fallback, "429") {
		t.Errorf("fallback note should name the failed provider and status: %q", c.Fallback)
	}
}

// The other statuses that mean "try elsewhere" fall back the same way.
func TestFallbackStatuses(t *testing.T) {
	for _, status := range []int{402, 403, 429, 500, 502, 503, 529} {
		primary := &modelUpstream{label: "relay", status: map[string]int{"m1": status}}
		backup := &modelUpstream{label: "backup"}
		fallbackSetup(t, primary, backup, "backup/m2")
		code, body := do(t, New(), "/v1/chat/completions", m1Req)
		if code != 200 || !strings.Contains(body, "backup") {
			t.Errorf("HTTP %d: status %d: %s", status, code, body)
		}
	}
}

// A bad request is the caller's problem; the fallback never sees it.
func TestBadRequestIsNotFallenBack(t *testing.T) {
	primary := &modelUpstream{label: "relay", status: map[string]int{"m1": 400}}
	backup := &modelUpstream{label: "backup"}
	fallbackSetup(t, primary, backup, "backup/m2")
	s := New()

	code, body := do(t, s, "/v1/chat/completions", m1Req)
	if code != 400 || !strings.Contains(body, "relay refused") {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := backup.calls(); len(got) != 0 {
		t.Errorf("fallback was tried for a 400: %v", got)
	}
}

// When every candidate fails, the client gets the last candidate's error.
func TestLastCandidateErrorIsReturned(t *testing.T) {
	primary := &modelUpstream{label: "relay", status: map[string]int{"m1": 429}}
	backup := &modelUpstream{label: "backup", status: map[string]int{"m2": 503}}
	fallbackSetup(t, primary, backup, "backup/m2")

	code, body := do(t, New(), "/v1/chat/completions", m1Req)
	if code != 503 || !strings.Contains(body, "backup refused") {
		t.Fatalf("want the last candidate's 503, got %d: %s", code, body)
	}
}

// A candidate that just failed rests for a while: the next request goes to
// the fallback first and leaves the failed provider alone.
func TestFailedProviderIsSkippedForAWhile(t *testing.T) {
	primary := &modelUpstream{label: "relay", status: map[string]int{"m1": 429}}
	backup := &modelUpstream{label: "backup"}
	fallbackSetup(t, primary, backup, "backup/m2")
	s := New()

	if code, _ := do(t, s, "/v1/chat/completions", m1Req); code != 200 {
		t.Fatalf("first request: %d", code)
	}
	if code, _ := do(t, s, "/v1/chat/completions", m1Req); code != 200 {
		t.Fatalf("second request: %d", code)
	}
	if got := primary.calls(); len(got) != 1 {
		t.Errorf("resting provider was tried again: %v", got)
	}
	if got := backup.calls(); len(got) != 2 {
		t.Errorf("fallback should serve both: %v", got)
	}
}

// A fallback that names no provider, or cannot be parsed, is passed over.
func TestUnusableFallbackEntriesAreSkipped(t *testing.T) {
	primary := &modelUpstream{label: "relay", status: map[string]int{"m1": 429}}
	backup := &modelUpstream{label: "backup"}
	fallbackSetup(t, primary, backup, "ghost/m2", "garbage", "backup/m2")

	code, body := do(t, New(), "/v1/chat/completions", m1Req)
	if code != 200 || !strings.Contains(body, "backup") {
		t.Fatalf("status %d: %s", code, body)
	}
}

// A stream that opens with a quota error is moved to the fallback before
// anything reaches the client, on the relay path (same protocol) ...
func TestStreamOpeningQuotaErrorFallsBackOnRelay(t *testing.T) {
	primary := &modelUpstream{label: "relay",
		open: `data: {"error":{"message":"You exceeded your current quota"}}`}
	backup := &modelUpstream{label: "backup"}
	fallbackSetup(t, primary, backup, "backup/m2")

	code, body := do(t, New(), "/v1/chat/completions",
		`{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "backup") || strings.Contains(body, "exceeded") {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := backup.calls(); strings.Join(got, ",") != "/v1/chat/completions m2" {
		t.Errorf("fallback upstream saw %v", got)
	}
}

// ... and on the translated path (an Anthropic client, a Chat-only provider).
func TestStreamOpeningQuotaErrorFallsBackWhenTranslated(t *testing.T) {
	primary := &modelUpstream{label: "relay",
		open: `data: {"error":{"message":"rate limit reached"}}`}
	backup := &modelUpstream{label: "backup"}
	fallbackSetup(t, primary, backup, "backup/m2")

	code, body := do(t, New(), "/v1/messages",
		`{"model":"m1","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "backup") || strings.Contains(body, "rate limit") {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := backup.calls(); len(got) != 1 {
		t.Errorf("fallback upstream saw %v", got)
	}
}

// An opening stream error that is not a quota problem is shown as it is.
func TestStreamOpeningBadRequestIsNotFallenBack(t *testing.T) {
	primary := &modelUpstream{label: "relay",
		open: `data: {"error":{"message":"messages field is malformed"}}`}
	backup := &modelUpstream{label: "backup"}
	fallbackSetup(t, primary, backup, "backup/m2")

	code, body := do(t, New(), "/v1/chat/completions",
		`{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "malformed") {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := backup.calls(); len(got) != 0 {
		t.Errorf("fallback was tried for a malformed request: %v", got)
	}
}

func TestFailoverReplyClassification(t *testing.T) {
	for _, c := range []struct {
		status int
		msg    string
		want   bool
	}{
		{400, "insufficient quota", false},
		{401, "", false},
		{402, "", true},
		{403, "", true},
		{429, "", true},
		{529, "", true},
		{503, "", true},
		{404, "model not found", false},
		{404, "quota exhausted", true},
	} {
		if got := failoverReply(c.status, c.msg); got != c.want {
			t.Errorf("failoverReply(%d, %q) = %v, want %v", c.status, c.msg, got, c.want)
		}
	}
}
