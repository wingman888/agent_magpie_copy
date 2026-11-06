package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// keyedFake is an upstream that tells keys apart: it records "path key" for
// every request and answers with the status set for that key (200 by default).
type keyedFake struct {
	mu     sync.Mutex
	seen   []string
	status map[string]int
}

const anthropicReply = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"claude-x","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

const chatReply = `data: {"id":"c1","object":"chat.completion.chunk","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}` + "\n\n" +
	"data: [DONE]\n\n"

func (f *keyedFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if key == "" {
		key = r.Header.Get("x-api-key")
	}
	f.mu.Lock()
	f.seen = append(f.seen, r.URL.Path+" "+key)
	code := f.status[key]
	f.mu.Unlock()
	if code == 0 {
		code = http.StatusOK
	}
	if code != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"refused for this key"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if r.URL.Path == "/v1/messages" {
		io.WriteString(w, anthropicReply)
		return
	}
	io.WriteString(w, chatReply)
}

// calls returns what the upstream saw since the last reset.
func (f *keyedFake) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *keyedFake) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = nil
}

// keyedSetup saves provider "fake" with a Chat base and an Anthropic base both
// on the fake, and the given keys, the first in use.
func keyedSetup(t *testing.T, up *httptest.Server, keys ...provider.KeyEntry) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := provider.Provider{ID: "fake", Name: "Fake", Models: []string{"m1", "claude-x", "gpt-5"},
		Chat: up.URL + "/v1", Anthropic: up.URL,
		Key: keys[0].Key, KeyName: keys[0].Name, KeyProtocol: keys[0].Protocol, Keys: keys[1:]}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func sendKeyed(t *testing.T, s *Server, path, body string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code
}

const chatClientBody = `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}],"stream":true}`

func TestFirstKeyRateLimitedSecondKeyAnswers(t *testing.T) {
	f := &keyedFake{status: map[string]int{"k-personal": http.StatusTooManyRequests}}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	keyedSetup(t, up,
		provider.KeyEntry{Name: "personal", Key: "k-personal"},
		provider.KeyEntry{Name: "team", Key: "k-team"})

	s := New()
	if code := sendKeyed(t, s, "/v1/chat/completions", chatClientBody); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got, want := f.calls(), []string{"/v1/chat/completions k-personal", "/v1/chat/completions k-team"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream saw %q, want %q", got, want)
	}
	c := s.Recent()[0]
	if c.Provider != "fake" || c.Status != http.StatusOK {
		t.Fatalf("call = provider %q status %d", c.Provider, c.Status)
	}
	if !strings.Contains(c.Fallback, "fake") || !strings.Contains(c.Fallback, "personal") {
		t.Fatalf("Fallback = %q, want it to name the refused key", c.Fallback)
	}

	// the next request starts with the key that answered
	f.reset()
	if code := sendKeyed(t, s, "/v1/chat/completions", chatClientBody); code != http.StatusOK {
		t.Fatalf("second request status = %d", code)
	}
	if got, want := f.calls(), []string{"/v1/chat/completions k-team"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second request upstream saw %q, want %q", got, want)
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Fatalf("clean request has Fallback %q", c.Fallback)
	}
}

func TestDisabledKeyIsNeverTried(t *testing.T) {
	f := &keyedFake{status: map[string]int{"k-personal": http.StatusTooManyRequests}}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	keyedSetup(t, up,
		provider.KeyEntry{Name: "personal", Key: "k-personal"},
		provider.KeyEntry{Name: "team", Key: "k-team", Off: true})

	s := New()
	if code := sendKeyed(t, s, "/v1/chat/completions", chatClientBody); code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the last candidate's 429", code)
	}
	if got, want := f.calls(), []string{"/v1/chat/completions k-personal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream saw %q, want %q", got, want)
	}
}

func TestClientErrorIsNotRetriedWithAnotherKey(t *testing.T) {
	f := &keyedFake{status: map[string]int{"k-personal": http.StatusBadRequest}}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	keyedSetup(t, up,
		provider.KeyEntry{Name: "personal", Key: "k-personal"},
		provider.KeyEntry{Name: "team", Key: "k-team"})

	s := New()
	if code := sendKeyed(t, s, "/v1/chat/completions", chatClientBody); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 as it came", code)
	}
	if got, want := f.calls(), []string{"/v1/chat/completions k-personal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream saw %q, want %q", got, want)
	}
}

func TestProtocolLimitedKeysReachOnlyTheirEndpoint(t *testing.T) {
	f := &keyedFake{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	keyedSetup(t, up,
		provider.KeyEntry{Name: "anthropic", Key: "k-anth", Protocol: provider.Anthropic},
		provider.KeyEntry{Name: "openai", Key: "k-oai", Protocol: provider.Chat})

	cases := []struct {
		name, path, body string
		want             []string
	}{
		{"claude code, claude model", "/v1/messages",
			`{"model":"fake/claude-x","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			[]string{"/v1/messages k-anth"}},
		{"openai client, claude model", "/v1/chat/completions",
			`{"model":"fake/claude-x","messages":[{"role":"user","content":"hi"}]}`,
			[]string{"/v1/messages k-anth"}},
		{"claude code, gpt model", "/v1/messages",
			`{"model":"fake/gpt-5","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			[]string{"/v1/chat/completions k-oai"}},
		{"openai client, gpt model", "/v1/chat/completions",
			`{"model":"fake/gpt-5","messages":[{"role":"user","content":"hi"}]}`,
			[]string{"/v1/chat/completions k-oai"}},
	}
	s := New()
	for _, tc := range cases {
		f.reset()
		if code := sendKeyed(t, s, tc.path, tc.body); code != http.StatusOK {
			t.Fatalf("%s: status = %d", tc.name, code)
		}
		if got := f.calls(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: upstream saw %q, want %q", tc.name, got, tc.want)
		}
	}
}
