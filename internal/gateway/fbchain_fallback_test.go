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

// fbchainVendor is a vendor that fails with a fixed status, streams a fixed
// reply, or answers 200 with its own name. It logs the model of each request.
type fbchainVendor struct {
	name   string
	status int    // 0 answers 200
	msg    string // the error message when status is set
	stream string // when set, a 200 event-stream reply with this body

	mu   sync.Mutex
	seen []string
}

func (v *fbchainVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &req)
	v.mu.Lock()
	v.seen = append(v.seen, req.Model)
	v.mu.Unlock()
	switch {
	case v.status != 0:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(v.status)
		io.WriteString(w, `{"error":{"message":"`+v.msg+`"}}`)
	case v.stream != "":
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, v.stream)
	default:
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"`+req.Model+`","choices":[{"index":0,"message":{"role":"assistant","content":"ok from `+v.name+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}
}

// got lists the models this vendor was asked for, in order.
func (v *fbchainVendor) got() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.seen...)
}

// fbchainEnv keeps the test's providers and catalog cache out of the user's files.
func fbchainEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// fbchainProvider starts a vendor and saves it as a provider named id, with a
// Chat base, two exposed models and the given fallback list.
func fbchainProvider(t *testing.T, id string, v *fbchainVendor, fallback ...string) {
	t.Helper()
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: id, Chat: up.URL + "/v1", Key: "sk-" + id,
		Models: []string{"m1", "m2"}, Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
}

const fbchainChat = `{"model":"pri/m1","messages":[{"role":"user","content":"hi"}]}`

func TestFbchainQuotaFailureMovesToFallbackModel(t *testing.T) {
	fbchainEnv(t)
	pri := &fbchainVendor{name: "pri", status: 429, msg: "rate limit exceeded"}
	fb := &fbchainVendor{name: "fb"}
	fbchainProvider(t, "fb", fb)
	fbchainProvider(t, "pri", pri, "fb/m2")

	s := New()
	rec := mkPost(t, s, "/v1/chat/completions", fbchainChat)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ok from fb") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(pri.got(), ","); got != "m1" {
		t.Fatalf("primary was asked for %q", got)
	}
	// the upstream gets the fallback's own model name, not the primary's
	if got := strings.Join(fb.got(), ","); got != "m2" {
		t.Fatalf("fallback was asked for %q, want its own name m2", got)
	}
	calls := s.Recent()
	if len(calls) == 0 || calls[0].Provider != "fb" || calls[0].Status != 200 {
		t.Fatalf("call record %+v", calls)
	}
	if !strings.Contains(calls[0].Fallback, "pri") {
		t.Fatalf("the call should name the failed provider, got %q", calls[0].Fallback)
	}
}

func TestFbchainRetryableStatusesMoveOn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		msg    string
	}{
		{"402", 402, "payment required"},
		{"429", 429, "slow down"},
		{"529", 529, "overloaded"},
		{"503", 503, "service unavailable"},
		{"403 out of quota", 403, "quota exhausted for this key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fbchainEnv(t)
			pri := &fbchainVendor{name: "pri", status: tc.status, msg: tc.msg}
			fb := &fbchainVendor{name: "fb"}
			fbchainProvider(t, "fb", fb)
			fbchainProvider(t, "pri", pri, "fb/m2")
			rec := mkPost(t, New(), "/v1/chat/completions", fbchainChat)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ok from fb") {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestFbchainClientErrorsStayWithTheClient(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		msg    string
	}{
		{"400", 400, "bad request"},
		{"403 forbidden for this model", 403, "forbidden for this model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fbchainEnv(t)
			pri := &fbchainVendor{name: "pri", status: tc.status, msg: tc.msg}
			fb := &fbchainVendor{name: "fb"}
			fbchainProvider(t, "fb", fb)
			fbchainProvider(t, "pri", pri, "fb/m2")
			rec := mkPost(t, New(), "/v1/chat/completions", fbchainChat)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.msg) {
				t.Fatalf("status %d, want %d with %q: %s", rec.Code, tc.status, tc.msg, rec.Body.String())
			}
			if got := fb.got(); len(got) != 0 {
				t.Fatalf("an error that is not a quota or rate limit must not fall back, fallback saw %v", got)
			}
		})
	}
}

