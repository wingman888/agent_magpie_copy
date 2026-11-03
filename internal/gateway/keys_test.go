package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func TestModelFamily(t *testing.T) {
	for _, tc := range []struct{ model, want string }{
		{"openrouter/openai/o3-mini", familyOpenAI},
		{"deepseek-chat", familyUnknown},
		{"claude-opus-4-8", familyAnthropic},
		{"relay/claude-opus-4-8", familyAnthropic},
		{"Claude-Sonnet-5", familyAnthropic},
		{"gpt-5.5", familyOpenAI},
		{"openai/gpt-4o", familyOpenAI},
		{"gpt-5-codex", familyOpenAI},
		{"o1-preview", familyOpenAI},
		{"o9", familyOpenAI},
		{"omni-model", familyUnknown},
		{"ollama/qwen2.5", familyUnknown},
		{"", familyUnknown},
	} {
		if got := modelFamily(tc.model); got != tc.want {
			t.Errorf("modelFamily(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

// keysOf is the key of each candidate, in the order requests try them.
func keysOf(cs []candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.p.Key)
	}
	return out
}

func TestKeysSuitTheModel(t *testing.T) {
	chat := provider.KeyAccount{Name: "GPT", Key: "k-chat", Protocol: provider.Chat}
	ant := provider.KeyAccount{Name: "Claude", Key: "k-ant", Protocol: provider.Anthropic}
	gen1 := provider.KeyAccount{Name: "Any1", Key: "k-gen1"}
	gen2 := provider.KeyAccount{Name: "Any2", Key: "k-gen2"}
	both := func(first provider.KeyAccount, rest ...provider.KeyAccount) provider.Provider {
		return provider.Provider{ID: "relay", Name: "Relay", Chat: "http://chat/v1", Anthropic: "http://ant",
			Key: first.Key, KeyName: first.Name, KeyProtocol: first.Protocol, Keys: rest}
	}
	for _, tc := range []struct {
		name  string
		p     provider.Provider
		from  provider.Protocol
		model string
		want  []string
	}{
		{"claude from anthropic: its own key first", both(chat, ant), provider.Anthropic, "claude-opus-4-8", []string{"k-ant", "k-chat"}},
		{"claude from anthropic, key order reversed", both(ant, chat), provider.Anthropic, "claude-opus-4-8", []string{"k-ant", "k-chat"}},
		{"claude from chat: anthropic key still first", both(chat, ant), provider.Chat, "claude-opus-4-8", []string{"k-ant", "k-chat"}},
		{"gpt from anthropic: openai key first", both(ant, chat), provider.Anthropic, "gpt-5.5", []string{"k-chat", "k-ant"}},
		{"gpt from anthropic, key order reversed", both(chat, ant), provider.Anthropic, "gpt-5.5", []string{"k-chat", "k-ant"}},
		{"unknown model from chat: key speaking chat first", both(ant, chat), provider.Chat, "deepseek-chat", []string{"k-chat", "k-ant"}},
		{"unknown model from chat, reversed", both(chat, ant), provider.Chat, "deepseek-chat", []string{"k-chat", "k-ant"}},
		{"unknown model from anthropic: anthropic key first", both(chat, ant), provider.Anthropic, "deepseek-chat", []string{"k-ant", "k-chat"}},
		{"unknown model from anthropic, reversed", both(ant, chat), provider.Anthropic, "deepseek-chat", []string{"k-ant", "k-chat"}},
		{"unknown model, no key speaks the client: order kept", both(chat, ant), provider.Responses, "deepseek-chat", []string{"k-chat", "k-ant"}},
		{"claude: any key ranks between own and other family", provider.Provider{ID: "relay", Chat: "http://chat/v1", Anthropic: "http://ant",
			Key: gen1.Key, KeyName: gen1.Name, Keys: []provider.KeyAccount{chat, ant}}, provider.Anthropic, "claude-opus-4-8", []string{"k-ant", "k-gen1", "k-chat"}},
		{"same tier keeps config order", provider.Provider{ID: "relay", Chat: "http://chat/v1", Anthropic: "http://ant",
			Key: gen1.Key, KeyName: gen1.Name, Keys: []provider.KeyAccount{gen2}}, provider.Anthropic, "claude-opus-4-8", []string{"k-gen1", "k-gen2"}},
		{"key limited to a protocol the provider lacks is ignored", provider.Provider{ID: "relay", Chat: "http://chat/v1",
			Key: "k-chat", KeyName: "GPT", KeyProtocol: provider.Chat, Keys: []provider.KeyAccount{ant}}, provider.Chat, "claude-opus-4-8", []string{"k-chat"}},
		{"key limited to responses, provider lacks it: ignored, others kept", provider.Provider{ID: "relay", Chat: "http://chat/v1", Anthropic: "http://ant",
			Key: "k-resp", KeyName: "Resp", KeyProtocol: provider.Responses, Keys: []provider.KeyAccount{gen1}}, provider.Chat, "gpt-5.5", []string{"k-gen1"}},
		{"only key limited to responses, provider lacks it: no candidate", provider.Provider{ID: "relay", Chat: "http://chat/v1",
			Key: "k-resp", KeyName: "Resp", KeyProtocol: provider.Responses}, provider.Chat, "gpt-5.5", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := keysOf(perKey(tc.from, tc.p, tc.model))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("keys %v, want %v", got, tc.want)
			}
		})
	}
}

