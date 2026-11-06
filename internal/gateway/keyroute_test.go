package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func TestFamilyOfModelName(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  family
	}{
		{"claude-opus-4-8", anthropicFamily},
		{"relay/claude-x", anthropicFamily},
		{"Claude-Sonnet", anthropicFamily},
		{"gpt-5.5", openAIFamily},
		{"openrouter/openai/o3-mini", openAIFamily},
		{"o1", openAIFamily},
		{"o9-pro", openAIFamily},
		{"codex-mini", openAIFamily},
		{"openai/gpt-codex-x", openAIFamily},
		{"deepseek-chat", unknownFamily},
		{"", unknownFamily},
	} {
		if got := familyOf(tc.model); got != tc.want {
			t.Errorf("familyOf(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

// twoKeys is a provider with an OpenAI-only key and an Anthropic-only key,
// in the order chatFirst says.
func twoKeys(chatFirst bool) provider.Provider {
	chatKey := provider.KeyAccount{Name: "chat", Key: "sk-chat", Protocol: provider.Chat}
	antKey := provider.KeyAccount{Name: "ant", Key: "sk-ant", Protocol: provider.Anthropic}
	p := provider.Provider{ID: "relay", Name: "Relay", Models: []string{"claude-opus-4-8", "gpt-5.5"}}
	first, second := antKey, chatKey
	if chatFirst {
		first, second = chatKey, antKey
	}
	p.Key, p.KeyName, p.KeyProtocol = first.Key, first.Name, first.Protocol
	p.Keys = []provider.KeyAccount{second}
	p.Chat, p.Anthropic = "http://chat.invalid/v1", "http://ant.invalid"
	return p
}

func TestKeysFitTheModel(t *testing.T) {
	for _, chatFirst := range []bool{true, false} {
		p := twoKeys(chatFirst)
		for _, tc := range []struct {
			model string
			from  provider.Protocol
			want  string
		}{
			{"claude-opus-4-8", provider.Anthropic, "sk-ant"},
			{"claude-opus-4-8", provider.Chat, "sk-ant"},
			{"gpt-5.5", provider.Anthropic, "sk-chat"},
			{"gpt-5.5", provider.Chat, "sk-chat"},
			// a model we can't place: the key that speaks the client's protocol directly
			{"deepseek-chat", provider.Chat, "sk-chat"},
			{"deepseek-chat", provider.Anthropic, "sk-ant"},
		} {
			got := providerPlan(p, tc.model, tc.from)
			if len(got) != 2 {
				t.Fatalf("chatFirst=%v %s from %s: %d candidates, want 2", chatFirst, tc.model, tc.from, len(got))
			}
			if got[0].p.Key != tc.want {
				t.Errorf("chatFirst=%v %s from %s: first key %s, want %s", chatFirst, tc.model, tc.from, got[0].p.Key, tc.want)
			}
		}
	}
}

func TestLimitedCandidateKeepsOnlyItsEndpoint(t *testing.T) {
	p := twoKeys(true)
	for _, c := range providerPlan(p, "claude-opus-4-8", provider.Chat) {
		switch c.p.Key {
		case "sk-ant":
			if c.p.Chat != "" || c.p.Responses != "" || c.p.Anthropic == "" {
				t.Errorf("Anthropic key sees endpoints chat=%q responses=%q anthropic=%q", c.p.Chat, c.p.Responses, c.p.Anthropic)
			}
		case "sk-chat":
			if c.p.Chat == "" || c.p.Anthropic != "" {
				t.Errorf("chat key sees endpoints chat=%q anthropic=%q", c.p.Chat, c.p.Anthropic)
			}
		}
	}
}

func TestKeyWithoutItsEndpointIsIgnored(t *testing.T) {
	// only a Chat endpoint: the Anthropic-limited key is left out, not an error
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: "http://chat.invalid/v1",
		KeyProtocol: provider.Anthropic, Key: "sk-ant",
		Keys: []provider.KeyAccount{{Key: "sk-chat", Protocol: provider.Chat}}}
	got := providerPlan(p, "claude-opus-4-8", provider.Chat)
	if len(got) != 1 || got[0].p.Key != "sk-chat" {
		t.Fatalf("candidates %+v, want only sk-chat", got)
	}
	// every key ignored: the default behavior, as before limits existed
	only := provider.Provider{ID: "relay", Name: "Relay", Chat: "http://chat.invalid/v1",
		KeyProtocol: provider.Anthropic, Key: "sk-ant"}
	got = providerPlan(only, "claude-opus-4-8", provider.Chat)
	if len(got) != 1 || got[0].p.Key != "sk-ant" {
		t.Fatalf("all-ignored candidates %+v, want the default single candidate", got)
	}
}

func TestUnlimitedKeysKeepTheirOrder(t *testing.T) {
	p := provider.Provider{ID: "plain", Name: "Plain", Key: "k1", Chat: "http://chat.invalid/v1",
		Anthropic: "http://ant.invalid", Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3"}}}
	for _, model := range []string{"claude-opus-4-8", "gpt-5.5", "deepseek-chat"} {
		got := providerPlan(p, model, provider.Anthropic)
		var keys []string
		for _, c := range got {
			keys = append(keys, c.p.Key)
		}
		if strings.Join(keys, ",") != "k1,k2,k3" {
			t.Errorf("%s: order %v, want k1,k2,k3", model, keys)
		}
	}
}

func TestAnthropicViewNeedsAnAnthropicKey(t *testing.T) {
	// only an OpenAI key: no key may reach the Anthropic endpoint
	openAIOnly := provider.Provider{ID: "relay", Name: "Relay", Key: "sk-chat", KeyProtocol: provider.Chat,
		Chat: "http://chat.invalid/v1", Anthropic: "http://ant.invalid"}
	if _, ok := anthropicView(openAIOnly); ok {
		t.Error("OpenAI-only provider offered an Anthropic view")
	}
	both := twoKeys(true)
	q, ok := anthropicView(both)
	if !ok || q.Key != "sk-ant" || q.Chat != "" {
		t.Errorf("anthropicView = %q, %v; want sk-ant with only the Anthropic endpoint", q.Key, ok)
	}
}

// setupRelay saves p with two fake upstreams: its OpenAI Chat endpoint and
// its Anthropic one.
func setupRelay(t *testing.T, p provider.Provider, chat, ant *fake) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	cu := httptest.NewServer(chat)
	t.Cleanup(cu.Close)
	au := httptest.NewServer(ant)
	t.Cleanup(au.Close)
	p.Chat, p.Anthropic = cu.URL+"/v1", au.URL
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

const antJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

const antSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

data: {"type":"message_stop"}

`

const chatSSE = `data: {"id":"c1","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}

data: {"id":"c1","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}

data: [DONE]

`

func TestAnthropicClientClaudeGoesStraightToAnthropic(t *testing.T) {
	for _, chatFirst := range []bool{true, false} {
		chat := &fake{t: t, ctype: "application/json", reply: `{"id":"x","choices":[]}`}
		ant := &fake{t: t, ctype: "application/json", reply: antJSON}
		setupRelay(t, twoKeys(chatFirst), chat, ant)
		code, body := post(t, "/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 {
			t.Fatalf("chatFirst=%v: status %d: %s", chatFirst, code, body)
		}
		if len(chat.got) != 0 {
			t.Errorf("chatFirst=%v: OpenAI endpoint got a request: %s", chatFirst, chat.got)
		}
		if ant.path != "/v1/messages" || ant.head.Get("x-api-key") != "sk-ant" {
			t.Errorf("chatFirst=%v: upstream path %s, headers %v", chatFirst, ant.path, ant.head)
		}
	}
}

func TestChatClientClaudeTranslatesToAnthropicKey(t *testing.T) {
	for _, stream := range []string{"false", "true"} {
		for _, chatFirst := range []bool{true, false} {
			chat := &fake{t: t, reply: chatSSE}
			ant := &fake{t: t, reply: antSSE}
			setupRelay(t, twoKeys(chatFirst), chat, ant)
			code, body := post(t, "/v1/chat/completions", `{"model":"relay/claude-opus-4-8","stream":`+stream+`,"messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 {
				t.Fatalf("stream=%s chatFirst=%v: status %d: %s", stream, chatFirst, code, body)
			}
			if len(chat.got) != 0 {
				t.Errorf("stream=%s chatFirst=%v: OpenAI endpoint got a request", stream, chatFirst)
			}
			if ant.path != "/v1/messages" || ant.head.Get("x-api-key") != "sk-ant" {
				t.Errorf("stream=%s chatFirst=%v: upstream path %s, headers %v", stream, chatFirst, ant.path, ant.head)
			}
			if !strings.Contains(body, "hello") {
				t.Errorf("stream=%s chatFirst=%v: reply lacks the answer: %s", stream, chatFirst, body)
			}
		}
	}
}

func TestAnthropicClientGPTTranslatesToOpenAIKey(t *testing.T) {
	for _, chatFirst := range []bool{true, false} {
		chat := &fake{t: t, reply: chatSSE}
		ant := &fake{t: t, reply: antSSE}
		setupRelay(t, twoKeys(chatFirst), chat, ant)
		code, body := post(t, "/v1/messages", `{"model":"relay/gpt-5.5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 {
			t.Fatalf("chatFirst=%v: status %d: %s", chatFirst, code, body)
		}
		if len(ant.got) != 0 {
			t.Errorf("chatFirst=%v: Anthropic endpoint got a request", chatFirst)
		}
		if chat.path != "/v1/chat/completions" || !strings.Contains(chat.head.Get("Authorization"), "sk-chat") {
			t.Errorf("chatFirst=%v: upstream path %s, headers %v", chatFirst, chat.path, chat.head)
		}
	}
}

func TestAnthropicCountTokensSkipsWithoutAnthropicKey(t *testing.T) {
	// only an OpenAI key: the estimate, and no request to Anthropic's endpoint
	openAIOnly := provider.Provider{ID: "relay", Name: "Relay", Key: "sk-chat", KeyProtocol: provider.Chat,
		Models: []string{"claude-opus-4-8"}}
	chat := &fake{t: t, ctype: "application/json", reply: `{}`}
	ant := &fake{t: t, ctype: "application/json", reply: `{"input_tokens":999}`}
	setupRelay(t, openAIOnly, chat, ant)
	code, body := post(t, "/v1/messages/count_tokens", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hello there"}]}`)
	if code != 200 || !strings.Contains(body, "input_tokens") {
		t.Fatalf("status %d: %s", code, body)
	}
	if len(ant.got) != 0 {
		t.Errorf("Anthropic endpoint got a count_tokens request: %s", ant.got)
	}
	if strings.Contains(body, "999") {
		t.Errorf("reply came from upstream: %s", body)
	}
}
