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

// keyFake is an upstream that answers by the key it is given: a key in fail
// gets that status, the others a normal reply. It counts what it saw.
type keyFake struct {
	mu    sync.Mutex
	fail  map[string]int // key → status it answers with
	hits  map[string]int // key → requests
	paths []string       // path of each request, in order
}

func newKeyFake() *keyFake {
	return &keyFake{fail: map[string]int{}, hits: map[string]int{}}
}

// anthropicReply is a complete Messages stream saying "ok".
var anthropicReply = sse(
	`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
	`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
	`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
	`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
	`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
)

func (f *keyFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reqBody, _ := io.ReadAll(r.Body)
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	f.mu.Lock()
	f.hits[key]++
	f.paths = append(f.paths, r.URL.Path)
	code := f.fail[key]
	f.mu.Unlock()
	if code != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"no way"}}`)
		return
	}
	switch r.URL.Path {
	case "/v1/chat/completions":
		if strings.Contains(string(reqBody), `"stream":true`) {
			// translated requests always stream upstream
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"ok"}}]}`,
				`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
				`data: [DONE]`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	case "/v1/messages/count_tokens":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":7}`)
	case "/v1/messages":
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicReply)
	default:
		http.NotFound(w, r)
	}
}

func (f *keyFake) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[key]
}

func (f *keyFake) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

// keyedUpstream isolates the config and starts the fake. Both protocol bases
// point at it, so a test can tell endpoints apart by path.
func keyedUpstream(t *testing.T) (*keyFake, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := newKeyFake()
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	return f, up.URL
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// callOn posts to one Server, so its memory of failed keys carries over.
func callOn(srv *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

const chatBody = `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`

func TestSecondKeyAnswersWhenFirstIsLimited(t *testing.T) {
	f, url := keyedUpstream(t)
	f.fail["k1"] = 429
	mustDo(t, provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: url + "/v1", Models: []string{"m1"}}))
	mustDo(t, provider.RenameKey("relay", provider.KeyRef("k1"), "personal"))
	mustDo(t, provider.AddKey("relay", "team", "k2", ""))

	srv := New()
	code, body := callOn(srv, "/v1/chat/completions", chatBody)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.count("k1") != 1 || f.count("k2") != 1 {
		t.Fatalf("hits k1=%d k2=%d, want 1 and 1", f.count("k1"), f.count("k2"))
	}
	calls := srv.Recent()
	if len(calls) != 1 || calls[0].Provider != "relay" {
		t.Fatalf("calls: %+v", calls)
	}
	if !strings.Contains(calls[0].Fallback, "relay") || !strings.Contains(calls[0].Fallback, "personal") {
		t.Fatalf("fallback = %q, want the failed key named", calls[0].Fallback)
	}
	if calls[0].Status != 200 {
		t.Fatalf("recorded status %d", calls[0].Status)
	}
}

func TestNextRequestUsesWorkingKeyFirst(t *testing.T) {
	f, url := keyedUpstream(t)
	f.fail["k1"] = 429
	mustDo(t, provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: url + "/v1", Models: []string{"m1"}}))
	mustDo(t, provider.AddKey("relay", "team", "k2", ""))

	srv := New()
	if code, body := callOn(srv, "/v1/chat/completions", chatBody); code != 200 {
		t.Fatalf("first: %d %s", code, body)
	}
	if code, body := callOn(srv, "/v1/chat/completions", chatBody); code != 200 {
		t.Fatalf("second: %d %s", code, body)
	}
	if f.count("k1") != 1 || f.count("k2") != 2 {
		t.Fatalf("hits k1=%d k2=%d, want 1 and 2: the limited key should rest", f.count("k1"), f.count("k2"))
	}
	calls := srv.Recent()
	if calls[0].Fallback != "" {
		t.Fatalf("second call fell back: %q", calls[0].Fallback)
	}
}

func TestDisabledKeyIsNeverTried(t *testing.T) {
	f, url := keyedUpstream(t)
	f.fail["k1"] = 429
	mustDo(t, provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: url + "/v1", Models: []string{"m1"}}))
	mustDo(t, provider.AddKey("relay", "team", "k2", ""))
	mustDo(t, provider.SetKeyOn("relay", provider.KeyRef("k2"), false))

	code, body := callOn(New(), "/v1/chat/completions", chatBody)
	if code != 429 {
		t.Fatalf("status %d, want the first key's 429 back: %s", code, body)
	}
	if f.count("k2") != 0 {
		t.Fatalf("disabled key was tried %d times", f.count("k2"))
	}
}