func TestKeysLimitedToAMissingProtocolAreNoCandidates(t *testing.T) {
	// Every key is for an endpoint the provider lacks: none of them may be
	// sent to another protocol's endpoint, so the provider yields no candidate.
	p := provider.Provider{ID: "relay", Chat: "http://chat/v1", Key: "k-ant", KeyName: "Claude", KeyProtocol: provider.Anthropic}
	if cs := perKey(provider.Anthropic, p, "claude-opus-4-8"); len(cs) != 0 {
		t.Fatalf("candidates %+v, want none", cs)
	}
	// a provider with no key at all still gets its default candidate, as before
	bare := provider.Provider{ID: "relay", Chat: "http://chat/v1"}
	if cs := perKey(provider.Anthropic, bare, "claude-opus-4-8"); len(cs) != 1 || cs[0].p.Chat == "" {
		t.Fatalf("keyless candidates %+v", cs)
	}
}

// routed is an upstream with a Chat and an Anthropic endpoint. It records
// "path key" for each request, the key from x-api-key or the bearer token.
type routed struct {
	calls []string
}

const (
	routedChatJSON = `{"id":"c1","object":"chat.completion","model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	routedAntJSON  = `{"id":"m1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
)

func (f *routed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	f.calls = append(f.calls, r.URL.Path+" "+key)
	body, _ := io.ReadAll(r.Body)
	stream := strings.Contains(string(body), `"stream":true`)
	switch {
	case r.URL.Path == "/v1/chat/completions" && stream:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		))
	case r.URL.Path == "/v1/chat/completions":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, routedChatJSON)
	case r.URL.Path == "/v1/messages" && stream:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
		))
	case r.URL.Path == "/v1/messages":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, routedAntJSON)
	case r.URL.Path == "/v1/messages/count_tokens":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":7}`)
	default:
		http.NotFound(w, r)
	}
}

// saveRelay saves provider "relay" as p, with the given keys, and clears
// the rest state a previous test may have left.
func saveRelay(t *testing.T, p provider.Provider) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// relayWith is the relay provider with both endpoints on up; the first key
// is the one in use and the others are saved after it, in order.
func relayWith(up *httptest.Server, first provider.KeyAccount, rest ...provider.KeyAccount) provider.Provider {
	return provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Anthropic: up.URL,
		Models: []string{"claude-opus-4-8", "gpt-5.5"},
		Key:    first.Key, KeyName: first.Name, KeyProtocol: first.Protocol, Keys: rest}
}

func TestRelayKeyForTheModel(t *testing.T) {
	ant := provider.KeyAccount{Name: "Claude", Key: "k-ant", Protocol: provider.Anthropic}
	chat := provider.KeyAccount{Name: "GPT", Key: "k-chat", Protocol: provider.Chat}
	for _, order := range []string{"chat key first", "anthropic key first"} {
		for _, tc := range []struct {
			name, path, model string
			stream            bool
			client            string // "anthropic" or "chat"
			wantCalls         string
			wantBody          string
		}{
			{"A: anthropic client, claude", "/v1/messages", "relay/claude-opus-4-8", false, "anthropic",
				"/v1/messages k-ant", `"type":"message"`},
			{"A stream: anthropic client, claude", "/v1/messages", "relay/claude-opus-4-8", true, "anthropic",
				"/v1/messages k-ant", "message_start"},
			{"B: chat client, claude", "/v1/chat/completions", "relay/claude-opus-4-8", false, "chat",
				"/v1/messages k-ant", "chat.completion"},
			{"B stream: chat client, claude", "/v1/chat/completions", "relay/claude-opus-4-8", true, "chat",
				"/v1/messages k-ant", "chat.completion.chunk"},
			{"C: anthropic client, gpt", "/v1/messages", "relay/gpt-5.5", false, "anthropic",
				"/v1/chat/completions k-chat", `"type":"message"`},
			{"C stream: anthropic client, gpt", "/v1/messages", "relay/gpt-5.5", true, "anthropic",
				"/v1/chat/completions k-chat", "message_start"},
			{"unknown model, chat client: key speaking chat", "/v1/chat/completions", "relay/deepseek-chat", false, "chat",
				"/v1/chat/completions k-chat", "chat.completion"},
		} {
			t.Run(order+" / "+tc.name, func(t *testing.T) {
				up := &routed{}
				srv := httptest.NewServer(up)
				defer srv.Close()
				if order == "chat key first" {
					saveRelay(t, relayWith(srv, chat, ant))
				} else {
					saveRelay(t, relayWith(srv, ant, chat))
				}
				var req string
				if tc.client == "anthropic" {
					req = `{"model":"` + tc.model + `","max_tokens":16,"stream":` + map[bool]string{true: "true", false: "false"}[tc.stream] +
						`,"messages":[{"role":"user","content":"hi"}]}`
				} else {
					req = `{"model":"` + tc.model + `","stream":` + map[bool]string{true: "true", false: "false"}[tc.stream] +
						`,"messages":[{"role":"user","content":"hi"}]}`
				}
				code, body := post(t, tc.path, req)
				if code != 200 || !strings.Contains(body, tc.wantBody) {
					t.Fatalf("status %d, body %s", code, body)
				}
				if len(up.calls) != 1 || up.calls[0] != tc.wantCalls {
					t.Fatalf("upstream got %v, want exactly [%s]", up.calls, tc.wantCalls)
				}
			})
		}
	}
}

