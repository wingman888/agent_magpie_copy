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

// hit is one request a relay got.
type hit struct {
	path, key string
	body      map[string]any
}

// relay serves Chat at /v1 and Anthropic at the root, each taking only its
// own key, the way relays that issue one key per API do.
type relay struct {
	mu   sync.Mutex
	hits []hit
	keys map[string]string // path → the key it takes
}

func (rl *relay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(b, &body)
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	rl.mu.Lock()
	rl.hits = append(rl.hits, hit{r.URL.Path, key, body})
	rl.mu.Unlock()
	if want, ok := rl.keys[r.URL.Path]; !ok || want != key {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
		return
	}
	stream, _ := body["stream"].(bool)
	switch r.URL.Path {
	case "/v1/messages/count_tokens":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":4242}`)
	case "/v1/messages":
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"x","usage":{"input_tokens":3}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`data: {"type":"message_stop"}`))
	case "/v1/chat/completions":
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","model":"x","choices":[{"delta":{"role":"assistant","content":"pong"}}]}`,
			`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`))
	}
}

// relaySetup saves a provider "relay" with a Chat and an Anthropic endpoint
// and the given keys, the first of them in use.
func relaySetup(t *testing.T, keys ...provider.KeyAccount) *relay {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	rl := &relay{keys: map[string]string{
		"/v1/messages":              "k-anth",
		"/v1/messages/count_tokens": "k-anth",
		"/v1/chat/completions":      "k-chat",
	}}
	srv := httptest.NewServer(rl)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Anthropic: srv.URL,
		Models: []string{"claude-opus-4-8", "gpt-5.5"},
		Key:    keys[0].Key, KeyName: keys[0].Name, KeyProtocol: keys[0].Protocol, Keys: keys[1:]}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return rl
}

var (
	chatKey = provider.KeyAccount{Name: "OpenAI", Key: "k-chat", Protocol: provider.Chat}
	anthKey = provider.KeyAccount{Name: "Claude", Key: "k-anth", Protocol: provider.Anthropic}
)

func TestKeyProtocolAnthropicClientClaudeModel(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, order := range [][]provider.KeyAccount{{chatKey, anthKey}, {anthKey, chatKey}} {
			rl := relaySetup(t, order...)
			req := `{"model":"relay/claude-opus-4-8","max_tokens":10,"x_marker":1,"stream":` + boolStr(stream) +
				`,"messages":[{"role":"user","content":"ping"}]}`
			code, body := post(t, "/v1/messages", req)
			if code != 200 || !strings.Contains(body, "pong") {
				t.Fatalf("stream=%v first=%s: %d %s", stream, order[0].Key, code, body)
			}
			if len(rl.hits) != 1 {
				t.Fatalf("stream=%v first=%s: hits %+v", stream, order[0].Key, rl.hits)
			}
			h := rl.hits[0]
			if h.path != "/v1/messages" || h.key != "k-anth" || h.body["x_marker"] != float64(1) ||
				h.body["model"] != "claude-opus-4-8" || h.body["stream"] != stream {
				t.Fatalf("stream=%v first=%s: upstream got %+v", stream, order[0].Key, h)
			}
		}
	}
}

func TestKeyProtocolChatClientClaudeModel(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, order := range [][]provider.KeyAccount{{chatKey, anthKey}, {anthKey, chatKey}} {
			rl := relaySetup(t, order...)
			req := `{"model":"relay/claude-opus-4-8","stream":` + boolStr(stream) + `,"messages":[{"role":"user","content":"ping"}]}`
			code, body := post(t, "/v1/chat/completions", req)
			if code != 200 || !strings.Contains(body, "pong") {
				t.Fatalf("stream=%v first=%s: %d %s", stream, order[0].Key, code, body)
			}
			if stream {
				if !strings.Contains(body, "chat.completion.chunk") {
					t.Fatalf("not a chat stream: %s", body)
				}
			} else if !strings.Contains(body, `"choices"`) {
				t.Fatalf("not a chat reply: %s", body)
			}
			if len(rl.hits) != 1 || rl.hits[0].path != "/v1/messages" || rl.hits[0].key != "k-anth" {
				t.Fatalf("stream=%v first=%s: hits %+v", stream, order[0].Key, rl.hits)
			}
			if _, ok := rl.hits[0].body["messages"]; !ok || rl.hits[0].body["max_tokens"] == nil {
				t.Fatalf("not translated to Anthropic: %+v", rl.hits[0].body)
			}
		}
	}
}

