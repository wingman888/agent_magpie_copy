package gateway

// Tests for "an API key may be limited to one protocol". This file is
// self-contained: every helper and type it uses is defined here, and every
// name carries an f10 prefix so it cannot collide with other test files.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// f10Post posts body to a fresh gateway and returns the status and reply.
func f10Post(t *testing.T, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// f10SSE joins the lines into a server-sent event stream.
func f10SSE(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n\n")
	}
	return b.String()
}

// f10KeysOf is the key of each candidate, in the order requests try them.
func f10KeysOf(cs []candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.p.Key)
	}
	return out
}

// f10Routed is an upstream with a Chat and an Anthropic endpoint. It records
// "path key" for each request, the key from x-api-key or the bearer token.
type f10Routed struct {
	calls []string
}

const (
	f10RoutedChatJSON = `{"id":"c1","object":"chat.completion","model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	f10RoutedAntJSON  = `{"id":"m1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
)

func (f *f10Routed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		io.WriteString(w, f10SSE(
			`data: {"id":"c1","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		))
	case r.URL.Path == "/v1/chat/completions":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, f10RoutedChatJSON)
	case r.URL.Path == "/v1/messages" && stream:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, f10SSE(
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
		))
	case r.URL.Path == "/v1/messages":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, f10RoutedAntJSON)
	case r.URL.Path == "/v1/messages/count_tokens":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":7}`)
	default:
		http.NotFound(w, r)
	}
}

// f10SaveRelay saves provider "relay" as p, with fresh config and cache
// directories and no resting keys left over from an earlier test.
func f10SaveRelay(t *testing.T, p provider.Provider) {
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

// f10RelayWith is the relay provider with both endpoints on up; the first key
// is the one in use and the others are saved after it, in order.
func f10RelayWith(up *httptest.Server, first provider.KeyAccount, rest ...provider.KeyAccount) provider.Provider {
	return provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Anthropic: up.URL,
		Models: []string{"claude-opus-4-8", "gpt-5.5"},
		Key:    first.Key, KeyName: first.Name, KeyProtocol: first.Protocol, Keys: rest}
}

// f10Orders are the two configuration orders of the same two keys: the first
// has its own key limited to Chat, the second to Anthropic.
var (
	f10ChatKey = provider.KeyAccount{Name: "GPT", Key: "k-chat", Protocol: provider.Chat}
	f10AntKey  = provider.KeyAccount{Name: "Claude", Key: "k-ant", Protocol: provider.Anthropic}
)

func f10Orders() []struct {
	name        string
	first, rest provider.KeyAccount
} {
	return []struct {
		name        string
		first, rest provider.KeyAccount
	}{
		{"chat key first", f10ChatKey, f10AntKey},
		{"anthropic key first", f10AntKey, f10ChatKey},
	}
}

func TestF10ModelFamily(t *testing.T) {
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

func TestF10KeysSuitTheModel(t *testing.T) {
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
			got := f10KeysOf(perKey(tc.from, tc.p, tc.model))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("keys %v, want %v", got, tc.want)
			}
		})
	}
}