func TestBadRequestIsNotRetried(t *testing.T) {
	f, url := keyedUpstream(t)
	f.fail["k1"] = 400
	mustDo(t, provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: url + "/v1", Models: []string{"m1"}}))
	mustDo(t, provider.AddKey("relay", "team", "k2", ""))

	srv := New()
	code, body := callOn(srv, "/v1/chat/completions", chatBody)
	if code != 400 || !strings.Contains(body, "no way") {
		t.Fatalf("status %d %s, want the 400 as it came", code, body)
	}
	if f.count("k2") != 0 {
		t.Fatalf("400 moved on to the next key")
	}
	if srv.Recent()[0].Fallback != "" {
		t.Fatalf("fallback set on a 400: %q", srv.Recent()[0].Fallback)
	}
}

func TestAllKeysFailingReturnsLastError(t *testing.T) {
	f, url := keyedUpstream(t)
	f.fail["k1"] = 429
	f.fail["k2"] = 503
	mustDo(t, provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: url + "/v1", Models: []string{"m1"}}))
	mustDo(t, provider.AddKey("relay", "team", "k2", ""))

	code, _ := callOn(New(), "/v1/chat/completions", chatBody)
	if code != 503 {
		t.Fatalf("status %d, want the last key's 503", code)
	}
	if f.count("k1") != 1 || f.count("k2") != 1 {
		t.Fatalf("hits k1=%d k2=%d", f.count("k1"), f.count("k2"))
	}
}

// protocolKeys is a relay with both endpoints, a key limited to chat (ko) and
// one limited to Anthropic (ka).
func protocolKeys(t *testing.T) (*keyFake, *Server) {
	t.Helper()
	f, url := keyedUpstream(t)
	mustDo(t, provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "ko", Chat: url + "/v1", Anthropic: url,
		Models: []string{"claude-x", "gpt-5", "m1"}}))
	mustDo(t, provider.SetKeyProtocol("relay", provider.KeyRef("ko"), provider.Chat))
	mustDo(t, provider.AddKey("relay", "anth", "ka", provider.Anthropic))
	return f, New()
}

func TestClaudeCodeClaudeModelHitsAnthropicKey(t *testing.T) {
	f, srv := protocolKeys(t)
	code, body := callOn(srv, "/v1/messages", `{"model":"claude-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := f.seen(); len(got) != 1 || got[0] != "/v1/messages" || f.count("ka") != 1 {
		t.Fatalf("paths %v, ka=%d: want one Anthropic call with ka", got, f.count("ka"))
	}
}

func TestOpenAIClaudeModelTranslatesToAnthropicKey(t *testing.T) {
	f, srv := protocolKeys(t)
	code, body := callOn(srv, "/v1/chat/completions", `{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := f.seen(); len(got) != 1 || got[0] != "/v1/messages" || f.count("ka") != 1 {
		t.Fatalf("paths %v, ka=%d: want the translated call on Anthropic with ka", got, f.count("ka"))
	}
	if srv.Recent()[0].To != provider.Anthropic {
		t.Fatalf("to = %s, want anthropic", srv.Recent()[0].To)
	}
}

func TestClaudeCodeGPTModelTranslatesToChatKey(t *testing.T) {
	f, srv := protocolKeys(t)
	code, body := callOn(srv, "/v1/messages", `{"model":"gpt-5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := f.seen(); len(got) != 1 || got[0] != "/v1/chat/completions" || f.count("ko") != 1 || f.count("ka") != 0 {
		t.Fatalf("paths %v, ko=%d ka=%d: want the translated call on chat with ko only", got, f.count("ko"), f.count("ka"))
	}
}

func TestCountTokensSkipsOpenAIOnlyKey(t *testing.T) {
	f, srv := protocolKeys(t)
	code, body := callOn(srv, "/v1/messages/count_tokens", `{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.count("ko") != 0 || f.count("ka") != 1 {
		t.Fatalf("count went to ko=%d ka=%d, want ka only", f.count("ko"), f.count("ka"))
	}
	var v map[string]any
	json.Unmarshal([]byte(body), &v)
	if v["input_tokens"] != float64(7) {
		t.Fatalf("body %s", body)
	}
}
