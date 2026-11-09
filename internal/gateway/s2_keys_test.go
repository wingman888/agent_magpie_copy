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

// s2Reply is a stream both endpoints' clients can read without complaint.
const s2Reply = "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"

// s2Upstream answers each key with the status the test gives it (200 when
// none), and records "path key" for every request in order.
type s2Upstream struct {
	mu    sync.Mutex
	codes map[string]int
	hits  []string
}

func (u *s2Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	u.mu.Lock()
	u.hits = append(u.hits, r.URL.Path+" "+key)
	code := u.codes[key]
	u.mu.Unlock()
	if code == 0 || code == http.StatusOK {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, s2Reply)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	io.WriteString(w, `{"error":{"message":"rate limited"}}`)
}

func (u *s2Upstream) seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.hits...)
}

// s2Env points magpie at a fresh config and starts an upstream that answers
// with the given status per key.
func s2Env(t *testing.T, codes map[string]int) (*s2Upstream, *httptest.Server) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	u := &s2Upstream{codes: codes}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	return u, up
}

// s2Post sends one request through the gateway and returns the status.
func s2Post(t *testing.T, h http.Handler, path, body string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code
}

const (
	s2ChatBody   = `{"model":"relay/gpt-5.5","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	s2ClaudeBody = `{"model":"relay/claude-sonnet-4","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
)

// s2Relay saves a relay with an OpenAI-style and an Anthropic endpoint on up,
// and the given first key.
func s2Relay(t *testing.T, up *httptest.Server, p provider.Provider) {
	t.Helper()
	p.ID, p.Name = "relay", "Relay"
	p.Chat, p.Anthropic = up.URL+"/v1", up.URL
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func TestS2FirstKeyRateLimitedSecondAnswers(t *testing.T) {
	u, up := s2Env(t, map[string]int{"sk-s2-one": http.StatusTooManyRequests})
	s2Relay(t, up, provider.Provider{Key: "sk-s2-one", KeyName: "personal", Chat: up.URL + "/v1"})
	if err := provider.AddKey("relay", "team", "sk-s2-two", ""); err != nil {
		t.Fatal(err)
	}
	srv := New()
	if code := s2Post(t, srv.Handler(), "/v1/chat/completions", s2ChatBody); code != 200 {
		t.Fatalf("status %d", code)
	}
	want := []string{"/v1/chat/completions sk-s2-one", "/v1/chat/completions sk-s2-two"}
	if got := u.seen(); !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream saw %v, want %v", got, want)
	}
	calls := srv.Recent()
	if len(calls) != 1 || calls[0].Status != 200 || calls[0].Provider != "relay" ||
		calls[0].Fallback != "relay (personal)" || calls[0].Key != "team" {
		t.Fatalf("call record: %+v", calls)
	}

	// the next request goes straight to the key that answered
	before := len(u.seen())
	if code := s2Post(t, srv.Handler(), "/v1/chat/completions", s2ChatBody); code != 200 {
		t.Fatalf("second status %d", code)
	}
	if got := u.seen()[before:]; !reflect.DeepEqual(got, []string{"/v1/chat/completions sk-s2-two"}) {
		t.Fatalf("second request went to %v", got)
	}
	if calls := srv.Recent(); calls[0].Fallback != "" {
		t.Errorf("second call still reports a fallback: %q", calls[0].Fallback)
	}
}

func TestS2DisabledKeyNeverTried(t *testing.T) {
	u, up := s2Env(t, map[string]int{"sk-s2-one": http.StatusTooManyRequests})
	s2Relay(t, up, provider.Provider{Key: "sk-s2-one", Chat: up.URL + "/v1"})
	provider.AddKey("relay", "off", "sk-s2-two", "")
	provider.AddKey("relay", "on", "sk-s2-three", "")
	if err := provider.SetKeyOn("relay", provider.KeyRef("sk-s2-two"), false); err != nil {
		t.Fatal(err)
	}
	if code := s2Post(t, New().Handler(), "/v1/chat/completions", s2ChatBody); code != 200 {
		t.Fatalf("status %d", code)
	}
	want := []string{"/v1/chat/completions sk-s2-one", "/v1/chat/completions sk-s2-three"}
	if got := u.seen(); !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream saw %v, want %v", got, want)
	}
}