func TestRelayIgnoresKeysWithoutEndpoint(t *testing.T) {
	up := &routed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	// a chat-only vendor: the Anthropic-limited key is not in use, so the
	// request goes out with the general key alone
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Models: []string{"claude-opus-4-8"},
		Key: "k-gen", Keys: []provider.KeyAccount{{Name: "Claude", Key: "k-ant", Protocol: provider.Anthropic}}}
	saveRelay(t, p)
	code, body := post(t, "/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"type":"message"`) {
		t.Fatalf("status %d, body %s", code, body)
	}
	if strings.Join(up.calls, ",") != "/v1/chat/completions k-gen" {
		t.Fatalf("upstream got %v", up.calls)
	}
}

func TestRelayAllKeysWithoutEndpointFallBack(t *testing.T) {
	up := &routed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	// the only key is for the Anthropic endpoint, which this vendor lacks:
	// the key is never sent to the Chat endpoint, so the request fails and
	// the upstream receives nothing with that key
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Models: []string{"claude-opus-4-8"},
		Key: "k-ant", KeyName: "Claude", KeyProtocol: provider.Anthropic}
	saveRelay(t, p)
	code, _ := post(t, "/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	if code < 400 {
		t.Fatalf("status %d, want an error: no key may be used", code)
	}
	for _, c := range up.calls {
		if strings.HasSuffix(c, " k-ant") {
			t.Fatalf("upstream got the anthropic-limited key: %v", up.calls)
		}
	}
	if len(up.calls) != 0 {
		t.Fatalf("upstream got %v, want nothing", up.calls)
	}
}

func TestCountTokensWithResponsesOnlyKey(t *testing.T) {
	// a Chat-only vendor whose only key is limited to Responses: counting
	// tokens estimates, and nothing is sent upstream
	const countBody = `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`
	up := &routed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	saveRelay(t, provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Models: []string{"claude-opus-4-8"},
		Key: "k-resp", KeyName: "Resp", KeyProtocol: provider.Responses})
	code, body := post(t, "/v1/messages/count_tokens", countBody)
	if code != 200 || !strings.Contains(body, "input_tokens") {
		t.Fatalf("status %d, body %s", code, body)
	}
	if len(up.calls) != 0 {
		t.Fatalf("upstream got %v, want nothing", up.calls)
	}
}

func TestResponsesKeyWithoutEndpointIsSkipped(t *testing.T) {
	// the first key is for Responses, which this vendor lacks: the request
	// is not an error and goes out with the general key on Chat
	up := &routed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	saveRelay(t, relayWith(srv, provider.KeyAccount{Name: "Resp", Key: "k-resp", Protocol: provider.Responses},
		provider.KeyAccount{Name: "Any", Key: "k-gen"}))
	code, body := post(t, "/v1/chat/completions", `{"model":"relay/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "chat.completion") {
		t.Fatalf("status %d, body %s", code, body)
	}
	if strings.Join(up.calls, ",") != "/v1/chat/completions k-gen" {
		t.Fatalf("upstream got %v", up.calls)
	}
}

func TestCountTokensNeverUsesAnthropicWithOpenAIKeyOnly(t *testing.T) {
	const countBody = `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`
	t.Run("only an openai key", func(t *testing.T) {
		up := &routed{}
		srv := httptest.NewServer(up)
		defer srv.Close()
		saveRelay(t, relayWith(srv, provider.KeyAccount{Name: "GPT", Key: "k-chat", Protocol: provider.Chat}))
		code, body := post(t, "/v1/messages/count_tokens", countBody)
		if code != 200 || !strings.Contains(body, "input_tokens") {
			t.Fatalf("status %d, body %s", code, body)
		}
		if len(up.calls) != 0 {
			t.Fatalf("upstream got %v, want nothing", up.calls)
		}
	})
	t.Run("an anthropic key too", func(t *testing.T) {
		up := &routed{}
		srv := httptest.NewServer(up)
		defer srv.Close()
		saveRelay(t, relayWith(srv, provider.KeyAccount{Name: "GPT", Key: "k-chat", Protocol: provider.Chat},
			provider.KeyAccount{Name: "Claude", Key: "k-ant", Protocol: provider.Anthropic}))
		code, body := post(t, "/v1/messages/count_tokens", countBody)
		if code != 200 || !strings.Contains(body, "7") {
			t.Fatalf("status %d, body %s", code, body)
		}
		if strings.Join(up.calls, ",") != "/v1/messages/count_tokens k-ant" {
			t.Fatalf("upstream got %v", up.calls)
		}
	})
}