func TestKeyProtocolAnthropicClientGPTModel(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, order := range [][]provider.KeyAccount{{chatKey, anthKey}, {anthKey, chatKey}} {
			rl := relaySetup(t, order...)
			req := `{"model":"relay/gpt-5.5","max_tokens":10,"stream":` + boolStr(stream) + `,"messages":[{"role":"user","content":"ping"}]}`
			code, body := post(t, "/v1/messages", req)
			if code != 200 || !strings.Contains(body, "pong") {
				t.Fatalf("stream=%v first=%s: %d %s", stream, order[0].Key, code, body)
			}
			if stream && !strings.Contains(body, "message_start") {
				t.Fatalf("not an Anthropic stream: %s", body)
			}
			if !stream && !strings.Contains(body, `"type":"message"`) {
				t.Fatalf("not an Anthropic reply: %s", body)
			}
			if len(rl.hits) != 1 || rl.hits[0].path != "/v1/chat/completions" || rl.hits[0].key != "k-chat" ||
				rl.hits[0].body["model"] != "gpt-5.5" {
				t.Fatalf("stream=%v first=%s: hits %+v", stream, order[0].Key, rl.hits)
			}
		}
	}
}

func TestKeyProtocolChatClientGPTModel(t *testing.T) {
	for _, order := range [][]provider.KeyAccount{{chatKey, anthKey}, {anthKey, chatKey}} {
		rl := relaySetup(t, order...)
		code, body := post(t, "/v1/chat/completions", `{"model":"relay/gpt-5.5","messages":[{"role":"user","content":"ping"}]}`)
		if code != 200 || len(rl.hits) != 1 || rl.hits[0].path != "/v1/chat/completions" || rl.hits[0].key != "k-chat" {
			t.Fatalf("first=%s: %d %s, hits %+v", order[0].Key, code, body, rl.hits)
		}
	}
}

// An unknown model goes to the key that speaks the client's protocol.
func TestKeyProtocolUnknownModelPrefersClientProtocol(t *testing.T) {
	for _, order := range [][]provider.KeyAccount{{chatKey, anthKey}, {anthKey, chatKey}} {
		rl := relaySetup(t, order...)
		code, body := post(t, "/v1/messages", `{"model":"relay/deepseek-chat","max_tokens":10,"messages":[{"role":"user","content":"ping"}]}`)
		if code != 200 || len(rl.hits) != 1 || rl.hits[0].path != "/v1/messages" || rl.hits[0].key != "k-anth" {
			t.Fatalf("anthropic client, first=%s: %d %s, hits %+v", order[0].Key, code, body, rl.hits)
		}
		rl.hits = nil
		code, body = post(t, "/v1/chat/completions", `{"model":"relay/deepseek-chat","messages":[{"role":"user","content":"ping"}]}`)
		if code != 200 || len(rl.hits) != 1 || rl.hits[0].path != "/v1/chat/completions" || rl.hits[0].key != "k-chat" {
			t.Fatalf("chat client, first=%s: %d %s, hits %+v", order[0].Key, code, body, rl.hits)
		}
	}
}

