package gateway_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

type relay struct {
	mu   sync.Mutex
	seen []string
}

func (r *relay) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.seen
	r.seen = nil
	return s
}

func lines(l ...string) string { return strings.Join(l, "\n\n") + "\n\n" }

// a relay whose Anthropic endpoint takes only k-ant and OpenAI endpoint only k-oai
func (rl *relay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	rl.mu.Lock()
	rl.seen = append(rl.seen, r.URL.Path+" "+key)
	rl.mu.Unlock()
	ant := r.URL.Path == "/v1/messages"
	if ant != (key == "k-ant") {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"this key is for another protocol"}}`)
		return
	}
	stream := strings.Contains(string(b), `"stream":true`)
	switch {
	case ant && stream:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, lines(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
	case stream:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, lines(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			`data: [DONE]`))
	case ant:
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	default:
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}
}

func env(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// saveRelay: antFirst picks which of the two keys is the current one.
func saveRelay(t *testing.T, antFirst bool) *relay {
	t.Helper()
	env(t)
	rl := &relay{}
	srv := httptest.NewServer(rl)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Anthropic: srv.URL,
		Models: []string{"claude-opus-4-8", "gpt-5.5", "deepseek-chat", "o3-mini"}}
	oai := provider.KeyAccount{Key: "k-oai", Protocol: provider.Chat}
	ant := provider.KeyAccount{Key: "k-ant", Protocol: provider.Anthropic}
	first, second := oai, ant
	if antFirst {
		first, second = ant, oai
	}
	p.Key, p.KeyProtocol = first.Key, first.Protocol
	p.Keys = []provider.KeyAccount{second}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return rl
}

func send(path, body string) (int, string) {
	rec := httptest.NewRecorder()
	gateway.New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const (
	antPath  = "/v1/messages"
	chatPath = "/v1/chat/completions"
)

func msg(model string, stream bool) string {
	s := ""
	if stream {
		s = `,"stream":true`
	}
	return `{"model":"` + model + `","max_tokens":9,"messages":[{"role":"user","content":"hi"}]` + s + `}`
}

func TestAcceptKeysRoutedByModelFamily(t *testing.T) {
	for _, antFirst := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			rl := saveRelay(t, antFirst)
			for _, x := range []struct{ name, path, model, want string }{
				{"A claude on anthropic", antPath, "relay/claude-opus-4-8", "/v1/messages k-ant"},
				{"B claude from chat client", chatPath, "relay/claude-opus-4-8", "/v1/messages k-ant"},
				{"C gpt from anthropic client", antPath, "relay/gpt-5.5", "/v1/chat/completions k-oai"},
				{"gpt from chat client", chatPath, "relay/gpt-5.5", "/v1/chat/completions k-oai"},
				{"o3 from anthropic client", antPath, "openrouter/openai/o3-mini", ""},
				{"o3 plain", antPath, "relay/o3-mini", "/v1/chat/completions k-oai"},
			} {
				if x.want == "" {
					continue
				}
				rl.take()
				code, body := send(x.path, msg(x.model, stream))
				got := rl.take()
				if code != 200 || len(got) != 1 || got[0] != x.want {
					t.Fatalf("%s antFirst=%v stream=%v: %d %s upstream %v", x.name, antFirst, stream, code, body, got)
				}
				if !strings.Contains(body, "ok") {
					t.Fatalf("%s: no content in %s", x.name, body)
				}
				if x.path == chatPath && !strings.Contains(body, "chat.completion") && !strings.Contains(body, `"choices"`) {
					t.Fatalf("%s: not chat format: %s", x.name, body)
				}
			}
		}
	}
}

func TestAcceptAnthropicRequestPassesThroughUntranslated(t *testing.T) {
	rl := saveRelay(t, false)
	_ = rl
	var gotBody string
	// replace upstream's view: check body keeps Anthropic shape via a spy
	spy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer spy.Close()
	p := provider.Provider{ID: "spy", Name: "Spy", Chat: spy.URL + "/v1", Anthropic: spy.URL, Key: "k-oai", KeyProtocol: provider.Chat,
		Keys: []provider.KeyAccount{{Key: "k-ant", Protocol: provider.Anthropic}}, Models: []string{"claude-opus-4-8"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := send(antPath, msg("spy/claude-opus-4-8", false))
	if code != 200 || !strings.Contains(gotBody, `"max_tokens"`) || strings.Contains(gotBody, `"choices"`) {
		t.Fatalf("%d %s upstream body %s", code, body, gotBody)
	}
	if !strings.Contains(gotBody, `"model":"claude-opus-4-8"`) {
		t.Fatalf("model not rewritten: %s", gotBody)
	}
}

func TestAcceptUnknownModelPrefersClientProtocolKey(t *testing.T) {
	for _, antFirst := range []bool{false, true} {
		rl := saveRelay(t, antFirst)
		code, body := send(antPath, msg("relay/deepseek-chat", false))
		if got := rl.take(); code != 200 || len(got) != 1 || got[0] != "/v1/messages k-ant" {
			t.Fatalf("anthropic client antFirst=%v: %d %s %v", antFirst, code, body, got)
		}
		code, body = send(chatPath, msg("relay/deepseek-chat", false))
		if got := rl.take(); code != 200 || len(got) != 1 || got[0] != "/v1/chat/completions k-oai" {
			t.Fatalf("chat client antFirst=%v: %d %s %v", antFirst, code, body, got)
		}
	}
}

func TestAcceptKeyForMissingEndpointIsIgnored(t *testing.T) {
	env(t)
	rl := &relay{}
	srv := httptest.NewServer(rl)
	defer srv.Close()
	// Chat endpoint only; the first key is Anthropic-only and has nowhere to go
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "k-ant", KeyProtocol: provider.Anthropic,
		Keys: []provider.KeyAccount{{Key: "k-oai"}}, Models: []string{"gpt-5.5"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := send(chatPath, msg("relay/gpt-5.5", false))
	got := rl.take()
	if code != 200 || len(got) != 1 || got[0] != "/v1/chat/completions k-oai" {
		t.Fatalf("%d %s upstream %v", code, body, got)
	}
}

func TestAcceptAllKeysIgnoredFallsBackToDefault(t *testing.T) {
	env(t)
	rl := &relay{}
	srv := httptest.NewServer(rl)
	defer srv.Close()
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "k-ant", KeyProtocol: provider.Anthropic, Models: []string{"gpt-5.5"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := send(chatPath, msg("relay/gpt-5.5", false))
	if len(rl.take()) == 0 || code == 500 || code == 404 {
		t.Fatalf("request not handled by default behavior: %d %s", code, body)
	}
}

func TestAcceptCountTokensWithOnlyOpenAIKey(t *testing.T) {
	env(t)
	rl := &relay{}
	srv := httptest.NewServer(rl)
	defer srv.Close()
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Anthropic: srv.URL,
		Key: "k-oai", KeyProtocol: provider.Chat, Models: []string{"claude-opus-4-8"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := send("/v1/messages/count_tokens", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hello there"}]}`)
	if got := rl.take(); len(got) != 0 {
		t.Fatalf("upstream was called: %v", got)
	}
	if code != 200 || !strings.Contains(body, "input_tokens") {
		t.Fatalf("%d %s", code, body)
	}
}
