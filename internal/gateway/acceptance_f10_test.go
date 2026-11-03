package gateway

// Acceptance tests for "an API key may be limited to one protocol",
// items 1–5, 9 and 10, checked through the gateway's HTTP surface. They
// reuse the routed upstream and the relay helpers from keys_test.go.

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// acceptKeys are the two keys of the relay: one only for its Chat endpoint,
// one only for its Anthropic endpoint.
var (
	acceptChatKey = provider.KeyAccount{Name: "GPT", Key: "k-chat", Protocol: provider.Chat}
	acceptAntKey  = provider.KeyAccount{Name: "Claude", Key: "k-ant", Protocol: provider.Anthropic}
)

// acceptOrders are the two configuration orders of the same two keys.
func acceptOrders() []struct {
	name        string
	first, rest provider.KeyAccount
} {
	return []struct {
		name        string
		first, rest provider.KeyAccount
	}{
		{"chat key first", acceptChatKey, acceptAntKey},
		{"anthropic key first", acceptAntKey, acceptChatKey},
	}
}

// 验收 1–4: 同一服务商（Chat 端点 + Anthropic 端点），两把限定协议的 Key，
// 两种配置顺序；流式与非流式；上游只收到一次且使用正确的 Key。
func TestAcceptance1to4KeyLimitedRouting(t *testing.T) {
	for _, order := range acceptOrders() {
		for _, tc := range []struct {
			name      string
			path      string
			model     string
			client    string // "anthropic" or "chat"
			stream    bool
			wantCall  string // the one upstream request
			wantReply string // the client's format
		}{
			{"1 anthropic client, claude", "/v1/messages", "relay/claude-opus-4-8", "anthropic", false,
				"/v1/messages k-ant", `"type":"message"`},
			{"1 stream: anthropic client, claude", "/v1/messages", "relay/claude-opus-4-8", "anthropic", true,
				"/v1/messages k-ant", "message_start"},
			{"2 chat client, claude", "/v1/chat/completions", "relay/claude-opus-4-8", "chat", false,
				"/v1/messages k-ant", "chat.completion"},
			{"2 stream: chat client, claude", "/v1/chat/completions", "relay/claude-opus-4-8", "chat", true,
				"/v1/messages k-ant", "chat.completion.chunk"},
			{"3 anthropic client, gpt", "/v1/messages", "relay/gpt-5.5", "anthropic", false,
				"/v1/chat/completions k-chat", `"type":"message"`},
			{"3 stream: anthropic client, gpt", "/v1/messages", "relay/gpt-5.5", "anthropic", true,
				"/v1/chat/completions k-chat", "message_start"},
		} {
			t.Run(order.name+" / "+tc.name, func(t *testing.T) {
				up := &routed{}
				srv := httptest.NewServer(up)
				defer srv.Close()
				saveRelay(t, relayWith(srv, order.first, order.rest))

				stream := "false"
				if tc.stream {
					stream = "true"
				}
				var req string
				if tc.client == "anthropic" {
					req = `{"model":"` + tc.model + `","max_tokens":16,"stream":` + stream +
						`,"messages":[{"role":"user","content":"hi"}]}`
				} else {
					req = `{"model":"` + tc.model + `","stream":` + stream +
						`,"messages":[{"role":"user","content":"hi"}]}`
				}
				code, body := post(t, tc.path, req)
				if code != 200 {
					t.Fatalf("status %d, body %s", code, body)
				}
				if !strings.Contains(body, tc.wantReply) {
					t.Fatalf("client reply is not in its own format (want %s): %s", tc.wantReply, body)
				}
				// exactly one upstream request, with the key for its endpoint
				if len(up.calls) != 1 || up.calls[0] != tc.wantCall {
					t.Fatalf("upstream got %v, want exactly [%s]", up.calls, tc.wantCall)
				}
			})
		}
	}
}

