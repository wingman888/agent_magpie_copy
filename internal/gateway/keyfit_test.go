package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func TestModelFamily(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  family
	}{
		{"claude-opus-4-8", anthropicFamily},
		{"relay/claude-x", anthropicFamily},
		{"Claude-Sonnet", anthropicFamily},
		{"anthropic/claude-haiku-5-5", anthropicFamily},
		{"gpt-5.5", openAIFamily},
		{"openai/gpt-4o", openAIFamily},
		{"gpt-5-codex", openAIFamily},
		{"codex-mini", openAIFamily},
		{"openrouter/openai/o3-mini", openAIFamily},
		{"o1", openAIFamily},
		{"o9-preview", openAIFamily},
		{"omni-thing", unknownFamily},
		{"o0-x", unknownFamily},
		{"deepseek-chat", unknownFamily},
		{"", unknownFamily},
	} {
		if got := modelFamily(tc.model); got != tc.want {
			t.Errorf("modelFamily(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestOrderKeysKeepsEqualFitInOrder(t *testing.T) {
	p := provider.Provider{Chat: "http://x/v1", Anthropic: "http://x"}
	anyA := provider.KeyAccount{Name: "a", Key: "k-a"}
	anyB := provider.KeyAccount{Name: "b", Key: "k-b"}
	ant := provider.KeyAccount{Name: "ant", Key: "k-ant", Protocol: provider.Anthropic}
	chat := provider.KeyAccount{Name: "chat", Key: "k-chat", Protocol: provider.Chat}
	names := func(ks []provider.KeyAccount) string {
		var out []string
		for _, k := range ks {
			out = append(out, k.Name)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		name  string
		keys  []provider.KeyAccount
		model string
		from  provider.Protocol
		want  string
	}{
		{"unlimited keys stay as they are", []provider.KeyAccount{anyA, anyB}, "claude-x", provider.Chat, "a,b"},
		{"claude takes the anthropic key first", []provider.KeyAccount{chat, anyA, ant}, "claude-x", provider.Anthropic, "ant,a,chat"},
		{"gpt takes the openai key first", []provider.KeyAccount{ant, anyA, chat}, "gpt-5.5", provider.Anthropic, "chat,a,ant"},
		{"unknown model takes the key speaking the client's protocol", []provider.KeyAccount{ant, chat}, "deepseek-chat", provider.Chat, "chat,ant"},
		{"unknown model, anthropic client", []provider.KeyAccount{chat, ant}, "deepseek-chat", provider.Anthropic, "ant,chat"},
	} {
		if got := names(orderKeys(p, tc.keys, tc.model, tc.from)); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

// keyRelay is an upstream with an OpenAI Chat and an Anthropic endpoint. It
// records every request and answers with "from-chat" or "from-anthropic".
type keyRelay struct {
	mu   sync.Mutex
	seen []seenReq
}

type seenReq struct {
	path, auth, xkey string
}

func (k *keyRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	k.mu.Lock()
	k.seen = append(k.seen, seenReq{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("x-api-key")})
	k.mu.Unlock()
	stream := bytes.Contains(body, []byte(`"stream":true`))
	switch r.URL.Path {
	case "/v1/chat/completions":
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`data: {"id":"c1","model":"gpt-5.5","choices":[{"delta":{"content":"from-chat"}}]}`,
				`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
				`data: [DONE]`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"from-chat"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	case "/v1/messages":
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-8","usage":{"input_tokens":3,"output_tokens":0}}}`,
				`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from-anthropic"}}`,
				`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
				`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
				`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"from-anthropic"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
	case "/v1/messages/count_tokens":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":42}`)
	default:
		http.NotFound(w, r)
	}
}

func (k *keyRelay) reset() {
	k.mu.Lock()
	k.seen = nil
	k.mu.Unlock()
}

func (k *keyRelay) calls() []seenReq {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]seenReq(nil), k.seen...)
}

// setupRelay saves a provider "relay" with both endpoints (served by up) and
// the given keys, the first one in front.
func setupRelay(t *testing.T, up *httptest.Server, keys ...provider.KeyAccount) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	p := provider.Provider{ID: "relay", Name: "Relay", Models: []string{"claude-opus-4-8", "gpt-5.5", "deepseek-chat"},
		Chat: up.URL + "/v1", Anthropic: up.URL,
		Key: keys[0].Key, KeyName: keys[0].Name, KeyProtocol: keys[0].Protocol}
	if len(keys) > 1 {
		p.Keys = keys[1:]
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

var (
	antKey  = provider.KeyAccount{Name: "ant", Key: "k-ant", Protocol: provider.Anthropic}
	chatKey = provider.KeyAccount{Name: "chat", Key: "k-chat", Protocol: provider.Chat}
)

// TestKeysByProtocolRouteEachRequest covers acceptance 1 to 4 for both key
// orders and both streaming choices.
func TestKeysByProtocolRouteEachRequest(t *testing.T) {
	orders := map[string][]provider.KeyAccount{
		"anthropic key first": {antKey, chatKey},
		"chat key first":      {chatKey, antKey},
	}
	for orderName, keys := range orders {
		for _, stream := range []string{"", ",\"stream\":true"} {
			up := &keyRelay{}
			srv := httptest.NewServer(up)
			setupRelay(t, srv, keys...)
			name := orderName + " stream=" + map[bool]string{true: "on", false: "off"}[stream != ""]

			// Anthropic client, Claude model: passthrough to Anthropic with the Anthropic key
			up.reset()
			code, body := post(t, "/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":10`+stream+`,"messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || !strings.Contains(body, "from-anthropic") {
				t.Errorf("%s A: %d %s", name, code, body)
			}
			if c := up.calls(); len(c) != 1 || c[0].path != "/v1/messages" || c[0].xkey != "k-ant" {
				t.Errorf("%s A: upstream saw %+v", name, c)
			}

			// Chat client, Claude model: translated to Anthropic, Anthropic key
			up.reset()
			code, body = post(t, "/v1/chat/completions", `{"model":"relay/claude-opus-4-8","max_tokens":10`+stream+`,"messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || !strings.Contains(body, "from-anthropic") {
				t.Errorf("%s B: %d %s", name, code, body)
			}
			if c := up.calls(); len(c) != 1 || c[0].path != "/v1/messages" || c[0].xkey != "k-ant" {
				t.Errorf("%s B: upstream saw %+v", name, c)
			}

			// Anthropic client, GPT model: translated to Chat, OpenAI key
			up.reset()
			code, body = post(t, "/v1/messages", `{"model":"relay/gpt-5.5","max_tokens":10`+stream+`,"messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || !strings.Contains(body, "from-chat") {
				t.Errorf("%s C: %d %s", name, code, body)
			}
			if c := up.calls(); len(c) != 1 || c[0].path != "/v1/chat/completions" || c[0].auth != "Bearer k-chat" {
				t.Errorf("%s C: upstream saw %+v", name, c)
			}
			srv.Close()
		}
	}
}

// TestUnknownModelPrefersDirectProtocol: deepseek-chat names no family, so the
// key speaking the client's protocol goes first whatever the config order.
func TestUnknownModelPrefersDirectProtocol(t *testing.T) {
	for _, keys := range [][]provider.KeyAccount{{antKey, chatKey}, {chatKey, antKey}} {
		up := &keyRelay{}
		srv := httptest.NewServer(up)
		setupRelay(t, srv, keys...)

		up.reset()
		if code, body := post(t, "/v1/messages", `{"model":"relay/deepseek-chat","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from-anthropic") {
			t.Fatalf("anthropic client: %d %s", code, body)
		}
		if c := up.calls(); len(c) != 1 || c[0].path != "/v1/messages" || c[0].xkey != "k-ant" {
			t.Errorf("anthropic client, keys %v: upstream saw %+v", keys[0].Name, c)
		}

		up.reset()
		if code, body := post(t, "/v1/chat/completions", `{"model":"relay/deepseek-chat","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from-chat") {
			t.Fatalf("chat client: %d %s", code, body)
		}
		if c := up.calls(); len(c) != 1 || c[0].path != "/v1/chat/completions" || c[0].auth != "Bearer k-chat" {
			t.Errorf("chat client, keys %v: upstream saw %+v", keys[0].Name, c)
		}
		srv.Close()
	}
}

// TestKeyForAnotherEndpointIsLeftOut: a key limited to Anthropic on a provider
// with no Anthropic endpoint is ignored, and the other key answers.
func TestKeyForAnotherEndpointIsLeftOut(t *testing.T) {
	up := &keyRelay{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Models: []string{"claude-opus-4-8"},
		Chat: srv.URL + "/v1", Key: antKey.Key, KeyName: antKey.Name, KeyProtocol: provider.Anthropic,
		Keys: []provider.KeyAccount{{Name: "any", Key: "k-any"}}}); err != nil {
		t.Fatal(err)
	}
	if code, body := post(t, "/v1/chat/completions", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`); code != 200 || !strings.Contains(body, "from-chat") {
		t.Fatalf("%d %s", code, body)
	}
	if c := up.calls(); len(c) != 1 || c[0].path != "/v1/chat/completions" || c[0].auth != "Bearer k-any" {
		t.Errorf("upstream saw %+v", c)
	}
}

// TestCountTokensNeverReachesAnthropicWithOpenAIKey: with only an OpenAI key,
// count_tokens estimates and sends nothing to the Anthropic endpoint.
func TestCountTokensNeverReachesAnthropicWithOpenAIKey(t *testing.T) {
	up := &keyRelay{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	setupRelay(t, srv, chatKey)

	code, body := post(t, "/v1/messages/count_tokens", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hello there"}]}`)
	if code != 200 || !strings.Contains(body, "input_tokens") || strings.Contains(body, "42") {
		t.Fatalf("%d %s", code, body)
	}
	if c := up.calls(); len(c) != 0 {
		t.Errorf("upstream saw %+v", c)
	}
}

// TestCountTokensUsesAnthropicKey: an Anthropic key is asked for the count.
func TestCountTokensUsesAnthropicKey(t *testing.T) {
	up := &keyRelay{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	setupRelay(t, srv, chatKey, antKey)

	code, body := post(t, "/v1/messages/count_tokens", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "42") {
		t.Fatalf("%d %s", code, body)
	}
	if c := up.calls(); len(c) != 1 || c[0].path != "/v1/messages/count_tokens" || c[0].xkey != "k-ant" {
		t.Errorf("upstream saw %+v", c)
	}
}
