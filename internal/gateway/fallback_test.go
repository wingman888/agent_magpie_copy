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

// fbFail is how the fake upstream refuses one model.
type fbFail struct {
	code int
	msg  string
}

// fbUp answers every model with success unless fail names it, and records the
// model each request asked for, in order.
type fbUp struct {
	mu     sync.Mutex
	fail   map[string]fbFail
	models []string
}

func (f *fbUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &req)
	f.mu.Lock()
	f.models = append(f.models, req.Model)
	fl, failing := f.fail[req.Model]
	f.mu.Unlock()
	if failing {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fl.code)
		io.WriteString(w, `{"error":{"message":"`+fl.msg+`"}}`)
		return
	}
	if streamOf(body) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","model":"`+req.Model+`","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","model":"`+req.Model+`","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			`data: [DONE]`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"`+req.Model+`","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
}

func (f *fbUp) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.models...)
}

// fbSetup isolates magpie's files, starts the fake upstream, and saves two
// providers: relay (model m1, falling back to backup/m2) and backup (model m2).
func fbSetup(t *testing.T, fail map[string]fbFail) (*fbUp, *Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fbUp{fail: fail}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", Models: []string{"m1"},
		Chat: up.URL + "/v1", Fallback: []string{"backup/m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "backup", Name: "Backup", Key: "k2", Models: []string{"m2"},
		Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	return f, New()
}

func fbDo(s *Server, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestFallbackRunsWhenPrimaryIsRateLimited(t *testing.T) {
	f, s := fbSetup(t, map[string]fbFail{"m1": {429, "rate limit exceeded"}})
	if code, out := fbDo(s, chatBody); code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	if got := strings.Join(f.asked(), ","); got != "m1,m2" {
		t.Fatalf("upstream asked for %s; want the fallback to ask for its own model name m2", got)
	}
	c := s.Recent()[0]
	if c.Status != 200 || c.Provider != "backup" || c.Fallback != "relay" {
		t.Errorf("call record = %+v; want backup answering, relay named as failed", c)
	}
}

func TestFallbackTriggers(t *testing.T) {
	for name, fl := range map[string]fbFail{
		"402 payment":     {402, "payment required"},
		"529 overloaded":  {529, "overloaded"},
		"503 unavailable": {503, "service unavailable"},
		"403 quota":       {403, "quota exceeded for this key"},
		"403 exhausted":   {403, "credits exhausted"},
		"403 额度":          {403, "额度已用完"},
	} {
		t.Run(name, func(t *testing.T) {
			f, s := fbSetup(t, map[string]fbFail{"m1": fl})
			if code, out := fbDo(s, chatBody); code != 200 {
				t.Fatalf("status %d: %s", code, out)
			}
			if got := strings.Join(f.asked(), ","); got != "m1,m2" {
				t.Errorf("upstream asked for %s", got)
			}
		})
	}
}

func TestBadRequestDoesNotFallBack(t *testing.T) {
	f, s := fbSetup(t, map[string]fbFail{"m1": {400, "bad parameter"}})
	code, out := fbDo(s, chatBody)
	if code != 400 || !strings.Contains(out, "bad parameter") {
		t.Fatalf("status %d: %s; want the 400 passed on", code, out)
	}
	if got := strings.Join(f.asked(), ","); got != "m1" {
		t.Errorf("upstream asked for %s; a 400 must not fall back", got)
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Errorf("call record names a fallback %q for a 400", c.Fallback)
	}
}

func TestLastCandidateErrorIsReturned(t *testing.T) {
	f, s := fbSetup(t, map[string]fbFail{"m1": {429, "primary busy"}, "m2": {503, "backup down"}})
	code, out := fbDo(s, chatBody)
	if code != 503 || !strings.Contains(out, "backup down") {
		t.Fatalf("status %d: %s; want the last candidate's 503", code, out)
	}
	if got := strings.Join(f.asked(), ","); got != "m1,m2" {
		t.Errorf("upstream asked for %s", got)
	}
}

func TestFailedProviderRestsWhileOthersAnswer(t *testing.T) {
	f, s := fbSetup(t, map[string]fbFail{"m1": {429, "rate limit exceeded"}})
	if code, out := fbDo(s, chatBody); code != 200 {
		t.Fatalf("first request: %d %s", code, out)
	}
	// the primary is still failing, but within a minute it is not asked again
	if code, out := fbDo(s, chatBody); code != 200 {
		t.Fatalf("second request: %d %s", code, out)
	}
	if got := strings.Join(f.asked(), ","); got != "m1,m2,m2" {
		t.Errorf("upstream asked for %s; the resting primary should be skipped", got)
	}
}

func TestStreamingRequestFallsBackBeforeAnyReply(t *testing.T) {
	f, s := fbSetup(t, map[string]fbFail{"m1": {429, "rate limit exceeded"}})
	body := `{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	code, out := fbDo(s, body)
	if code != 200 || !strings.Contains(out, `"ok"`) {
		t.Fatalf("status %d: %s; want the fallback's stream", code, out)
	}
	if got := strings.Join(f.asked(), ","); got != "m1,m2" {
		t.Errorf("upstream asked for %s", got)
	}
}
