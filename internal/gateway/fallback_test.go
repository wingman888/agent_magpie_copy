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

// okStream is a whole chat reply, streamed.
const okStream = `data: {"id":"c1","object":"chat.completion.chunk","model":"m2","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"c1","object":"chat.completion.chunk","model":"m2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}` + "\n\n" +
	"data: [DONE]\n\n"

// scripted is an upstream that answers by the API key it is sent. A key in
// codes gets that status with the message in body (a rate-limit message when
// none is given); a key in streamErr gets a stream whose first event is an
// error; any other key a normal reply, streamed when the request asks for it.
// It records the model names and keys it was sent.
type scripted struct {
	mu        sync.Mutex
	codes     map[string]int
	body      map[string]string
	streamErr map[string]bool
	models    []string
	keys      []string
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	json.Unmarshal(b, &req)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	s.models = append(s.models, req.Model)
	s.keys = append(s.keys, key)
	code, msg, errStream := s.codes[key], s.body[key], s.streamErr[key]
	s.mu.Unlock()
	switch {
	case code != 0:
		if msg == "" {
			msg = `{"error":{"message":"rate limit exceeded"}}`
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, msg)
	case errStream:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"error":{"message":"rate limit exceeded"}}`+"\n\n")
	case req.Stream:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, okStream)
	default:
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okChat)
	}
}

// took returns the model names and keys sent since the last call, and forgets them.
func (s *scripted) took() (models, keys []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	models, keys = s.models, s.keys
	s.models, s.keys = nil, nil
	return models, keys
}

// fallbackSetup makes a provider "prim" (key k1, model m1) and a provider
// "back" (key k2, model m2), each a Chat endpoint on its own upstream, and
// sets prim's fallback to back/m2.
func fallbackSetup(t *testing.T, prim, back *scripted) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	pu := httptest.NewServer(prim)
	t.Cleanup(pu.Close)
	bu := httptest.NewServer(back)
	t.Cleanup(bu.Close)
	if err := provider.Save(provider.Provider{ID: "prim", Name: "Prim", Key: "k1", Models: []string{"m1"}, Chat: pu.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "back", Name: "Back", Key: "k2", Models: []string{"m2"}, Chat: bu.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetFallback("prim", []string{"back/m2"}); err != nil {
		t.Fatal(err)
	}
}

// postTo sends one request through the handler and returns the recorder.
func postTo(h http.Handler, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec
}

const primChat = `{"model":"prim/m1","messages":[{"role":"user","content":"hi"}]}`

// Quota, rate-limit and overload replies move the request to the fallback
// model, and the upstream is sent that model's own name.
func TestFallbackOnQuotaAndOverload(t *testing.T) {
	cases := []struct {
		name string
		code int
		msg  string
	}{
		{"rate limited", 429, ""},
		{"payment required", 402, `{"error":{"message":"insufficient balance"}}`},
		{"overloaded", 529, `{"error":{"message":"overloaded"}}`},
		{"quota as 403", 403, `{"error":{"message":"quota exhausted for this key"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prim := &scripted{codes: map[string]int{"k1": c.code}, body: map[string]string{"k1": c.msg}}
			back := &scripted{}
			fallbackSetup(t, prim, back)
			srv := New()
			rec := postTo(srv.Handler(), "/v1/chat/completions", primChat)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			pm, _ := prim.took()
			bm, bk := back.took()
			if strings.Join(pm, ",") != "m1" || strings.Join(bm, ",") != "m2" || strings.Join(bk, ",") != "k2" {
				t.Fatalf("primary sent %v, fallback sent %v with keys %v; want m1, then m2 with k2", pm, bm, bk)
			}
			calls := srv.Recent()
			if len(calls) == 0 || calls[0].Provider != "back" || calls[0].Status != 200 || !strings.Contains(calls[0].Fallback, "prim") {
				t.Fatalf("call record = %+v", calls)
			}
		})
	}
}

// A bad request is the client's error: it is not retried elsewhere.
func TestBadRequestIsNotFallenBack(t *testing.T) {
	prim := &scripted{codes: map[string]int{"k1": 400}, body: map[string]string{"k1": `{"error":{"message":"bad param"}}`}}
	back := &scripted{}
	fallbackSetup(t, prim, back)
	srv := New()
	rec := postTo(srv.Handler(), "/v1/chat/completions", primChat)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "bad param") {
		t.Fatalf("status %d, body %s; want the 400 passed through", rec.Code, rec.Body.String())
	}
	if _, bk := back.took(); len(bk) != 0 {
		t.Fatalf("fallback was contacted with keys %v", bk)
	}
	if calls := srv.Recent(); len(calls) == 0 || calls[0].Fallback != "" {
		t.Fatalf("call record = %+v; nothing should have failed before", calls)
	}
}