func TestF10KeysLimitedToAMissingProtocolAreNoCandidates(t *testing.T) {
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

func TestF10KeyTierRanksFourLevels(t *testing.T) {
	// a vendor with a Chat and an Anthropic endpoint, and no Responses one
	p := provider.Provider{ID: "relay", Chat: "http://chat/v1", Anthropic: "http://ant"}
	chatKey := provider.KeyAccount{Name: "GPT", Key: "k-chat", Protocol: provider.Chat}
	antKey := provider.KeyAccount{Name: "Claude", Key: "k-ant", Protocol: provider.Anthropic}
	gen := provider.KeyAccount{Name: "Any", Key: "k-gen"}
	for _, tc := range []struct {
		name  string
		from  provider.Protocol
		model string
		k     provider.KeyAccount
		want  int
	}{
		// claude model: own family 0, any-key direct 1, any-key translated 2, other family 3
		{"claude, anthropic client, own key", provider.Anthropic, "claude-opus-4-8", antKey, 0},
		{"claude, anthropic client, any key direct", provider.Anthropic, "claude-opus-4-8", gen, 1},
		{"claude, responses client, any key translated", provider.Responses, "claude-opus-4-8", gen, 2},
		{"claude, responses client, own key", provider.Responses, "claude-opus-4-8", antKey, 0},
		{"claude, anthropic client, openai key", provider.Anthropic, "claude-opus-4-8", chatKey, 3},
		{"claude, chat client, openai key", provider.Chat, "claude-opus-4-8", chatKey, 3},
		// gpt model: own family 0, any-key direct 1, any-key translated 2, other family 3
		{"gpt, chat client, own key", provider.Chat, "gpt-5.5", chatKey, 0},
		{"gpt, chat client, any key direct", provider.Chat, "gpt-5.5", gen, 1},
		{"gpt, responses client, any key translated", provider.Responses, "gpt-5.5", gen, 2},
		{"gpt, anthropic client, anthropic key", provider.Anthropic, "gpt-5.5", antKey, 3},
		// unknown model: direct key 0, others 1; the name never favours a key
		{"unknown, chat client, own key", provider.Chat, "deepseek-chat", chatKey, 0},
		{"unknown, chat client, any key direct", provider.Chat, "deepseek-chat", gen, 0},
		{"unknown, chat client, anthropic key", provider.Chat, "deepseek-chat", antKey, 1},
		{"unknown, responses client, any key translated", provider.Responses, "deepseek-chat", gen, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := keyTier(tc.from, modelFamily(tc.model), p, tc.k); got != tc.want {
				t.Fatalf("keyTier = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestF10KeysFourLevelsOrderAndConfigOrderWithinTier(t *testing.T) {
	p := func(first provider.KeyAccount, rest ...provider.KeyAccount) provider.Provider {
		return provider.Provider{ID: "relay", Chat: "http://chat/v1", Anthropic: "http://ant",
			Key: first.Key, KeyName: first.Name, KeyProtocol: first.Protocol, Keys: rest}
	}
	chat := provider.KeyAccount{Name: "GPT", Key: "k-chat", Protocol: provider.Chat}
	chat2 := provider.KeyAccount{Name: "GPT2", Key: "k-chat2", Protocol: provider.Chat}
	ant := provider.KeyAccount{Name: "Claude", Key: "k-ant", Protocol: provider.Anthropic}
	gen1 := provider.KeyAccount{Name: "Any1", Key: "k-gen1"}
	gen2 := provider.KeyAccount{Name: "Any2", Key: "k-gen2"}
	gen3 := provider.KeyAccount{Name: "Any3", Key: "k-gen3"}
	for _, tc := range []struct {
		name  string
		p     provider.Provider
		from  provider.Protocol
		model string
		want  []string
	}{
		// claude from responses: own 0, translated generic 2, other family 3,
		// in that order whatever the configured order
		{"claude from responses, four levels", p(chat, gen1, ant), provider.Responses, "claude-opus-4-8",
			[]string{"k-ant", "k-gen1", "k-chat"}},
		{"claude from responses, configured the other way", p(ant, chat, gen1), provider.Responses, "claude-opus-4-8",
			[]string{"k-ant", "k-gen1", "k-chat"}},
		// gpt from chat: own 0, direct generic 1, other family 3
		{"gpt from chat, four levels", p(ant, gen1, chat2), provider.Chat, "gpt-5.5",
			[]string{"k-chat2", "k-gen1", "k-ant"}},
		// same tier keeps the configured order: generics 2 (translated) then openai keys 3
		{"same tiers keep configured order", p(gen1, chat, gen2, chat2, gen3), provider.Responses, "claude-opus-4-8",
			[]string{"k-gen1", "k-gen2", "k-gen3", "k-chat", "k-chat2"}},
		{"same tiers keep configured order, reversed", p(chat2, gen3, ant, gen2, chat), provider.Responses, "claude-opus-4-8",
			[]string{"k-ant", "k-gen3", "k-gen2", "k-chat2", "k-chat"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := f10KeysOf(perKey(tc.from, tc.p, tc.model))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("keys %v, want %v", got, tc.want)
			}
		})
	}
}

func TestF10RelayKeyForTheModel(t *testing.T) {
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
				up := &f10Routed{}
				srv := httptest.NewServer(up)
				defer srv.Close()
				if order == "chat key first" {
					f10SaveRelay(t, f10RelayWith(srv, chat, ant))
				} else {
					f10SaveRelay(t, f10RelayWith(srv, ant, chat))
				}
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
				code, body := f10Post(t, tc.path, req)
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

func TestF10RelayIgnoresKeysWithoutEndpoint(t *testing.T) {
	up := &f10Routed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	// a chat-only vendor: the Anthropic-limited key is not in use, so the
	// request goes out with the general key alone
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Models: []string{"claude-opus-4-8"},
		Key: "k-gen", Keys: []provider.KeyAccount{{Name: "Claude", Key: "k-ant", Protocol: provider.Anthropic}}}
	f10SaveRelay(t, p)
	code, body := f10Post(t, "/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"type":"message"`) {
		t.Fatalf("status %d, body %s", code, body)
	}
	if strings.Join(up.calls, ",") != "/v1/chat/completions k-gen" {
		t.Fatalf("upstream got %v", up.calls)
	}
}

func TestF10RelayAllKeysWithoutEndpointFallBack(t *testing.T) {
	up := &f10Routed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	// the only key is for the Anthropic endpoint, which this vendor lacks:
	// the key is never sent to the Chat endpoint, so the request fails and
	// the upstream receives nothing with that key
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Models: []string{"claude-opus-4-8"},
		Key: "k-ant", KeyName: "Claude", KeyProtocol: provider.Anthropic}
	f10SaveRelay(t, p)
	code, _ := f10Post(t, "/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
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

func TestF10CountTokensWithResponsesOnlyKey(t *testing.T) {
	// a Chat-only vendor whose only key is limited to Responses: counting
	// tokens estimates, and nothing is sent upstream
	const countBody = `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`
	up := &f10Routed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	f10SaveRelay(t, provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Models: []string{"claude-opus-4-8"},
		Key: "k-resp", KeyName: "Resp", KeyProtocol: provider.Responses})
	code, body := f10Post(t, "/v1/messages/count_tokens", countBody)
	if code != 200 || !strings.Contains(body, "input_tokens") {
		t.Fatalf("status %d, body %s", code, body)
	}
	if len(up.calls) != 0 {
		t.Fatalf("upstream got %v, want nothing", up.calls)
	}
}

func TestF10ResponsesKeyWithoutEndpointIsSkipped(t *testing.T) {
	// the first key is for Responses, which this vendor lacks: the request
	// is not an error and goes out with the general key on Chat
	up := &f10Routed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	f10SaveRelay(t, f10RelayWith(srv, provider.KeyAccount{Name: "Resp", Key: "k-resp", Protocol: provider.Responses},
		provider.KeyAccount{Name: "Any", Key: "k-gen"}))
	code, body := f10Post(t, "/v1/chat/completions", `{"model":"relay/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "chat.completion") {
		t.Fatalf("status %d, body %s", code, body)
	}
	if strings.Join(up.calls, ",") != "/v1/chat/completions k-gen" {
		t.Fatalf("upstream got %v", up.calls)
	}
}

func TestF10CountTokensNeverUsesAnthropicWithOpenAIKeyOnly(t *testing.T) {
	const countBody = `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`
	t.Run("only an openai key", func(t *testing.T) {
		up := &f10Routed{}
		srv := httptest.NewServer(up)
		defer srv.Close()
		f10SaveRelay(t, f10RelayWith(srv, provider.KeyAccount{Name: "GPT", Key: "k-chat", Protocol: provider.Chat}))
		code, body := f10Post(t, "/v1/messages/count_tokens", countBody)
		if code != 200 || !strings.Contains(body, "input_tokens") {
			t.Fatalf("status %d, body %s", code, body)
		}
		if len(up.calls) != 0 {
			t.Fatalf("upstream got %v, want nothing", up.calls)
		}
	})
	t.Run("an anthropic key too", func(t *testing.T) {
		up := &f10Routed{}
		srv := httptest.NewServer(up)
		defer srv.Close()
		f10SaveRelay(t, f10RelayWith(srv, provider.KeyAccount{Name: "GPT", Key: "k-chat", Protocol: provider.Chat},
			provider.KeyAccount{Name: "Claude", Key: "k-ant", Protocol: provider.Anthropic}))
		code, body := f10Post(t, "/v1/messages/count_tokens", countBody)
		if code != 200 || !strings.Contains(body, "7") {
			t.Fatalf("status %d, body %s", code, body)
		}
		if strings.Join(up.calls, ",") != "/v1/messages/count_tokens k-ant" {
			t.Fatalf("upstream got %v", up.calls)
		}
	})
}

// Acceptance 1–4: one provider with a Chat and an Anthropic endpoint and two
// protocol-limited keys, in both configuration orders; streaming and not.
// The upstream must see exactly one request, with the key for its endpoint.
func TestF10Acceptance1to4KeyLimitedRouting(t *testing.T) {
	for _, order := range f10Orders() {
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
				up := &f10Routed{}
				srv := httptest.NewServer(up)
				defer srv.Close()
				f10SaveRelay(t, f10RelayWith(srv, order.first, order.rest))

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
				code, body := f10Post(t, tc.path, req)
				if code != 200 {
					t.Fatalf("status %d, body %s", code, body)
				}
				if !strings.Contains(body, tc.wantReply) {
					t.Fatalf("client reply is not in its own format (want %s): %s", tc.wantReply, body)
				}
				if len(up.calls) != 1 || up.calls[0] != tc.wantCall {
					t.Fatalf("upstream got %v, want exactly [%s]", up.calls, tc.wantCall)
				}
			})
		}
	}
}

// Acceptance 5: openrouter/openai/o3-mini is OpenAI family, deepseek-chat is
// unknown; an unknown model prefers the key that speaks the client's protocol,
// whatever the configuration order, and the name favours no key.
func TestF10Acceptance5UnknownModelPrefersClientProtocolKey(t *testing.T) {
	if got := modelFamily("openrouter/openai/o3-mini"); got != familyOpenAI {
		t.Fatalf("modelFamily(openrouter/openai/o3-mini) = %q, want openai", got)
	}
	if got := modelFamily("deepseek-chat"); got != familyUnknown {
		t.Fatalf("modelFamily(deepseek-chat) = %q, want unknown", got)
	}

	for _, order := range f10Orders() {
		p := provider.Provider{ID: "relay", Name: "Relay", Chat: "http://chat/v1", Anthropic: "http://ant",
			Key: order.first.Key, KeyName: order.first.Name, KeyProtocol: order.first.Protocol,
			Keys: []provider.KeyAccount{order.rest}}
		if got := strings.Join(f10KeysOf(perKey(provider.Chat, p, "deepseek-chat")), ","); got != "k-chat,k-ant" {
			t.Fatalf("%s: chat client, deepseek-chat: keys %s, want k-chat,k-ant", order.name, got)
		}
		if got := strings.Join(f10KeysOf(perKey(provider.Anthropic, p, "deepseek-chat")), ","); got != "k-ant,k-chat" {
			t.Fatalf("%s: anthropic client, deepseek-chat: keys %s, want k-ant,k-chat", order.name, got)
		}
		// an OpenAI-family name still ranks the OpenAI key first, from any client
		if got := strings.Join(f10KeysOf(perKey(provider.Anthropic, p, "openrouter/openai/o3-mini")), ","); got != "k-chat,k-ant" {
			t.Fatalf("%s: o3-mini from anthropic: keys %s, want k-chat,k-ant", order.name, got)
		}
	}

	// end to end: an unknown model from a chat client goes out on the chat
	// key only, whichever key is listed first
	for _, order := range f10Orders() {
		t.Run(order.name, func(t *testing.T) {
			up := &f10Routed{}
			srv := httptest.NewServer(up)
			defer srv.Close()
			f10SaveRelay(t, f10RelayWith(srv, order.first, order.rest))
			code, body := f10Post(t, "/v1/chat/completions", `{"model":"relay/deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || !strings.Contains(body, "chat.completion") {
				t.Fatalf("status %d, body %s", code, body)
			}
			if len(up.calls) != 1 || up.calls[0] != "/v1/chat/completions k-chat" {
				t.Fatalf("upstream got %v, want exactly [/v1/chat/completions k-chat]", up.calls)
			}
		})
	}
}

// Acceptance 9: a key limited to a protocol the provider lacks is not an
// error and is not used; the request is served by the other keys.
func TestF10Acceptance9LimitedKeyWithoutEndpointIsIgnored(t *testing.T) {
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
				up := &f10Routed{}
				srv := httptest.NewServer(up)
				defer srv.Close()
				// a Chat-only vendor: neither Anthropic nor Responses exists
				p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Models: []string{"claude-opus-4-8", "gpt-5.5"},
					Key: order.first.Key, KeyName: order.first.Name, KeyProtocol: order.first.Protocol,
					Keys: []provider.KeyAccount{order.rest}}
				f10SaveRelay(t, p)

				code, body := f10Post(t, "/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
				if code != 200 || !strings.Contains(body, `"type":"message"`) {
					t.Fatalf("anthropic client: status %d, body %s", code, body)
				}
				if len(up.calls) != 1 || up.calls[0] != "/v1/chat/completions k-gen" {
					t.Fatalf("anthropic client: upstream got %v, want exactly [/v1/chat/completions k-gen]", up.calls)
				}

				up.calls = nil
				code, body = f10Post(t, "/v1/chat/completions", `{"model":"relay/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
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

// Acceptance 10: with only OpenAI-limited keys usable, count_tokens never
// reaches the Anthropic endpoint and still returns an estimate.
func TestF10Acceptance10CountTokensWithOnlyOpenAIKeys(t *testing.T) {
	const countBody = `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`
	for _, tc := range []struct {
		name        string
		first, rest provider.KeyAccount
	}{
		{"chat-limited key", f10ChatKey, provider.KeyAccount{}},
		{"responses-limited key, then chat-limited", provider.KeyAccount{Name: "Resp", Key: "k-resp", Protocol: provider.Responses}, f10ChatKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &f10Routed{}
			srv := httptest.NewServer(up)
			defer srv.Close()
			p := f10RelayWith(srv, tc.first)
			if tc.rest.Key != "" {
				p = f10RelayWith(srv, tc.first, tc.rest)
			}
			f10SaveRelay(t, p)
			code, body := f10Post(t, "/v1/messages/count_tokens", countBody)
			if code != 200 || !strings.Contains(body, "input_tokens") {
				t.Fatalf("status %d, body %s", code, body)
			}
			if len(up.calls) != 0 {
				t.Fatalf("upstream got %v, want no request at all", up.calls)
			}
		})
	}
}
