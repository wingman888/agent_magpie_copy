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
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// refusal is how the fake upstream turns one key away.
type refusal struct {
	status int
	msg    string
}

// modelFake is an upstream that records "key model" for every request and
// refuses the keys listed in refuse; the others get a streamed chat reply.
type modelFake struct {
	mu     sync.Mutex
	refuse map[string]refusal
	seen   []string
}

func (f *modelFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var req struct {
		Model string `json:"model"`
	}
	json.Unmarshal(b, &req)
	f.mu.Lock()
	f.seen = append(f.seen, key+" "+req.Model)
	ref, refused := f.refuse[key]
	f.mu.Unlock()
	if refused {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(ref.status)
		io.WriteString(w, `{"error":{"message":"`+ref.msg+`"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, chatReply)
}

func (f *modelFake) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *modelFake) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = nil
}

func (f *modelFake) setRefuse(key string, ref refusal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse[key] = ref
}

// fallbackSetup saves "fake" (key k1, model m1) with the given fallback list,
// and "other" (key k2) on the same upstream. The clock is the returned pointer.
func fallbackSetup(t *testing.T, up *httptest.Server, fallback []string) *time.Time {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, p := range []provider.Provider{
		{ID: "fake", Name: "Fake", Models: []string{"m1"}, Chat: up.URL + "/v1", Key: "k1", Fallback: fallback},
		{ID: "other", Name: "Other", Models: []string{"m2"}, Chat: up.URL + "/v1", Key: "k2"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	return &now
}

// sendModel posts a streamed chat request for fake/m1 and returns the reply.
func sendModel(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatClientBody)))
	return rec
}

func newModelFake() *modelFake { return &modelFake{refuse: map[string]refusal{}} }

// Refusals that mean "try the next one" end in the fallback's answer, which
// the client never sees as an error; the upstream gets the fallback's model.
func TestFallbackAnswersRefusedPrimary(t *testing.T) {
	cases := map[string]refusal{
		"rate limited": {429, "slow down"},
		"payment":      {402, "payment required"},
		"overloaded":   {529, "overloaded"},
		"unavailable":  {503, "unavailable"},
		"quota 403":    {403, "You exceeded your current quota"},
		"balance 403":  {403, "insufficient balance"},
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			f := newModelFake()
			f.setRefuse("k1", ref)
			up := httptest.NewServer(f)
			defer up.Close()
			fallbackSetup(t, up, []string{"other/m2"})
			s := New()
			rec := sendModel(t, s)
			if rec.Code != 200 || strings.Contains(rec.Body.String(), ref.msg) {
				t.Fatalf("client got %d %q, want the fallback's reply", rec.Code, rec.Body.String())
			}
			if want := []string{"k1 m1", "k2 m2"}; !reflect.DeepEqual(f.calls(), want) {
				t.Fatalf("upstream saw %v, want %v", f.calls(), want)
			}
			c := s.Recent()[0]
			if c.Provider != "other" || c.Status != 200 || c.Fallback != "fake" {
				t.Fatalf("call = provider %q status %d fallback %q", c.Provider, c.Status, c.Fallback)
			}
		})
	}
}

// Refusals that are the caller's fault stay where they are: no fallback.
func TestFallbackNotUsedForClientErrors(t *testing.T) {
	f := newModelFake()
	f.setRefuse("k1", refusal{400, "bad request"})
	up := httptest.NewServer(f)
	defer up.Close()
	fallbackSetup(t, up, []string{"other/m2"})
	s := New()
	rec := sendModel(t, s)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "bad request") {
		t.Fatalf("client got %d %q, want the 400 as it was", rec.Code, rec.Body.String())
	}
	if want := []string{"k1 m1"}; !reflect.DeepEqual(f.calls(), want) {
		t.Fatalf("upstream saw %v, want %v", f.calls(), want)
	}
	if c := s.Recent()[0]; c.Fallback != "" || c.Provider != "fake" {
		t.Fatalf("call = provider %q fallback %q", c.Provider, c.Fallback)
	}
}

// When every candidate is refused, the last one's error goes to the client.
func TestLastCandidateErrorIsReturned(t *testing.T) {
	f := newModelFake()
	f.setRefuse("k1", refusal{429, "primary busy"})
	f.setRefuse("k2", refusal{503, "other down"})
	up := httptest.NewServer(f)
	defer up.Close()
	fallbackSetup(t, up, []string{"other/m2"})
	s := New()
	rec := sendModel(t, s)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "other down") {
		t.Fatalf("client got %d %q, want the fallback's 503", rec.Code, rec.Body.String())
	}
	c := s.Recent()[0]
	if c.Status != 503 || c.Provider != "other" || c.Fallback != "fake" {
		t.Fatalf("call = status %d provider %q fallback %q", c.Status, c.Provider, c.Fallback)
	}
}

// A refused provider rests for a minute: the next request goes straight to the
// fallback, and after the minute the provider is tried first again.
func TestRefusedProviderIsSkippedForAMinute(t *testing.T) {
	f := newModelFake()
	f.setRefuse("k1", refusal{429, "slow down"})
	up := httptest.NewServer(f)
	defer up.Close()
	now := fallbackSetup(t, up, []string{"other/m2"})
	s := New()
	s.clock = func() time.Time { return *now }

	sendModel(t, s)
	f.reset()
	*now = now.Add(30 * time.Second)
	if rec := sendModel(t, s); rec.Code != 200 {
		t.Fatalf("second request: client got %d", rec.Code)
	}
	if want := []string{"k2 m2"}; !reflect.DeepEqual(f.calls(), want) {
		t.Fatalf("while resting, upstream saw %v, want %v", f.calls(), want)
	}

	f.reset()
	*now = now.Add(31 * time.Second)
	sendModel(t, s)
	if want := []string{"k1 m1", "k2 m2"}; !reflect.DeepEqual(f.calls(), want) {
		t.Fatalf("after resting, upstream saw %v, want %v", f.calls(), want)
	}
}

// A resting candidate is still tried when nothing else is left.
func TestRestingCandidateIsTriedLastResort(t *testing.T) {
	f := newModelFake()
	f.setRefuse("k1", refusal{429, "slow down"})
	up := httptest.NewServer(f)
	defer up.Close()
	now := fallbackSetup(t, up, nil)
	s := New()
	s.clock = func() time.Time { return *now }

	if rec := sendModel(t, s); rec.Code != 429 {
		t.Fatalf("first request: client got %d, want 429", rec.Code)
	}
	f.reset()
	if rec := sendModel(t, s); rec.Code != 429 {
		t.Fatalf("second request: client got %d, want 429", rec.Code)
	}
	if want := []string{"k1 m1"}; !reflect.DeepEqual(f.calls(), want) {
		t.Fatalf("upstream saw %v, want %v", f.calls(), want)
	}
}

// Entries that do not name a usable provider and model are skipped silently.
func TestUnresolvableFallbackEntriesAreSkipped(t *testing.T) {
	f := newModelFake()
	f.setRefuse("k1", refusal{429, "slow down"})
	up := httptest.NewServer(f)
	defer up.Close()
	fallbackSetup(t, up, []string{"nonsense", "nobody/m3", "other/", " /m9", "other/m2"})
	s := New()
	if rec := sendModel(t, s); rec.Code != 200 {
		t.Fatalf("client got %d, want the fallback's reply", rec.Code)
	}
	if want := []string{"k1 m1", "k2 m2"}; !reflect.DeepEqual(f.calls(), want) {
		t.Fatalf("upstream saw %v, want %v", f.calls(), want)
	}
}
