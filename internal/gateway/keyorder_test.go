package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// protoGateUp is a relay with a Chat and an Anthropic endpoint on one
// server. It records each request as "path key", the key being the one the
// request carried.
type protoGateUp struct {
	mu   sync.Mutex
	hits []string
}

var (
	protoGateChatSSE = sse(
		`data: {"id":"c1","model":"gpt-5.5","choices":[{"delta":{"role":"assistant","content":"hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
		`data: [DONE]`)
	protoGateChatJSON = `{"id":"c1","object":"chat.completion","model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`
	protoGateAnthSSE  = sse(
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":3}}}",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`data: {"type":"message_stop"}`)
	protoGateAnthJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`
)

func (u *protoGateUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	u.mu.Lock()
	u.hits = append(u.hits, r.URL.Path+" "+key)
	u.mu.Unlock()
	stream := strings.Contains(string(b), `"stream":true`)
	chat := strings.HasSuffix(r.URL.Path, "/chat/completions")
	switch {
	case chat && stream:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, protoGateChatSSE)
	case chat:
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, protoGateChatJSON)
	case stream:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, protoGateAnthSSE)
	default:
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, protoGateAnthJSON)
	}
}

func (u *protoGateUp) tried() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return strings.Join(u.hits, ", ")
}

// protoGateSetup is a provider "relay" with a Chat endpoint, and an Anthropic
// one when anthropic is true, holding first and rest as its keys.
func protoGateSetup(t *testing.T, anthropic bool, first provider.KeyAccount, rest ...provider.KeyAccount) *protoGateUp {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	up := &protoGateUp{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: "relay", Name: "Relay", Models: []string{"claude-opus-4-8", "gpt-5.5"},
		Chat: srv.URL + "/v1", Key: first.Key, KeyName: first.Name, KeyProtocol: first.Protocol, Keys: rest}
	if anthropic {
		p.Anthropic = srv.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return up
}

// protoGateKeys are the relay's two keys, in the order asked for.
func protoGateKeys(chatFirst bool) (provider.KeyAccount, []provider.KeyAccount) {
	chat := provider.KeyAccount{Name: "Chat", Key: "chat-key", Protocol: provider.Chat}
	anth := provider.KeyAccount{Name: "Anthropic", Key: "anthropic-key", Protocol: provider.Anthropic}
	if chatFirst {
		return chat, []provider.KeyAccount{anth}
	}
	return anth, []provider.KeyAccount{chat}
}

func TestProtoGate_ModelFamily(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  family
	}{
		{"openrouter/openai/o3-mini", famOpenAI},
		{"o1-preview", famOpenAI},
		{"gpt-5.5", famOpenAI},
		{"codex-mini-latest", famOpenAI},
		{"deepseek-chat", famUnknown},
		{"gemini-2.5-pro", famUnknown},
		{"claude-opus-4-8", famAnthropic},
		{"relay/claude-opus-4-8", famAnthropic},
		{"Claude-Sonnet", famAnthropic},
	} {
		if got := modelFamily(tc.model); got != tc.want {
			t.Errorf("modelFamily(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

// TestProtoGate_BestFitKeyFirst: the order keys are tried in, for each
// model and client protocol. The configured order is chat, anthropic, then
// an unlimited key; equal fit keeps that order.
func TestProtoGate_BestFitKeyFirst(t *testing.T) {
	p := provider.Provider{ID: "relay", Chat: "http://chat.invalid", Anthropic: "http://anthropic.invalid",
		Key: "chat-key", KeyProtocol: provider.Chat,
		Keys: []provider.KeyAccount{{Key: "anthropic-key", Protocol: provider.Anthropic}, {Key: "any-key"}}}
	keysOf := func(model string, from provider.Protocol) string {
		var out []string
		for _, c := range usableCandidates(p, model, from) {
			out = append(out, c.p.Key)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		model string
		from  provider.Protocol
		want  string
	}{
		{"claude-opus-4-8", provider.Anthropic, "anthropic-key,any-key,chat-key"},
		{"claude-opus-4-8", provider.Chat, "anthropic-key,any-key,chat-key"},
		{"gpt-5.5", provider.Anthropic, "chat-key,any-key,anthropic-key"},
		{"gpt-5.5", provider.Chat, "chat-key,any-key,anthropic-key"},
		{"deepseek-chat", provider.Chat, "chat-key,any-key,anthropic-key"},
		{"deepseek-chat", provider.Anthropic, "anthropic-key,any-key,chat-key"},
	} {
		if got := keysOf(tc.model, tc.from); got != tc.want {
			t.Errorf("%s from %s: got %s, want %s", tc.model, tc.from, got, tc.want)
		}
	}

	// no Anthropic endpoint: the Anthropic-limited key is ignored, not an
	// error; the unlimited key needs translating, the chat key is another
	// vendor's for a Claude model, so it comes last
	noAnth := p
	noAnth.Anthropic = ""
	var got []string
	for _, c := range usableCandidates(noAnth, "claude-opus-4-8", provider.Anthropic) {
		got = append(got, c.p.Key)
	}
	if strings.Join(got, ",") != "any-key,chat-key" {
		t.Errorf("without an Anthropic endpoint: got %v", got)
	}
}

// Scenario 1 and 4: an Anthropic client asks for a Claude model. The request
// goes once to the Anthropic endpoint, with the Anthropic key, untranslated;
// which key is listed first makes no difference.
func TestProtoGate_AnthropicClientClaudeUsesAnthropicKey(t *testing.T) {
	for _, chatFirst := range []bool{true, false} {
		for _, stream := range []string{"false", "true"} {
			t.Run(fmt.Sprintf("chatFirst=%v stream=%s", chatFirst, stream), func(t *testing.T) {
				first, rest := protoGateKeys(chatFirst)
				up := protoGateSetup(t, true, first, rest...)
				code, body := post(t, "/v1/messages", fmt.Sprintf(`{"model":"relay/claude-opus-4-8","max_tokens":10,"stream":%s,"messages":[{"role":"user","content":"hi"}]}`, stream))
				if code != 200 || !strings.Contains(body, "hi") {
					t.Fatalf("%d %s", code, body)
				}
				if got := up.tried(); got != "/v1/messages anthropic-key" {
					t.Fatalf("upstream saw %q", got)
				}
			})
		}
	}
}

// Scenario 2: a Chat client asks for a Claude model. The request is
// translated to the Anthropic endpoint, with the Anthropic key, and the
// client gets a Chat reply.
func TestProtoGate_ChatClientClaudeTranslatesToAnthropic(t *testing.T) {
	for _, chatFirst := range []bool{true, false} {
		for _, stream := range []string{"false", "true"} {
			t.Run(fmt.Sprintf("chatFirst=%v stream=%s", chatFirst, stream), func(t *testing.T) {
				first, rest := protoGateKeys(chatFirst)
				up := protoGateSetup(t, true, first, rest...)
				code, body := post(t, "/v1/chat/completions", fmt.Sprintf(`{"model":"relay/claude-opus-4-8","stream":%s,"messages":[{"role":"user","content":"hi"}]}`, stream))
				if code != 200 || !strings.Contains(body, `"choices"`) {
					t.Fatalf("%d %s", code, body)
				}
				if got := up.tried(); got != "/v1/messages anthropic-key" {
					t.Fatalf("upstream saw %q", got)
				}
			})
		}
	}
}

// Scenario 3: an Anthropic client asks for a GPT model. The request is
// translated to the Chat endpoint, with the Chat key.
func TestProtoGate_AnthropicClientGPTTranslatesToChat(t *testing.T) {
	for _, chatFirst := range []bool{true, false} {
		for _, stream := range []string{"false", "true"} {
			t.Run(fmt.Sprintf("chatFirst=%v stream=%s", chatFirst, stream), func(t *testing.T) {
				first, rest := protoGateKeys(chatFirst)
				up := protoGateSetup(t, true, first, rest...)
				code, body := post(t, "/v1/messages", fmt.Sprintf(`{"model":"relay/gpt-5.5","max_tokens":10,"stream":%s,"messages":[{"role":"user","content":"hi"}]}`, stream))
				if code != 200 || !strings.Contains(body, "hi") {
					t.Fatalf("%d %s", code, body)
				}
				if got := up.tried(); got != "/v1/chat/completions chat-key" {
					t.Fatalf("upstream saw %q", got)
				}
			})
		}
	}
}

// Scenario 9: a limited key with no endpoint for its protocol is ignored, and
// the request goes on with the keys that can take it.
func TestProtoGate_KeyWithoutItsEndpointIsIgnored(t *testing.T) {
	up := protoGateSetup(t, false,
		provider.KeyAccount{Name: "Anthropic", Key: "anthropic-key", Protocol: provider.Anthropic},
		provider.KeyAccount{Name: "Any", Key: "any-key"})
	code, body := post(t, "/v1/chat/completions", `{"model":"relay/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"choices"`) {
		t.Fatalf("%d %s", code, body)
	}
	if got := up.tried(); got != "/v1/chat/completions any-key" {
		t.Fatalf("upstream saw %q", got)
	}
}

// Scenario 10: with only an OpenAI key, count_tokens never reaches the
// Anthropic endpoint; the count is estimated. With an Anthropic key it is
// forwarded on that key.
func TestProtoGate_CountTokensNeedsAnthropicKey(t *testing.T) {
	const countBody = `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hello there"}]}`
	up := protoGateSetup(t, true, provider.KeyAccount{Name: "Chat", Key: "chat-key", Protocol: provider.Chat})
	code, body := post(t, "/v1/messages/count_tokens", countBody)
	if code != 200 || !strings.Contains(body, "input_tokens") {
		t.Fatalf("%d %s", code, body)
	}
	if got := up.tried(); got != "" {
		t.Fatalf("count_tokens reached upstream: %q", got)
	}

	up = protoGateSetup(t, true, provider.KeyAccount{Name: "Chat", Key: "chat-key", Protocol: provider.Chat},
		provider.KeyAccount{Name: "Anthropic", Key: "anthropic-key", Protocol: provider.Anthropic})
	if code, body := post(t, "/v1/messages/count_tokens", countBody); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if got := up.tried(); got != "/v1/messages/count_tokens anthropic-key" {
		t.Fatalf("upstream saw %q", got)
	}
}