func TestS2BadRequestIsNotRetried(t *testing.T) {
	u, up := s2Env(t, map[string]int{"sk-s2-one": http.StatusBadRequest})
	s2Relay(t, up, provider.Provider{Key: "sk-s2-one", Chat: up.URL + "/v1"})
	provider.AddKey("relay", "", "sk-s2-two", "")
	srv := New()
	if code := s2Post(t, srv.Handler(), "/v1/chat/completions", s2ChatBody); code != 400 {
		t.Fatalf("status %d, want 400 passed through", code)
	}
	if got := u.seen(); len(got) != 1 {
		t.Fatalf("a 400 switched keys: %v", got)
	}
}

func TestS2LastKeyErrorReturned(t *testing.T) {
	u, up := s2Env(t, map[string]int{"sk-s2-one": http.StatusTooManyRequests, "sk-s2-two": http.StatusTooManyRequests})
	s2Relay(t, up, provider.Provider{Key: "sk-s2-one", Chat: up.URL + "/v1"})
	provider.AddKey("relay", "", "sk-s2-two", "")
	srv := New()
	if code := s2Post(t, srv.Handler(), "/v1/chat/completions", s2ChatBody); code != 429 {
		t.Fatalf("status %d, want the last key's 429", code)
	}
	if got := u.seen(); len(got) != 2 {
		t.Fatalf("upstream saw %v", got)
	}
	if calls := srv.Recent(); len(calls) != 1 || calls[0].Status != 429 || calls[0].Fallback == "" {
		t.Fatalf("call record: %+v", calls)
	}
}

func TestS2ProtocolLimitedKeysReachTheirEndpoint(t *testing.T) {
	const claudeKey, openaiKey = "sk-s2-claude", "sk-s2-openai"
	for _, claudeFirst := range []bool{true, false} {
		u, up := s2Env(t, nil)
		claude := provider.Provider{Key: claudeKey, KeyProtocol: provider.Anthropic}
		openai := provider.Provider{Key: openaiKey, KeyProtocol: provider.Chat}
		first, second := openai, claude
		if claudeFirst {
			first, second = claude, openai
		}
		s2Relay(t, up, first)
		if err := provider.AddKey("relay", "", second.Key, second.KeyProtocol); err != nil {
			t.Fatal(err)
		}
		h := New().Handler()
		steps := []struct{ path, body, want string }{
			// Claude Code asks for a Claude model: the Anthropic key, relayed
			{"/v1/messages", s2ClaudeBody, "/v1/messages " + claudeKey},
			// an OpenAI client asks for a Claude model: translated to Anthropic
			{"/v1/chat/completions", s2ClaudeBody, "/v1/messages " + claudeKey},
			// Claude Code asks for a GPT model: translated to the OpenAI endpoint
			{"/v1/messages", strings.Replace(s2ChatBody, `"messages"`, `"max_tokens":10,"messages"`, 1), "/v1/chat/completions " + openaiKey},
			// an OpenAI client asks for a GPT model: relayed
			{"/v1/chat/completions", s2ChatBody, "/v1/chat/completions " + openaiKey},
		}
		for i, st := range steps {
			before := len(u.seen())
			if code := s2Post(t, h, st.path, st.body); code != 200 {
				t.Fatalf("claudeFirst=%v step %d: status %d", claudeFirst, i, code)
			}
			if got := u.seen()[before:]; !reflect.DeepEqual(got, []string{st.want}) {
				t.Errorf("claudeFirst=%v step %d: upstream saw %v, want %s", claudeFirst, i, got, st.want)
			}
		}
	}
}

func TestS2CountTokensSkipsOpenAIKey(t *testing.T) {
	// only an OpenAI-limited key: counting falls back to the local estimate
	u, up := s2Env(t, nil)
	s2Relay(t, up, provider.Provider{Key: "sk-s2-openai", KeyProtocol: provider.Chat})
	if code := s2Post(t, New().Handler(), "/v1/messages/count_tokens", s2ClaudeBody); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got := u.seen(); len(got) != 0 {
		t.Fatalf("an OpenAI-only key counted Anthropic tokens: %v", got)
	}

	// with an Anthropic-limited key, the vendor counts
	u2, up2 := s2Env(t, nil)
	s2Relay(t, up2, provider.Provider{Key: "sk-s2-claude", KeyProtocol: provider.Anthropic})
	if code := s2Post(t, New().Handler(), "/v1/messages/count_tokens", s2ClaudeBody); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got := u2.seen(); !reflect.DeepEqual(got, []string{"/v1/messages/count_tokens sk-s2-claude"}) {
		t.Fatalf("upstream saw %v", got)
	}
}
