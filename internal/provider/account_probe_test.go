package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

// recorded is one request a stand-in backend received.
type recorded struct {
	path   string
	header http.Header
	body   map[string]any
}

// recorder collects requests seen by a stand-in backend.
type recorder struct {
	mu   sync.Mutex
	reqs []recorded
}

func (r *recorder) add(req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, recorded{path: req.URL.Path, header: req.Header.Clone(), body: m})
}

func (r *recorder) all() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.reqs...)
}

// TestProbeCodexAccountUsesSignedResponses checks that the connectivity test of
// a Codex account sends a streamed, unstored Responses request with the
// account's token and id, to the ChatGPT backend.
func TestProbeCodexAccountUsesSignedResponses(t *testing.T) {
	setupEnv(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})

	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"response"}`))
	}))
	t.Cleanup(srv.Close)
	old := CodexBase
	CodexBase = srv.URL + "/backend-api/codex/"
	t.Cleanup(func() { CodexBase = old })

	p := findAccount(Accounts(), "codex")
	if p == nil {
		t.Fatal("codex account not listed")
	}
	want := ""
	if ms := p.Available(); len(ms) > 0 {
		want = ms[0].ID
	}

	res := p.Test(context.Background())
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(res), res)
	}
	if !res[0].OK {
		t.Fatalf("probe failed: %+v", res[0])
	}
	if res[0].Protocol != Responses {
		t.Errorf("protocol = %v, want Responses", res[0].Protocol)
	}
	if res[0].Model != want || want == "" {
		t.Errorf("model = %q, want first available %q", res[0].Model, want)
	}

	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("backend saw %d requests, want 1", len(got))
	}
	r := got[0]
	if r.path != "/backend-api/codex/responses" {
		t.Errorf("path = %s", r.path)
	}
	if v := r.header.Get("Authorization"); v != "Bearer codex-access-SECRET" {
		t.Errorf("Authorization = %q", v)
	}
	if v := r.header.Get("chatgpt-account-id"); v != "acct-123" {
		t.Errorf("chatgpt-account-id = %q", v)
	}
	if r.body["stream"] != true {
		t.Errorf("stream = %v, want true", r.body["stream"])
	}
	if r.body["store"] != false {
		t.Errorf("store = %v, want false", r.body["store"])
	}
	if _, ok := r.body["input"].([]any); !ok {
		t.Errorf("input = %#v, want a list of items", r.body["input"])
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := r.body[k]; ok {
			t.Errorf("%s must be removed before the backend", k)
		}
	}
	if m, _ := r.body["model"].(string); m != want {
		t.Errorf("model in body = %q, want %q", m, want)
	}
}

// TestProbeCodexAccountSignedOutReportsError checks that a Codex account whose
// sign-in is gone reports an error and sends nothing.
func TestProbeCodexAccountSignedOutReportsError(t *testing.T) {
	setupEnv(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})

	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	old := CodexBase
	CodexBase = srv.URL + "/backend-api/codex/"
	t.Cleanup(func() { CodexBase = old })

	p := findAccount(Accounts(), "codex")
	if p == nil {
		t.Fatal("codex account not listed")
	}
	if err := os.Remove(homeFile(".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}

	res := p.Test(context.Background())
	if len(res) != 1 || res[0].OK || res[0].Error == "" {
		t.Fatalf("want one failed result with an error, got %+v", res)
	}
	if n := len(rec.all()); n != 0 {
		t.Errorf("backend saw %d requests after sign-out, want 0", n)
	}
}

// TestProbeCopilotAccountUsesSessionChat checks that the connectivity test of
// a Copilot account sends a Chat Completions request through the session's
// endpoint with the session token and the user-initiated marker.
func TestProbeCopilotAccountUsesSessionChat(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCopilot(t, "apps.json", `{"github.com:x":{"user":"octocat","oauth_token":"gho_refresh"}}`)

	var rec recorder
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			if r.Header.Get("Authorization") != "token gho_refresh" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "sess-probe",
				"expires_at": time.Now().Add(time.Hour).Unix(),
				"endpoints":  map[string]any{"api": srv.URL + "/api"},
			})
		case "/api/chat/completions":
			rec.add(r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[]}`))
		default:
			// The model list is not what this test is about; refusing it
			// leaves the built-in list in place.
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { CopilotTokenURL = old })

	p := copilotAccount(t)
	res := p.Test(context.Background())
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(res), res)
	}
	if !res[0].OK {
		t.Fatalf("probe failed: %+v", res[0])
	}
	if res[0].Protocol != Chat {
		t.Errorf("protocol = %v, want Chat", res[0].Protocol)
	}
	if res[0].Model == "" {
		t.Error("probe chose no model")
	}

	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("session endpoint saw %d chat requests, want 1", len(got))
	}
	r := got[0]
	if r.path != "/api/chat/completions" {
		t.Errorf("path = %s", r.path)
	}
	if v := r.header.Get("Authorization"); v != "Bearer sess-probe" {
		t.Errorf("Authorization = %q", v)
	}
	if r.header.Get("Copilot-Integration-Id") == "" {
		t.Error("Copilot-Integration-Id must be set")
	}
	if v := r.header.Get("X-Initiator"); v != "user" {
		t.Errorf("X-Initiator = %q, want user", v)
	}
	if m, _ := r.body["model"].(string); m != res[0].Model {
		t.Errorf("model in body = %q, want %q", m, res[0].Model)
	}
}

// TestProbePlainProviderUnchanged checks that an ordinary provider is still
// probed with its own key on the protocols it serves.
func TestProbePlainProviderUnchanged(t *testing.T) {
	setupEnv(t)
	isolateCache(t)

	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(srv.Close)

	p := Provider{ID: "plainp", Name: "Plain", Chat: srv.URL + "/v1", Key: "sk-plain", Models: []string{"m1"}}
	res := p.Test(context.Background())
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(res), res)
	}
	if !res[0].OK || res[0].Protocol != Chat || res[0].Model != "m1" {
		t.Errorf("unexpected result: %+v", res[0])
	}

	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(got))
	}
	if got[0].path != "/v1/chat/completions" {
		t.Errorf("path = %s", got[0].path)
	}
	if v := got[0].header.Get("Authorization"); v != "Bearer sk-plain" {
		t.Errorf("Authorization = %q", v)
	}
	if v := got[0].header.Get("X-Initiator"); v != "" {
		t.Errorf("plain provider must not get X-Initiator, got %q", v)
	}
}
