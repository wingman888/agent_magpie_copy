package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyedUpstream answers by the key it was called with: a key mapped to a
// status gets that status, every other key gets 200. It records "path key"
// for each call, so a test can see which key was tried where.
type keyedUpstream struct {
	mu     sync.Mutex
	status map[string]int
	seen   []string
}

func (u *keyedUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if key == "" {
		key = r.Header.Get("x-api-key")
	}
	u.mu.Lock()
	u.seen = append(u.seen, r.URL.Path+" "+key)
	code := u.status[key]
	u.mu.Unlock()
	if code == 0 {
		code = http.StatusOK
	}
	if code >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"limited"}}`)
		return
	}
	switch r.URL.Path {
	case "/v1/chat/completions":
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
			`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			`data: [DONE]`))
	case "/v1/messages":
		// translation always asks upstream to stream, so answer as a stream
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
	case "/v1/messages/count_tokens":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":7}`)
	default:
		http.NotFound(w, r)
	}
}

func (u *keyedUpstream) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

// keyedSetup saves a provider "relay" with both OpenAI and Anthropic endpoints
// on one upstream, the first key given, and returns the upstream.
func keyedSetup(t *testing.T, key string, status map[string]int) *keyedUpstream {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	u := &keyedUpstream{status: status}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: key,
		Chat: up.URL + "/v1", Anthropic: up.URL, Models: []string{"m1", "claude-sonnet-x", "gpt-x"}})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// do sends one request through the handler and returns the code and body.
