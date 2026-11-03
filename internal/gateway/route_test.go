package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// keyRecorder is an upstream that notes the key and path of every request,
// and answers by key.
type keyRecorder struct {
	mu     sync.Mutex
	keys   []string
	paths  []string
	answer func(key, path string) (status int, ctype, body string)
}

func (k *keyRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	k.mu.Lock()
	k.keys = append(k.keys, key)
	k.paths = append(k.paths, r.URL.Path)
	k.mu.Unlock()
	status, ctype, body := k.answer(key, r.URL.Path)
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(status)
	io.WriteString(w, body)
}

// seen lists the keys the upstream was sent, in order, joined by commas.
func (k *keyRecorder) seen() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return strings.Join(k.keys, ",")
}

// seenPaths lists the paths the upstream was sent, in order, joined by commas.
func (k *keyRecorder) seenPaths() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return strings.Join(k.paths, ",")
}

func newUpstream(t *testing.T, answer func(key, path string) (int, string, string)) (*keyRecorder, string) {
	t.Helper()
	rc := &keyRecorder{answer: answer}
	srv := httptest.NewServer(rc)
	t.Cleanup(srv.Close)
	return rc, srv.URL
}

// keyHome points magpie's config at a fresh directory.
func keyHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func callGateway(t *testing.T, h http.Handler, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

var anthropicReply = sse(
	`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":3}}}`,
	`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
	`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
	`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
	`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)

var chatReply = sse(
	`data: {"id":"c1","model":"gpt-test","choices":[{"delta":{"content":"ok"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
	`data: [DONE]`)

const jsonType = "application/json"
const streamType = "text/event-stream"

func saveProvider(t *testing.T, p provider.Provider) {
	t.Helper()
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// relayWithTwoProtocols is a relay with an Anthropic-only key and an
// OpenAI-only key. anthFirst makes the Anthropic key the current one.
func relayWithTwoProtocols(t *testing.T, anthFirst bool) (anth, oai *keyRecorder, h http.Handler) {
	t.Helper()
	keyHome(t)
	anth, anthURL := newUpstream(t, func(key, path string) (int, string, string) {
		return 200, streamType, anthropicReply
	})
	oai, oaiURL := newUpstream(t, func(key, path string) (int, string, string) {
		return 200, streamType, chatReply
	})
	ant := provider.ExtraKey{Name: "anthropic", Key: "sk-ant", Protocol: provider.Anthropic}
	cha := provider.ExtraKey{Name: "openai", Key: "sk-oai", Protocol: provider.Chat}
	p := provider.Provider{ID: "relay", Name: "Relay", Anthropic: anthURL, Chat: oaiURL + "/v1",
		Models: []string{"claude-test", "gpt-test"}}
	if anthFirst {
		p.Key, p.KeyName, p.KeyProtocol, p.Keys = ant.Key, ant.Name, ant.Protocol, []provider.ExtraKey{cha}
	} else {
		p.Key, p.KeyName, p.KeyProtocol, p.Keys = cha.Key, cha.Name, cha.Protocol, []provider.ExtraKey{ant}
	}
	saveProvider(t, p)
	return anth, oai, New().Handler()
}

func TestProtocolLimitedKeysReachTheirEndpoint(t *testing.T) {
	for _, anthFirst := range []bool{true, false} {
		name := "openaiFirst"
		if anthFirst {
			name = "anthropicFirst"
		}
		t.Run(name, func(t *testing.T) {
			t.Run("claude code asks for claude", func(t *testing.T) {
				anth, oai, h := relayWithTwoProtocols(t, anthFirst)
				code, body := callGateway(t, h, "/v1/messages", `{"model":"claude-test","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
				if code != 200 || !strings.Contains(body, "ok") {
					t.Fatalf("status %d: %s", code, body)
				}
				if got := anth.seen(); got != "sk-ant" {
					t.Errorf("anthropic keys: %q", got)
				}
				if got := oai.seen(); got != "" {
					t.Errorf("openai endpoint was reached: %q", got)
				}
			})
			t.Run("openai client asks for claude", func(t *testing.T) {
				anth, oai, h := relayWithTwoProtocols(t, anthFirst)
				code, body := callGateway(t, h, "/v1/chat/completions", `{"model":"claude-test","messages":[{"role":"user","content":"hi"}]}`)
				if code != 200 {
					t.Fatalf("status %d: %s", code, body)
				}
				if got := anth.seenPaths(); got != "/v1/messages" || anth.seen() != "sk-ant" {
					t.Errorf("anthropic: paths %q keys %q", got, anth.seen())
				}
				if got := oai.seen(); got != "" {
					t.Errorf("openai endpoint was reached: %q", got)
				}
			})
			t.Run("claude code asks for gpt", func(t *testing.T) {
				anth, oai, h := relayWithTwoProtocols(t, anthFirst)
				code, body := callGateway(t, h, "/v1/messages", `{"model":"gpt-test","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
				if code != 200 {
					t.Fatalf("status %d: %s", code, body)
				}
				if got := oai.seenPaths(); got != "/v1/chat/completions" || oai.seen() != "sk-oai" {
					t.Errorf("openai: paths %q keys %q", got, oai.seen())
				}
				if got := anth.seen(); got != "" {
					t.Errorf("anthropic endpoint was reached: %q", got)
				}
			})
		})
	}
}

func TestCountTokensNeverUsesOpenAIKey(t *testing.T) {
	keyHome(t)
	anth, anthURL := newUpstream(t, func(key, path string) (int, string, string) {
		return 200, jsonType, `{"input_tokens":42}`
	})
	oai, oaiURL := newUpstream(t, func(key, path string) (int, string, string) {
		return 200, jsonType, `{"input_tokens":1}`
	})
	saveProvider(t, provider.Provider{ID: "relay", Name: "Relay", Anthropic: anthURL, Chat: oaiURL + "/v1",
		Models: []string{"claude-test"}, Key: "sk-oai", KeyProtocol: provider.Chat,
		Keys: []provider.ExtraKey{{Name: "anthropic", Key: "sk-ant", Protocol: provider.Anthropic}}})
	code, body := callGateway(t, New().Handler(), "/v1/messages/count_tokens", `{"model":"claude-test","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "42") {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := anth.seen(); got != "sk-ant" {
		t.Errorf("anthropic keys: %q", got)
	}
	if got := oai.seen(); got != "" {
		t.Errorf("openai-only key was used for count_tokens: %q", got)
	}
}

func TestKeyFailoverMovesOnAndRests(t *testing.T) {
	keyHome(t)
	rc, url := newUpstream(t, func(key, path string) (int, string, string) {
		if key == "sk-personal" {
			return 429, jsonType, `{"error":{"message":"personal limit"}}`
		}
		return 200, streamType, chatReply
	})
	saveProvider(t, provider.Provider{ID: "fake", Name: "Fake", Chat: url + "/v1", Models: []string{"m1"},
		Key: "sk-personal", KeyName: "personal", Keys: []provider.ExtraKey{{Name: "team", Key: "sk-team"}}})
	s := New()
	h := s.Handler()
	req := `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`

	code, body := callGateway(t, h, "/v1/chat/completions", req)
	if code != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("first request: status %d: %s", code, body)
	}
	if got := rc.seen(); got != "sk-personal,sk-team" {
		t.Fatalf("first request keys: %q", got)
	}
	c := s.Recent()[0]
	if c.Status != 200 || c.Provider != "fake" || c.Fallback != "fake (personal)" {
		t.Errorf("record: status %d provider %q fallback %q", c.Status, c.Provider, c.Fallback)
	}

	// the personal key is resting: the next request goes straight to the team key
	code, body = callGateway(t, h, "/v1/chat/completions", req)
	if code != 200 {
		t.Fatalf("second request: status %d: %s", code, body)
	}
	if got := rc.seen(); got != "sk-personal,sk-team,sk-team" {
		t.Errorf("second request keys: %q", got)
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Errorf("second request should not fall back: %q", c.Fallback)
	}
}

func TestOffKeyIsNeverTried(t *testing.T) {
	keyHome(t)
	rc, url := newUpstream(t, func(key, path string) (int, string, string) {
		return 429, jsonType, `{"error":{"message":"personal limit"}}`
	})
	saveProvider(t, provider.Provider{ID: "fake", Name: "Fake", Chat: url + "/v1", Models: []string{"m1"},
		Key: "sk-personal", KeyName: "personal", Keys: []provider.ExtraKey{{Name: "team", Key: "sk-team", Off: true}}})
	code, body := callGateway(t, New().Handler(), "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 429 || !strings.Contains(body, "personal limit") {
		t.Errorf("status %d: %s", code, body)
	}
	if got := rc.seen(); got != "sk-personal" {
		t.Errorf("keys tried: %q", got)
	}
}

func TestBadRequestIsNotRetried(t *testing.T) {
	keyHome(t)
	rc, url := newUpstream(t, func(key, path string) (int, string, string) {
		return 400, jsonType, `{"error":{"message":"bad param"}}`
	})
	saveProvider(t, provider.Provider{ID: "fake", Name: "Fake", Chat: url + "/v1", Models: []string{"m1"},
		Key: "sk-personal", KeyName: "personal", Keys: []provider.ExtraKey{{Name: "team", Key: "sk-team"}}})
	s := New()
	code, body := callGateway(t, s.Handler(), "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || !strings.Contains(body, "bad param") {
		t.Errorf("status %d: %s", code, body)
	}
	if got := rc.seen(); got != "sk-personal" {
		t.Errorf("keys tried: %q", got)
	}
	if f := s.Recent()[0].Fallback; f != "" {
		t.Errorf("fallback on a plain error: %q", f)
	}
}

func TestLastKeysErrorIsReturned(t *testing.T) {
	keyHome(t)
	rc, url := newUpstream(t, func(key, path string) (int, string, string) {
		if key == "sk-personal" {
			return 429, jsonType, `{"error":{"message":"personal limit"}}`
		}
		return 429, jsonType, `{"error":{"message":"team limit"}}`
	})
	saveProvider(t, provider.Provider{ID: "fake", Name: "Fake", Chat: url + "/v1", Models: []string{"m1"},
		Key: "sk-personal", KeyName: "personal", Keys: []provider.ExtraKey{{Name: "team", Key: "sk-team"}}})
	s := New()
	code, body := callGateway(t, s.Handler(), "/v1/chat/completions", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 429 || !strings.Contains(body, "team limit") || strings.Contains(body, "personal limit") {
		t.Errorf("status %d: %s", code, body)
	}
	if got := rc.seen(); got != "sk-personal,sk-team" {
		t.Errorf("keys tried: %q", got)
	}
	if c := s.Recent()[0]; c.Status != 429 || !strings.Contains(c.Error, "team limit") {
		t.Errorf("record: status %d error %q", c.Status, c.Error)
	}
}

func TestStreamOpeningOverloadIsRerouted(t *testing.T) {
	keyHome(t)
	rc, url := newUpstream(t, func(key, path string) (int, string, string) {
		if key == "sk-personal" {
			return 200, streamType, sse(
				`event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
		}
		return 200, streamType, anthropicReply
	})
	saveProvider(t, provider.Provider{ID: "fake", Name: "Fake", Anthropic: url, Models: []string{"claude-test"},
		Key: "sk-personal", KeyName: "personal", Keys: []provider.ExtraKey{{Name: "team", Key: "sk-team"}}})
	s := New()
	code, body := callGateway(t, s.Handler(), "/v1/messages", `{"model":"claude-test","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"text":"ok"`) || strings.Contains(body, "overloaded") {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := rc.seen(); got != "sk-personal,sk-team" {
		t.Errorf("keys tried: %q", got)
	}
	if f := s.Recent()[0].Fallback; f != "fake (personal)" {
		t.Errorf("fallback: %q", f)
	}
}

func TestKeysAreOrderedByModelFamily(t *testing.T) {
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: "https://c.example/v1", Anthropic: "https://a.example",
		Key: "sk-oai", KeyProtocol: provider.Chat,
		Keys: []provider.ExtraKey{{Key: "sk-ant", Protocol: provider.Anthropic}, {Key: "sk-off", Off: true}}}
	cases := []struct {
		name  string
		from  provider.Protocol
		model string
		first string
	}{
		{"claude from anthropic", provider.Anthropic, "claude-x", "sk-ant"},
		{"claude from chat", provider.Chat, "anthropic/claude-x", "sk-ant"},
		{"gpt from anthropic", provider.Anthropic, "gpt-x", "sk-oai"},
		{"codex from chat", provider.Chat, "deepseek/gpt-5-codex", "sk-oai"},
		{"o-series from anthropic", provider.Anthropic, "o3-mini", "sk-oai"},
		{"other model from chat", provider.Chat, "llama-x", "sk-oai"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tries := attempts(p, c.from, c.model, []byte(`{}`))
			if len(tries) != 2 {
				t.Fatalf("attempts: %d (the off key must not be among them)", len(tries))
			}
			s := New()
			s.order(p.ID, tries)
			if got := tries[0].key.Key; got != c.first {
				t.Errorf("first attempt key %q, want %q", got, c.first)
			}
		})
	}
}

func TestKeyWithoutEndpointTakesNoPart(t *testing.T) {
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: "https://c.example/v1",
		Key: "sk-chat", KeyProtocol: provider.Chat,
		Keys: []provider.ExtraKey{{Key: "sk-ant", Protocol: provider.Anthropic}, {Key: "sk-any"}}}
	tries := attempts(p, provider.Anthropic, "claude-x", []byte(`{}`))
	var keys []string
	for _, a := range tries {
		keys = append(keys, a.key.Key)
	}
	if strings.Join(keys, ",") != "sk-chat,sk-any" {
		t.Errorf("attempts: %v", keys)
	}
	for _, a := range tries {
		if a.key.Key == "sk-any" && a.p.Key != "sk-any" {
			t.Errorf("provider not keyed with %q", a.p.Key)
		}
	}
}