// When the fallback fails too, the client gets the last candidate's error.
func TestLastCandidateErrorReachesClient(t *testing.T) {
	prim := &scripted{codes: map[string]int{"k1": 429}}
	back := &scripted{codes: map[string]int{"k2": 402}, body: map[string]string{"k2": `{"error":{"message":"insufficient balance"}}`}}
	fallbackSetup(t, prim, back)
	srv := New()
	rec := postTo(srv.Handler(), "/v1/chat/completions", primChat)
	if rec.Code != 402 || !strings.Contains(rec.Body.String(), "insufficient") {
		t.Fatalf("status %d, body %s; want the fallback's 402", rec.Code, rec.Body.String())
	}
	calls := srv.Recent()
	if len(calls) == 0 || calls[0].Provider != "back" || calls[0].Status != 402 || !strings.Contains(calls[0].Fallback, "prim") {
		t.Fatalf("call record = %+v", calls)
	}
}

// A provider that just failed is passed over for about a minute: the next
// request goes straight to the fallback.
func TestFailedProviderSkippedWhileResting(t *testing.T) {
	prim := &scripted{codes: map[string]int{"k1": 429}}
	back := &scripted{}
	fallbackSetup(t, prim, back)
	h := New().Handler()
	if rec := postTo(h, "/v1/chat/completions", primChat); rec.Code != 200 {
		t.Fatalf("first status %d", rec.Code)
	}
	if pm, _ := prim.took(); strings.Join(pm, ",") != "m1" {
		t.Fatalf("first request sent primary %v, want m1", pm)
	}
	if rec := postTo(h, "/v1/chat/completions", primChat); rec.Code != 200 {
		t.Fatalf("second status %d", rec.Code)
	}
	if pm, _ := prim.took(); len(pm) != 0 {
		t.Fatalf("second request reached the resting primary with %v", pm)
	}
}

// Call.Fallback names each failed key by its provider and key name; the
// provider in Call.Provider is the one that answered.
func TestFallbackRecordNamesFailedKeys(t *testing.T) {
	prim := &scripted{codes: map[string]int{"k1": 429, "k3": 429}}
	back := &scripted{}
	fallbackSetup(t, prim, back)
	if err := provider.RenameKey("prim", provider.KeyRef("k1"), "own"); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("prim", "team", "k3", ""); err != nil {
		t.Fatal(err)
	}
	srv := New()
	if rec := postTo(srv.Handler(), "/v1/chat/completions", primChat); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	calls := srv.Recent()
	if len(calls) == 0 || calls[0].Provider != "back" || calls[0].Fallback != "prim (own), prim (team)" {
		t.Fatalf("call record = %+v; want Provider back and Fallback %q", calls, "prim (own), prim (team)")
	}
}

// An error in the first event of a stream is retried on the fallback before
// anything reaches the client, when the client asked for a translated stream.
func TestStreamOpeningErrorMovesToFallback(t *testing.T) {
	prim := &scripted{streamErr: map[string]bool{"k1": true}}
	back := &scripted{}
	fallbackSetup(t, prim, back)
	srv := New()
	body := `{"model":"prim/m1","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := postTo(srv.Handler(), "/v1/messages", body)
	out := rec.Body.String()
	if rec.Code != 200 || strings.Contains(out, "rate limit") || !strings.Contains(out, "hi") {
		t.Fatalf("status %d, body %s; want the fallback's reply only", rec.Code, out)
	}
	pm, _ := prim.took()
	bm, bk := back.took()
	if strings.Join(pm, ",") != "m1" || strings.Join(bm, ",") != "m2" || strings.Join(bk, ",") != "k2" {
		t.Fatalf("primary sent %v, fallback sent %v with %v", pm, bm, bk)
	}
	calls := srv.Recent()
	if len(calls) == 0 || calls[0].Provider != "back" || !strings.Contains(calls[0].Fallback, "prim") {
		t.Fatalf("call record = %+v", calls)
	}
}

// Fallback entries that cannot be read are skipped, and duplicates are dropped.
func TestUnreadableFallbackEntriesSkipped(t *testing.T) {
	prim := &scripted{codes: map[string]int{"k1": 429}}
	back := &scripted{}
	fallbackSetup(t, prim, back)
	if err := provider.SetFallback("prim", []string{" back/m2 ", "back/m2", "", "nonsense", "nobody/x"}); err != nil {
		t.Fatal(err)
	}
	if rec := postTo(New().Handler(), "/v1/chat/completions", primChat); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if bm, _ := back.took(); strings.Join(bm, ",") != "m2" {
		t.Fatalf("fallback sent %v, want one m2", bm)
	}
}