func do(t *testing.T, s *Server, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func lastCall(t *testing.T, s *Server) Call {
	t.Helper()
	calls := s.Recent()
	if len(calls) == 0 {
		t.Fatal("no call recorded")
	}
	return calls[0]
}

const chatReq = `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`

// A rate-limited first key is replaced by the second within the same request,
// the call log says so, and the next request goes straight to the second key.
func TestRateLimitedKeyFailsOverToSecondKey(t *testing.T) {
	u := keyedSetup(t, "k1", map[string]int{"k1": http.StatusTooManyRequests})
	must(t, provider.AddKey("relay", "team", "k2", ""))
	s := New()

	code, body := do(t, s, "/v1/chat/completions", chatReq)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := u.calls(); strings.Join(got, ",") != "/v1/chat/completions k1,/v1/chat/completions k2" {
		t.Fatalf("upstream saw %v", got)
	}
	c := lastCall(t, s)
	if c.Key != "team" || c.Provider != "relay" {
		t.Errorf("call key/provider: %q %q", c.Key, c.Provider)
	}
	if !strings.Contains(c.Fallback, "relay") || !strings.Contains(c.Fallback, provider.KeyRef("k1")) {
		t.Errorf("fallback should name the failed key: %q", c.Fallback)
	}

	code, _ = do(t, s, "/v1/chat/completions", chatReq)
	if code != 200 {
		t.Fatalf("second request: %d", code)
	}
	got := u.calls()
	if len(got) != 3 || got[2] != "/v1/chat/completions k2" {
		t.Errorf("second request should use k2 directly: %v", got)
	}
	if c := lastCall(t, s); c.Fallback != "" || c.Key != "team" {
		t.Errorf("second call: %+v", c)
	}
}

// A key that is switched off is never tried, even when the others fail.
func TestDisabledKeyIsNeverTried(t *testing.T) {
	u := keyedSetup(t, "k1", map[string]int{"k1": http.StatusTooManyRequests})
	must(t, provider.AddKey("relay", "off", "k2", ""))
	must(t, provider.SetKeyOn("relay", provider.KeyRef("k2"), false))
	s := New()

	code, _ := do(t, s, "/v1/chat/completions", chatReq)
	if code != 429 {
		t.Errorf("only k1 is enabled; its 429 should reach the client, got %d", code)
	}
	for _, c := range u.calls() {
		if strings.HasSuffix(c, " k2") {
			t.Errorf("disabled key tried: %v", u.calls())
		}
	}
}

// A 400 is the request's fault, so the next key is not tried.
func TestBadRequestDoesNotFailOver(t *testing.T) {
	u := keyedSetup(t, "k1", map[string]int{"k1": http.StatusBadRequest})
	must(t, provider.AddKey("relay", "", "k2", ""))
	s := New()

	code, _ := do(t, s, "/v1/chat/completions", chatReq)
	if code != 400 {
		t.Fatalf("status %d", code)
	}
	if got := u.calls(); len(got) != 1 {
		t.Errorf("400 should not move to another key: %v", got)
	}
}

// When every key fails, the client gets the last key's error.
func TestLastKeysErrorReachesClient(t *testing.T) {
	keyedSetup(t, "k1", map[string]int{"k1": http.StatusTooManyRequests, "k2": http.StatusTooManyRequests})
	must(t, provider.AddKey("relay", "", "k2", ""))
	s := New()

	code, body := do(t, s, "/v1/chat/completions", chatReq)
	if code != 429 || !strings.Contains(body, "limited") {
		t.Errorf("status %d body %s", code, body)
	}
}

// A key limited to Anthropic only reaches the Anthropic endpoint; a key limited
// to OpenAI only reaches the OpenAI endpoint. Model family decides which is tried first.
func TestProtocolLimitedKeysUseTheirEndpoint(t *testing.T) {
	u := keyedSetup(t, "ka", nil)
	must(t, provider.SetKeyProtocol("relay", provider.KeyRef("ka"), provider.Anthropic))
	must(t, provider.AddKey("relay", "oai", "ko", provider.Chat))
	s := New()

	// Claude Code asks for a Claude model: the Anthropic key, on /v1/messages
	code, body := do(t, s, "/v1/messages", `{"model":"claude-sonnet-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("claude: %d %s", code, body)
	}
	if got := u.calls(); len(got) != 1 || got[0] != "/v1/messages ka" {
		t.Errorf("claude request: %v", got)
	}

	// Claude Code asks for a GPT model: translated, so the OpenAI-limited key on chat
	code, body = do(t, s, "/v1/messages", `{"model":"gpt-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("gpt: %d %s", code, body)
	}
	got := u.calls()
	if len(got) != 2 || got[1] != "/v1/chat/completions ko" {
		t.Errorf("gpt request should go to the OpenAI key's endpoint: %v", got)
	}
	if c := lastCall(t, s); c.To != provider.Chat || c.Key != "oai" {
		t.Errorf("call to/key: %v %q", c.To, c.Key)
	}

	// An OpenAI client asks for a Claude model: the Anthropic key, translated
	code, body = do(t, s, "/v1/chat/completions", `{"model":"claude-sonnet-x","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("openai client, claude: %d %s", code, body)
	}
	got = u.calls()
	if len(got) != 3 || got[2] != "/v1/messages ka" {
		t.Errorf("openai client with claude model should use the Anthropic key: %v", got)
	}
}

// An OpenAI-limited key is never used to count Anthropic tokens.
func TestCountTokensIgnoresOpenAIOnlyKeys(t *testing.T) {
	u := keyedSetup(t, "ko", nil)
	must(t, provider.SetKeyProtocol("relay", provider.KeyRef("ko"), provider.Chat))
	s := New()

	code, body := do(t, s, "/v1/messages/count_tokens", `{"model":"claude-sonnet-x","messages":[{"role":"user","content":"hello there"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	for _, c := range u.calls() {
		if strings.HasPrefix(c, "/v1/messages/count_tokens") {
			t.Errorf("OpenAI-only key used for count_tokens: %v", u.calls())
		}
	}
	var out map[string]any
	json.Unmarshal([]byte(body), &out)
	if _, ok := out["input_tokens"]; !ok {
		t.Errorf("no estimate: %s", body)
	}
}

// A resting key waits behind the others until its rest is over.
func TestRestingKeyWaitsBehindOthers(t *testing.T) {
	s := New()
	a := provider.Provider{ID: "relay", Key: "a"}
	b := provider.Provider{ID: "relay", Key: "b"}
	cands := []provider.Provider{a, b}

	if got := s.order(cands); got[0].Key != "a" {
		t.Fatalf("fresh order: %s", got[0].Key)
	}
	s.rest(a)
	if got := s.order(cands); got[0].Key != "b" || got[1].Key != "a" {
		t.Errorf("resting key should go last: %s %s", got[0].Key, got[1].Key)
	}
	// once the rest is over, the original order comes back
	s.mu.Lock()
	s.keyRest[restKey(a)] = time.Now().Add(-2 * keyRestFor)
	s.mu.Unlock()
	if got := s.order(cands); got[0].Key != "a" {
		t.Errorf("rest should have ended: %s", got[0].Key)
	}
}

