package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// fbxFail is the reply the upstream gives a model that is set to fail.
type fbxFail struct {
	code int
	body string
}

// fbxUpstream answers by the model it was sent. A model in fail gets that
// reply; every other model a plain completion (an event stream when asked).
// It records the model of each request, in order.
type fbxUpstream struct {
	mu   sync.Mutex
	fail map[string]fbxFail
	hits []string
}

func (u *fbxUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var v struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	json.Unmarshal(raw, &v)
	u.mu.Lock()
	u.hits = append(u.hits, v.Model)
	f, bad := u.fail[v.Model]
	u.mu.Unlock()
	if bad {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.code)
		io.WriteString(w, f.body)
		return
	}
	if v.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"x","object":"chat.completion.chunk","model":"`+v.Model+`","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`+"\n\n"+"data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"x","object":"chat.completion","model":"`+v.Model+`","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
}

func (u *fbxUpstream) seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.hits...)
}

// fbxSetup points magpie's config at a temp dir and saves two providers on one
// upstream: "relay" (model m1, falls back to "backup/m2") and "backup" (m2).
func fbxSetup(t *testing.T, fail map[string]fbxFail, fallback []string) *fbxUpstream {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := &fbxUpstream{fail: fail}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "backup", Name: "Backup", Key: "k2", Chat: srv.URL + "/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: srv.URL + "/v1", Models: []string{"m1"}, Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
	return up
}

func fbxPost(s *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func fbxCheckHits(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream saw models %q, want %q", got, want)
	}
}

const fbxChat = `{"model":"relay/m1","messages":[{"role":"user","content":"hi"}]}`

func TestFbxRateLimitedFallsBackWithItsOwnModel(t *testing.T) {
	// the unresolvable entry is skipped, and the good one is tried once
	up := fbxSetup(t, map[string]fbxFail{"m1": {429, `{"error":{"message":"rate limited"}}`}},
		[]string{"nope/zz", "backup/m2"})
	s := New()
	if code, out := fbxPost(s, "/v1/chat/completions", fbxChat); code != 200 {
		t.Fatalf("request: %d %s", code, out)
	}
	call := s.Recent()[0]
	if call.Status != 200 || call.Provider != "backup" || call.Fallback != "relay" {
		t.Fatalf("call record: %+v", call)
	}
	fbxCheckHits(t, up.seen(), "m1", "m2")
}

func TestFbxFailoverStatuses(t *testing.T) {
	cases := []struct {
		name string
		f    fbxFail
	}{
		{"402", fbxFail{402, `{"error":{"message":"payment required"}}`}},
		{"529", fbxFail{529, `{"error":{"message":"overloaded"}}`}},
		{"403 quota", fbxFail{403, `{"error":{"message":"quota exhausted for this key"}}`}},
		{"403 额度已用完", fbxFail{403, `{"error":{"message":"额度已用完"}}`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			up := fbxSetup(t, map[string]fbxFail{"m1": c.f}, []string{"backup/m2"})
			s := New()
			if code, out := fbxPost(s, "/v1/chat/completions", fbxChat); code != 200 {
				t.Fatalf("request: %d %s", code, out)
			}
			fbxCheckHits(t, up.seen(), "m1", "m2")
		})
	}
}

func TestFbxBadRequestDoesNotFallBack(t *testing.T) {
	up := fbxSetup(t, map[string]fbxFail{"m1": {400, `{"error":{"message":"bad parameter"}}`}}, []string{"backup/m2"})
	s := New()
	code, out := fbxPost(s, "/v1/chat/completions", fbxChat)
	if code != 400 || !strings.Contains(out, "bad parameter") {
		t.Fatalf("want the 400 returned as is, got %d %s", code, out)
	}
	fbxCheckHits(t, up.seen(), "m1")
	if c := s.Recent()[0]; c.Fallback != "" || c.Provider != "relay" {
		t.Fatalf("call record: %+v", c)
	}
}

func TestFbxLastCandidateErrorReturned(t *testing.T) {
	up := fbxSetup(t, map[string]fbxFail{
		"m1": {429, `{"error":{"message":"rate limited"}}`},
		"m2": {503, `{"error":{"message":"backup is down"}}`},
	}, []string{"backup/m2"})
	s := New()
	code, out := fbxPost(s, "/v1/chat/completions", fbxChat)
	if code != 503 || !strings.Contains(out, "backup is down") {
		t.Fatalf("want the last candidate's error, got %d %s", code, out)
	}
	fbxCheckHits(t, up.seen(), "m1", "m2")
}

func TestFbxFailedProviderSkippedWithinCooldown(t *testing.T) {
	up := fbxSetup(t, map[string]fbxFail{"m1": {429, `{"error":{"message":"rate limited"}}`}}, []string{"backup/m2"})
	s := New()
	if code, out := fbxPost(s, "/v1/chat/completions", fbxChat); code != 200 {
		t.Fatalf("first request: %d %s", code, out)
	}
	// relay is resting, so the next request goes straight to the fallback
	if code, out := fbxPost(s, "/v1/chat/completions", fbxChat); code != 200 {
		t.Fatalf("second request: %d %s", code, out)
	}
	if c := s.Recent()[0]; c.Fallback != "" || c.Provider != "backup" {
		t.Fatalf("second request should go straight to backup: %+v", c)
	}
	fbxCheckHits(t, up.seen(), "m1", "m2", "m2")
}

func TestFbxStreamOpeningErrorFallsBack(t *testing.T) {
	up := fbxSetup(t, map[string]fbxFail{"m1": {429, `{"error":{"message":"rate limited"}}`}}, []string{"backup/m2"})
	s := New()
	body := `{"model":"relay/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	code, out := fbxPost(s, "/v1/chat/completions", body)
	if code != 200 || !strings.Contains(out, "hi") {
		t.Fatalf("stream request: %d %s", code, out)
	}
	if c := s.Recent()[0]; c.Provider != "backup" || c.Fallback != "relay" {
		t.Fatalf("call record: %+v", c)
	}
	fbxCheckHits(t, up.seen(), "m1", "m2")
}

func TestFbxTranslatedRequestFallsBack(t *testing.T) {
	// the relay speaks only Chat, so an Anthropic client's request is translated
	up := fbxSetup(t, map[string]fbxFail{"m1": {429, `{"error":{"message":"rate limited"}}`}}, []string{"backup/m2"})
	s := New()
	body := `{"model":"relay/m1","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`
	code, out := fbxPost(s, "/v1/messages", body)
	if code != 200 || !strings.Contains(out, "hi") {
		t.Fatalf("translated request: %d %s", code, out)
	}
	c := s.Recent()[0]
	if c.Provider != "backup" || c.Fallback != "relay" || c.To != provider.Chat {
		t.Fatalf("call record: %+v", c)
	}
	fbxCheckHits(t, up.seen(), "m1", "m2")
}