// 验收 5: modelFamily 对 openrouter/openai/o3-mini 为 openai，对 deepseek-chat 为 unknown；
// 无法判断时优先选能直接讲客户端协议的 Key，与配置顺序无关，名称不偏向任何 Key。
func TestAcceptance5UnknownModelPrefersClientProtocolKey(t *testing.T) {
	if got := modelFamily("openrouter/openai/o3-mini"); got != familyOpenAI {
		t.Fatalf("modelFamily(openrouter/openai/o3-mini) = %q, want openai", got)
	}
	if got := modelFamily("deepseek-chat"); got != familyUnknown {
		t.Fatalf("modelFamily(deepseek-chat) = %q, want unknown", got)
	}

	for _, order := range acceptOrders() {
		p := provider.Provider{ID: "relay", Name: "Relay", Chat: "http://chat/v1", Anthropic: "http://ant",
			Key: order.first.Key, KeyName: order.first.Name, KeyProtocol: order.first.Protocol,
			Keys: []provider.KeyAccount{order.rest}}
		if got := strings.Join(keysOf(perKey(provider.Chat, p, "deepseek-chat")), ","); got != "k-chat,k-ant" {
			t.Fatalf("%s: chat client, deepseek-chat: keys %s, want k-chat,k-ant", order.name, got)
		}
		if got := strings.Join(keysOf(perKey(provider.Anthropic, p, "deepseek-chat")), ","); got != "k-ant,k-chat" {
			t.Fatalf("%s: anthropic client, deepseek-chat: keys %s, want k-ant,k-chat", order.name, got)
		}
		// an OpenAI-family name still ranks the OpenAI key first, from any client
		if got := strings.Join(keysOf(perKey(provider.Anthropic, p, "openrouter/openai/o3-mini")), ","); got != "k-chat,k-ant" {
			t.Fatalf("%s: o3-mini from anthropic: keys %s, want k-chat,k-ant", order.name, got)
		}
	}

	// end to end: an unknown model from a chat client goes out on the chat
	// key only, whichever key is listed first
	for _, order := range acceptOrders() {
		t.Run(order.name, func(t *testing.T) {
			up := &routed{}
			srv := httptest.NewServer(up)
			defer srv.Close()
			saveRelay(t, relayWith(srv, order.first, order.rest))
			code, body := post(t, "/v1/chat/completions", `{"model":"relay/deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || !strings.Contains(body, "chat.completion") {
				t.Fatalf("status %d, body %s", code, body)
			}
			if len(up.calls) != 1 || up.calls[0] != "/v1/chat/completions k-chat" {
				t.Fatalf("upstream got %v, want exactly [/v1/chat/completions k-chat]", up.calls)
			}
		})
	}
}

// 验收 9: 限定协议在该服务商没有对应端点时，请求照常处理，不报错、不使用该 Key。
func TestAcceptance9LimitedKeyWithoutEndpointIsIgnored(t *testing.T) {
	gen := provider.KeyAccount{Name: "Any", Key: "k-gen"}
	for _, limited := range []provider.KeyAccount{
		{Name: "Claude", Key: "k-ant", Protocol: provider.Anthropic},
		{Name: "Resp", Key: "k-resp", Protocol: provider.Responses},
	} {
		for _, order := range []struct {
			name        string
			first, rest provider.KeyAccount
		}{
			{"limited key first", limited, gen},
			{"general key first", gen, limited},
		} {
			t.Run(limited.Name+", "+order.name, func(t *testing.T) {
				up := &routed{}
				srv := httptest.NewServer(up)
				defer srv.Close()
				// a Chat-only vendor: neither Anthropic nor Responses exists
				p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Models: []string{"claude-opus-4-8", "gpt-5.5"},
					Key: order.first.Key, KeyName: order.first.Name, KeyProtocol: order.first.Protocol,
					Keys: []provider.KeyAccount{order.rest}}
				saveRelay(t, p)

				code, body := post(t, "/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
				if code != 200 || !strings.Contains(body, `"type":"message"`) {
					t.Fatalf("anthropic client: status %d, body %s", code, body)
				}
				if len(up.calls) != 1 || up.calls[0] != "/v1/chat/completions k-gen" {
					t.Fatalf("anthropic client: upstream got %v, want exactly [/v1/chat/completions k-gen]", up.calls)
				}

				up.calls = nil
				code, body = post(t, "/v1/chat/completions", `{"model":"relay/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
				if code != 200 || !strings.Contains(body, "chat.completion") {
					t.Fatalf("chat client: status %d, body %s", code, body)
				}
				if len(up.calls) != 1 || up.calls[0] != "/v1/chat/completions k-gen" {
					t.Fatalf("chat client: upstream got %v, want exactly [/v1/chat/completions k-gen]", up.calls)
				}
			})
		}
	}
}

// 验收 10: 只有 OpenAI 专用 Key 可用时，count_tokens 不向 Anthropic 端点发请求，仍返回估算。
func TestAcceptance10CountTokensWithOnlyOpenAIKeys(t *testing.T) {
	const countBody = `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`
	for _, tc := range []struct {
		name        string
		first, rest provider.KeyAccount
	}{
		{"chat-limited key", acceptChatKey, provider.KeyAccount{}},
		{"responses-limited key, then chat-limited", provider.KeyAccount{Name: "Resp", Key: "k-resp", Protocol: provider.Responses}, acceptChatKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &routed{}
			srv := httptest.NewServer(up)
			defer srv.Close()
			p := relayWith(srv, tc.first)
			if tc.rest.Key != "" {
				p = relayWith(srv, tc.first, tc.rest)
			}
			saveRelay(t, p)
			code, body := post(t, "/v1/messages/count_tokens", countBody)
			if code != 200 || !strings.Contains(body, "input_tokens") {
				t.Fatalf("status %d, body %s", code, body)
			}
			if len(up.calls) != 0 {
				t.Fatalf("upstream got %v, want no request at all", up.calls)
			}
		})
	}
}