func TestFbchainLastCandidateErrorIsReturned(t *testing.T) {
	fbchainEnv(t)
	pri := &fbchainVendor{name: "pri", status: 429, msg: "primary limited"}
	fb := &fbchainVendor{name: "fb", status: 503, msg: "fallback down"}
	fbchainProvider(t, "fb", fb)
	fbchainProvider(t, "pri", pri, "fb/m2")
	s := New()
	rec := mkPost(t, s, "/v1/chat/completions", fbchainChat)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "fallback down") {
		t.Fatalf("the last candidate's error should reach the client, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "primary limited") {
		t.Fatalf("the first candidate's error leaked to the client: %s", rec.Body.String())
	}
	if calls := s.Recent(); len(calls) == 0 || calls[0].Status != 503 {
		t.Fatalf("call record %+v", calls)
	}
}

func TestFbchainFailedProviderIsSkippedForAWhile(t *testing.T) {
	fbchainEnv(t)
	pri := &fbchainVendor{name: "pri", status: 429, msg: "rate limit exceeded"}
	fb := &fbchainVendor{name: "fb"}
	fbchainProvider(t, "fb", fb)
	fbchainProvider(t, "pri", pri, "fb/m2")
	s := New()
	mkPost(t, s, "/v1/chat/completions", fbchainChat)
	rec := mkPost(t, s, "/v1/chat/completions", fbchainChat)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ok from fb") {
		t.Fatalf("second status %d: %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(pri.got(), ","); got != "m1" {
		t.Fatalf("the failed provider should be skipped, primary saw %q", got)
	}
	if got := strings.Join(fb.got(), ","); got != "m2,m2" {
		t.Fatalf("fallback saw %q", got)
	}
}

func TestFbchainStreamStartFailureMovesOn(t *testing.T) {
	fbchainEnv(t)
	pri := &fbchainVendor{name: "pri", stream: "data: {\"error\":{\"message\":\"rate limit reached\",\"type\":\"rate_limit_error\"}}\n\n"}
	fb := &fbchainVendor{name: "fb", stream: "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi from fb\"},\"finish_reason\":null}]}\n\n" +
		"data: [DONE]\n\n"}
	fbchainProvider(t, "fb", fb)
	fbchainProvider(t, "pri", pri, "fb/m2")
	rec := mkPost(t, New(), "/v1/chat/completions", `{"model":"pri/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi from fb") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "rate limit reached") {
		t.Fatalf("the failed first event reached the client: %s", rec.Body.String())
	}
	if got := strings.Join(fb.got(), ","); got != "m2" {
		t.Fatalf("fallback saw %q", got)
	}
}

func TestFbchainStreamErrorThatIsNotAQuotaPassesThrough(t *testing.T) {
	fbchainEnv(t)
	pri := &fbchainVendor{name: "pri", stream: "data: {\"error\":{\"message\":\"bad input\",\"type\":\"invalid_request_error\"}}\n\n"}
	fb := &fbchainVendor{name: "fb"}
	fbchainProvider(t, "fb", fb)
	fbchainProvider(t, "pri", pri, "fb/m2")
	rec := mkPost(t, New(), "/v1/chat/completions", `{"model":"pri/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(rec.Body.String(), "bad input") {
		t.Fatalf("the client should see its own error, got %s", rec.Body.String())
	}
	if got := fb.got(); len(got) != 0 {
		t.Fatalf("a non-quota stream error must not fall back, fallback saw %v", got)
	}
}

func TestFbchainUnreadableFallbackEntriesAreSkipped(t *testing.T) {
	fbchainEnv(t)
	pri := &fbchainVendor{name: "pri", status: 429, msg: "rate limit exceeded"}
	fb := &fbchainVendor{name: "fb"}
	fbchainProvider(t, "fb", fb)
	fbchainProvider(t, "pri", pri, "nosuch/m9", "garbage", "fb/m2")
	rec := mkPost(t, New(), "/v1/chat/completions", fbchainChat)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ok from fb") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}