// A key for an endpoint the provider lacks is left out, quietly.
func TestKeyProtocolWithoutEndpointIsIgnored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	up := &byKey{limited: map[string]bool{}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	p := provider.Provider{ID: "plan", Name: "Plan", Chat: srv.URL + "/v1", Models: []string{"m1"},
		Key: "k-anth", KeyProtocol: provider.Responses,
		Keys: []provider.KeyAccount{{Key: "k-team"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, "from-k-team") || strings.Join(up.seen, ",") != "k-team" {
		t.Fatalf("%d %s, tried %v", code, body, up.seen)
	}
}

func TestCountTokensKeyProtocol(t *testing.T) {
	req := `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"count these words please"}]}`
	// only an OpenAI key: no request to the Anthropic endpoint, an estimate
	rl := relaySetup(t, chatKey)
	code, body := post(t, "/v1/messages/count_tokens", req)
	if code != 200 || !strings.Contains(body, "input_tokens") || strings.Contains(body, "4242") || len(rl.hits) != 0 {
		t.Fatalf("%d %s, hits %+v", code, body, rl.hits)
	}
	// with an Anthropic key, whichever comes first, it is asked with that key
	for _, order := range [][]provider.KeyAccount{{chatKey, anthKey}, {anthKey, chatKey}} {
		rl = relaySetup(t, order...)
		code, body = post(t, "/v1/messages/count_tokens", req)
		if code != 200 || !strings.Contains(body, "4242") || len(rl.hits) != 1 || rl.hits[0].key != "k-anth" {
			t.Fatalf("first=%s: %d %s, hits %+v", order[0].Key, code, body, rl.hits)
		}
	}
}

func TestModelFamily(t *testing.T) {
	for m, want := range map[string]provider.Protocol{
		"claude-opus-4-8":           provider.Anthropic,
		"relay/claude-sonnet-4-5":   provider.Anthropic,
		"openrouter/openai/o3-mini": provider.Chat,
		"gpt-5.5":                   provider.Chat,
		"relay/gpt-4o":              provider.Chat,
		"o1":                        provider.Chat,
		"gpt-5.1-codex-max":         provider.Chat,
		"codex-mini-latest":         provider.Chat,
		"deepseek-chat":             "",
		"ollama-model":              "",
		"gemini-2.5-pro":            "",
	} {
		if got := modelFamily(m); got != want {
			t.Errorf("modelFamily(%q) = %q, want %q", m, got, want)
		}
	}
}

func TestPerKeyOrder(t *testing.T) {
	p := provider.Provider{ID: "r", Name: "R", Chat: "https://r.test/v1", Anthropic: "https://r.test",
		Key: "a", KeyProtocol: provider.Chat,
		Keys: []provider.KeyAccount{{Key: "b"}, {Key: "c", Protocol: provider.Anthropic}, {Key: "d", Protocol: provider.Responses}, {Key: "e", Off: true}}}
	keys := func(cs []candidate) string {
		var out []string
		for _, c := range cs {
			out = append(out, c.p.Key)
		}
		return strings.Join(out, ",")
	}
	// d is for Responses, which r lacks: left out
	if got := keys(perKey(p, "claude-opus-4-8", provider.Chat)); got != "b,c,a" {
		t.Errorf("claude: %s", got)
	}
	if got := keys(perKey(p, "gpt-5.5", provider.Anthropic)); got != "a,b,c" {
		t.Errorf("gpt: %s", got)
	}
	if got := keys(perKey(p, "deepseek-chat", provider.Anthropic)); got != "b,c,a" {
		t.Errorf("unknown, anthropic client: %s", got)
	}
	if got := keys(perKey(p, "deepseek-chat", provider.Responses)); got != "a,b,c" {
		t.Errorf("unknown, responses client: %s", got)
	}
	for _, c := range perKey(p, "claude-opus-4-8", provider.Chat) {
		if c.p.Key == "a" && (c.p.Anthropic != "" || c.p.Chat == "") {
			t.Errorf("chat key sees %+v", c.p)
		}
		if c.p.Key == "c" && (c.p.Chat != "" || c.p.Anthropic == "") {
			t.Errorf("anthropic key sees %+v", c.p)
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
