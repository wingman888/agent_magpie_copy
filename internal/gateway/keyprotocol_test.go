package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyCall is one request an upstream received: where, with which key.
type keyCall struct {
	path, key string
	body      string
}

// relayUp stands for a relay with both an Anthropic and a Chat endpoint. It
// records every call and answers each path in the shape the gateway expects.
type relayUp struct {
	calls []keyCall
}

func keyOf(r *http.Request) string {
	if k := r.Header.Get("x-api-key"); k != "" {
		return k
	}
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func (u *relayUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.calls = append(u.calls, keyCall{path: r.URL.Path, key: keyOf(r), body: string(b)})
	stream := strings.Contains(string(b), `"stream":true`)
	switch r.URL.Path {
	case "/v1/messages":
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-8","usage":{"input_tokens":3}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
	case "/v1/messages/count_tokens":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":5}`)
	case "/v1/chat/completions":
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant","content":"pong"},"finish_reason":null}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
			`data: [DONE]`))
	default:
		http.NotFound(w, r)
	}
}

var (
	antKey   = provider.KeyAccount{Name: "ant", Key: "ant-key", Protocol: provider.Anthropic}
	oaiKey   = provider.KeyAccount{Name: "oai", Key: "oai-key", Protocol: provider.Chat}
	plainKey = provider.KeyAccount{Name: "plain", Key: "plain-key"}
)

// relayWith saves provider "relay" with the given keys in order, the first
// one as the provider's current key.
func relayWith(t *testing.T, up *relayUp, endpoints []provider.Protocol, keys ...provider.KeyAccount) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: "relay", Name: "Relay", Models: []string{"claude-opus-4-8", "gpt-5.5", "deepseek-chat"}}
	for _, pr := range endpoints {
		switch pr {
		case provider.Chat:
			p.Chat = srv.URL + "/v1"
		case provider.Anthropic:
			p.Anthropic = srv.URL
		}
	}
	p.Key, p.KeyName, p.KeyProtocol = keys[0].Key, keys[0].Name, keys[0].Protocol
	p.Keys = keys[1:]
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

var bothEndpoints = []provider.Protocol{provider.Chat, provider.Anthropic}

// orders are the two key orders every case must agree on.
var orders = map[string][]provider.KeyAccount{
	"anthropic key first": {antKey, oaiKey},
	"chat key first":      {oaiKey, antKey},
}

func onlyCall(t *testing.T, up *relayUp, path, key string) {
	t.Helper()
	if len(up.calls) != 1 {
		t.Fatalf("upstream got %d calls, want 1: %+v", len(up.calls), up.calls)
	}
	if up.calls[0].path != path || up.calls[0].key != key {
		t.Fatalf("upstream got %s with key %q, want %s with %q", up.calls[0].path, up.calls[0].key, path, key)
	}
}

func TestAnthropicClientClaudeModelUsesAnthropicKeyUntranslated(t *testing.T) {
	for name, keys := range orders {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s stream=%v", name, stream), func(t *testing.T) {
				up := &relayUp{}
				relayWith(t, up, bothEndpoints, keys...)
				body := fmt.Sprintf(`{"model":"relay/claude-opus-4-8","max_tokens":10,"stream":%v,"messages":[{"role":"user","content":"hi"}]}`, stream)
				code, out := post(t, "/v1/messages", body)
				if code != 200 || !strings.Contains(out, "pong") {
					t.Fatalf("%d %s", code, out)
				}
				onlyCall(t, up, "/v1/messages", "ant-key")
				if !strings.Contains(up.calls[0].body, `"model":"claude-opus-4-8"`) {
					t.Fatalf("upstream body %s", up.calls[0].body)
				}
			})
		}
	}
}

func TestChatClientClaudeModelTranslatesToAnthropic(t *testing.T) {
	for name, keys := range orders {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s stream=%v", name, stream), func(t *testing.T) {
				up := &relayUp{}
				relayWith(t, up, bothEndpoints, keys...)
				body := fmt.Sprintf(`{"model":"relay/claude-opus-4-8","stream":%v,"messages":[{"role":"user","content":"hi"}]}`, stream)
				code, out := post(t, "/v1/chat/completions", body)
				if code != 200 || !strings.Contains(out, "pong") {
					t.Fatalf("%d %s", code, out)
				}
				if !stream {
					var resp struct {
						Choices []struct {
							Message struct {
								Content string `json:"content"`
							} `json:"message"`
						} `json:"choices"`
					}
					if err := json.Unmarshal([]byte(out), &resp); err != nil || len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "pong" {
						t.Fatalf("not a chat reply: %s", out)
					}
				}
				onlyCall(t, up, "/v1/messages", "ant-key")
			})
		}
	}
}

func TestAnthropicClientGPTModelTranslatesToOpenAI(t *testing.T) {
	for name, keys := range orders {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s stream=%v", name, stream), func(t *testing.T) {
				up := &relayUp{}
				relayWith(t, up, bothEndpoints, keys...)
				body := fmt.Sprintf(`{"model":"relay/gpt-5.5","max_tokens":10,"stream":%v,"messages":[{"role":"user","content":"hi"}]}`, stream)
				code, out := post(t, "/v1/messages", body)
				if code != 200 || !strings.Contains(out, "pong") {
					t.Fatalf("%d %s", code, out)
				}
				onlyCall(t, up, "/v1/chat/completions", "oai-key")
			})
		}
	}
}

// deepseek-chat names no family, so the key that speaks the client's own
// protocol goes first, whatever the order in the config.
func TestUnknownModelPrefersKeyThatSpeaksClientProtocol(t *testing.T) {
	for name, keys := range orders {
		t.Run(name+" chat client", func(t *testing.T) {
			up := &relayUp{}
			relayWith(t, up, bothEndpoints, keys...)
			code, out := post(t, "/v1/chat/completions", `{"model":"relay/deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || !strings.Contains(out, "pong") {
				t.Fatalf("%d %s", code, out)
			}
			onlyCall(t, up, "/v1/chat/completions", "oai-key")
		})
		t.Run(name+" anthropic client", func(t *testing.T) {
			up := &relayUp{}
			relayWith(t, up, bothEndpoints, keys...)
			code, out := post(t, "/v1/messages", `{"model":"relay/deepseek-chat","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || !strings.Contains(out, "pong") {
				t.Fatalf("%d %s", code, out)
			}
			onlyCall(t, up, "/v1/messages", "ant-key")
		})
	}
}

// A key limited to a protocol the provider lacks is ignored, and the request
// goes on with the keys that can be used.
func TestKeyWithoutMatchingEndpointIsIgnored(t *testing.T) {
	up := &relayUp{}
	relayWith(t, up, []provider.Protocol{provider.Chat}, antKey, plainKey)
	code, out := post(t, "/v1/chat/completions", `{"model":"relay/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(out, "pong") {
		t.Fatalf("%d %s", code, out)
	}
	onlyCall(t, up, "/v1/chat/completions", "plain-key")
}

// Anthropic token counting may only reach the Anthropic endpoint with an
// Anthropic-capable key; with only an OpenAI key it estimates instead.
func TestCountTokensNeverUsesOpenAIKeyOnAnthropicEndpoint(t *testing.T) {
	t.Run("only openai key", func(t *testing.T) {
		up := &relayUp{}
		relayWith(t, up, bothEndpoints, oaiKey)
		code, out := post(t, "/v1/messages/count_tokens", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(out, "input_tokens") {
			t.Fatalf("%d %s", code, out)
		}
		for _, c := range up.calls {
			if c.path == "/v1/messages/count_tokens" {
				t.Fatalf("count_tokens reached Anthropic endpoint: %+v", c)
			}
		}
	})
	t.Run("anthropic key available", func(t *testing.T) {
		up := &relayUp{}
		relayWith(t, up, bothEndpoints, oaiKey, antKey)
		code, out := post(t, "/v1/messages/count_tokens", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(out, "5") {
			t.Fatalf("%d %s", code, out)
		}
		onlyCall(t, up, "/v1/messages/count_tokens", "ant-key")
	})
}

func TestModelFamily(t *testing.T) {
	for model, want := range map[string]family{
		"claude-opus-4-8":            famAnthropic,
		"Claude-Sonnet-5-5":          famAnthropic,
		"anthropic/claude-haiku-5-5": famAnthropic,
		"gpt-5.5":                    famOpenAI,
		"openai/gpt-4o":              famOpenAI,
		"gpt-5-codex":                famOpenAI,
		"codex-mini":                 famOpenAI,
		"o1-preview":                 famOpenAI,
		"o3-mini":                    famOpenAI,
		"openrouter/openai/o3-mini":  famOpenAI,
		"o9":                         famOpenAI,
		"deepseek-chat":              famUnknown,
		"openrouter":                 famUnknown,
		"o":                          famUnknown,
		"o0-x":                       famUnknown,
		"gemini-2.5-pro":             famUnknown,
		"":                           famUnknown,
	} {
		if got := modelFamily(model); got != want {
			t.Errorf("modelFamily(%q) = %v, want %v", model, got, want)
		}
	}
}

// candidate order within a provider: best fit first, the rest kept behind it
// in their configured order.
func TestCandidateOrderByFit(t *testing.T) {
	p := provider.Provider{ID: "relay", Chat: "http://x/v1", Anthropic: "http://x", Key: "chat-key", KeyProtocol: provider.Chat,
		Keys: []provider.KeyAccount{antKey, plainKey}}
	keyOrder := func(model string, from provider.Protocol) string {
		cs, fellBack := perKey(p, model, from)
		if fellBack {
			t.Fatal("unexpected fallback")
		}
		var ks []string
		for _, c := range cs {
			ks = append(ks, c.p.Key)
		}
		return strings.Join(ks, ",")
	}
	for _, tc := range []struct {
		model string
		from  provider.Protocol
		want  string
	}{
		{"claude-opus-4-8", provider.Anthropic, "ant-key,plain-key,chat-key"},
		{"claude-opus-4-8", provider.Chat, "ant-key,plain-key,chat-key"},
		{"gpt-5.5", provider.Anthropic, "chat-key,plain-key,ant-key"},
		{"deepseek-chat", provider.Anthropic, "ant-key,plain-key,chat-key"},
		{"deepseek-chat", provider.Chat, "chat-key,plain-key,ant-key"},
	} {
		if got := keyOrder(tc.model, tc.from); got != tc.want {
			t.Errorf("%s from %s: order %s, want %s", tc.model, tc.from, got, tc.want)
		}
	}
}
