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

func TestKeyRouteFamilyOf(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  family
	}{
		{"claude-opus-4-8", familyAnthropic},
		{"relay/claude-opus-4-8", familyAnthropic},
		{"gpt-5.5", familyOpenAI},
		{"openrouter/openai/o3-mini", familyOpenAI},
		{"o1", familyOpenAI},
		{"o9-preview", familyOpenAI},
		{"gpt-5-codex", familyOpenAI},
		{"deepseek-chat", familyUnknown},
		{"omni-moderation", familyUnknown},
		{"o0", familyUnknown},
	} {
		if got := familyOf(tc.model); got != tc.want {
			t.Errorf("familyOf(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

// krRelayProvider is a relay with a Chat and an Anthropic endpoint.
func krRelayProvider(base string, keys ...provider.KeyAccount) provider.Provider {
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: base + "/v1", Anthropic: base, Models: []string{"claude-opus-4-8", "gpt-5.5"}}
	if len(keys) > 0 {
		p.Key, p.KeyName, p.KeyProtocol = keys[0].Key, keys[0].Name, keys[0].Protocol
		p.Keys = keys[1:]
	}
	return p
}

func TestKeyRouteOrdersKeysByFit(t *testing.T) {
	p := krRelayProvider("https://relay.test",
		provider.KeyAccount{Name: "chat", Key: "sk-chat", Protocol: provider.Chat},
		provider.KeyAccount{Name: "any-1", Key: "sk-any-1"},
		provider.KeyAccount{Name: "anth", Key: "sk-anth", Protocol: provider.Anthropic},
		provider.KeyAccount{Name: "any-2", Key: "sk-any-2"})
	names := func(ks []provider.KeyAccount) string {
		var out []string
		for _, k := range ks {
			out = append(out, k.Name)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		name, model string
		from        provider.Protocol
		want        string
	}{
		// Claude: Anthropic key first, unrestricted keys next, OpenAI-class key last
		{"claude from anthropic", "claude-opus-4-8", provider.Anthropic, "anth,any-1,any-2,chat"},
		{"claude from chat", "claude-opus-4-8", provider.Chat, "anth,any-1,any-2,chat"},
		// GPT: OpenAI-class key first, Anthropic key last
		{"gpt from anthropic", "gpt-5.5", provider.Anthropic, "chat,any-1,any-2,anth"},
		{"gpt from chat", "o3-mini", provider.Chat, "chat,any-1,any-2,anth"},
		// unknown family: the key that speaks the client's protocol wins
		{"unknown from chat", "deepseek-chat", provider.Chat, "chat,any-1,any-2,anth"},
		{"unknown from anthropic", "deepseek-chat", provider.Anthropic, "anth,any-1,any-2,chat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := names(routeKeys(p, p.KeysOn(), tc.model, tc.from))
			if got != tc.want {
				t.Errorf("order = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestKeyRouteLeavesOutKeysWithoutEndpoint(t *testing.T) {
	// a chat-only relay: the Anthropic-limited key is not used at all
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Models: []string{"m1"},
		Key: "sk-chat", KeyName: "chat",
		Keys: []provider.KeyAccount{{Name: "anth", Key: "sk-anth", Protocol: provider.Anthropic}}}
	ks := routeKeys(p, p.KeysOn(), "claude-opus-4-8", provider.Chat)
	if len(ks) != 1 || ks[0].Key != "sk-chat" {
		t.Fatalf("usable keys: %+v", ks)
	}
	// every key limited to a missing endpoint: nothing is usable, so the
	// candidates fall back to the provider as it is
	only := provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Models: []string{"m1"},
		Key: "sk-anth", KeyProtocol: provider.Anthropic}
	if got := routeKeys(only, only.KeysOn(), "claude-opus-4-8", provider.Chat); len(got) != 0 {
		t.Fatalf("usable keys: %+v", got)
	}
	cands := perKey(only, "claude-opus-4-8", provider.Chat)
	if len(cands) != 1 || cands[0].p.Key != "sk-anth" || cands[0].p.Chat == "" {
		t.Fatalf("default candidates: %+v", cands)
	}
}

// krUpstream serves both endpoints of one relay and records every request
// with the key it carried. Replies follow the request's stream flag.
type krUpstream struct {
	mu   sync.Mutex
	hits []krHit
}

type krHit struct {
	path, key string
}

func (u *krUpstream) keys() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []string
	for _, h := range u.hits {
		out = append(out, h.path+"@"+h.key)
	}
	return out
}

func (u *krUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	u.mu.Lock()
	u.hits = append(u.hits, krHit{r.URL.Path, key})
	u.mu.Unlock()
	var req struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &req)
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/messages/count_tokens":
		io.WriteString(w, `{"input_tokens":42}`)
	case "/v1/messages":
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-8","usage":{"input_tokens":3}}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
				`data: {"type":"message_stop"}`))
			return
		}
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
	case "/v1/chat/completions":
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`data: {"id":"c1","model":"gpt-5.5","choices":[{"delta":{"role":"assistant","content":"hi"}}]}`,
				`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
				`data: [DONE]`))
			return
		}
		io.WriteString(w, `{"id":"c1","model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	default:
		http.NotFound(w, r)
	}
}

// krSetup saves the relay and resets the resting state.
func krSetup(t *testing.T, p provider.Provider) *krUpstream {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	up := &krUpstream{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p.Chat = srv.URL + "/v1"
	p.Anthropic = srv.URL
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return up
}

// krBoth are the two relay keys in both configuration orders.
func krBoth() map[string]provider.Provider {
	chatFirst := krRelayProvider("", provider.KeyAccount{Name: "chat", Key: "sk-chat", Protocol: provider.Chat},
		provider.KeyAccount{Name: "anth", Key: "sk-anth", Protocol: provider.Anthropic})
	anthFirst := krRelayProvider("", provider.KeyAccount{Name: "anth", Key: "sk-anth", Protocol: provider.Anthropic},
		provider.KeyAccount{Name: "chat", Key: "sk-chat", Protocol: provider.Chat})
	return map[string]provider.Provider{"chat-first": chatFirst, "anth-first": anthFirst}
}

func TestKeyRouteAnthropicClientClaudeUsesAnthropicKey(t *testing.T) {
	for name, p := range krBoth() {
		t.Run(name, func(t *testing.T) {
			up := krSetup(t, p)
			code, body := post(t, "/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || !strings.Contains(body, "hello") {
				t.Fatalf("%d %s", code, body)
			}
			if got := strings.Join(up.keys(), " "); got != "/v1/messages@sk-anth" {
				t.Fatalf("upstream saw %s", got)
			}
		})
	}
}

func TestKeyRouteChatClientClaudeTranslatesToAnthropicKey(t *testing.T) {
	for name, p := range krBoth() {
		t.Run(name, func(t *testing.T) {
			up := krSetup(t, p)
			code, body := post(t, "/v1/chat/completions", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			var out struct {
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(body), &out) != nil || len(out.Choices) == 0 || out.Choices[0].Message.Content != "hello" {
				t.Fatalf("reply not chat format: %s", body)
			}
			if got := strings.Join(up.keys(), " "); got != "/v1/messages@sk-anth" {
				t.Fatalf("upstream saw %s", got)
			}
		})
	}
}

func TestKeyRouteAnthropicClientGPTTranslatesToChatKey(t *testing.T) {
	for name, p := range krBoth() {
		t.Run(name, func(t *testing.T) {
			up := krSetup(t, p)
			code, body := post(t, "/v1/messages", `{"model":"relay/gpt-5.5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || !strings.Contains(body, `"hi"`) {
				t.Fatalf("%d %s", code, body)
			}
			if got := strings.Join(up.keys(), " "); got != "/v1/chat/completions@sk-chat" {
				t.Fatalf("upstream saw %s", got)
			}
		})
	}
}

func TestKeyRouteStreamingMatchesNonStreaming(t *testing.T) {
	for name, p := range krBoth() {
		t.Run(name, func(t *testing.T) {
			up := krSetup(t, p)
			// Claude from an Anthropic client: relayed as-is, streamed
			code, body := post(t, "/v1/messages", `{"model":"relay/claude-opus-4-8","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || !strings.Contains(body, "hello") {
				t.Fatalf("claude stream %d %s", code, body)
			}
			// GPT from an Anthropic client: translated, streamed
			code, body = post(t, "/v1/messages", `{"model":"relay/gpt-5.5","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || !strings.Contains(body, "message_start") || !strings.Contains(body, `"hi"`) {
				t.Fatalf("gpt stream %d %s", code, body)
			}
			want := "/v1/messages@sk-anth /v1/chat/completions@sk-chat"
			if got := strings.Join(up.keys(), " "); got != want {
				t.Fatalf("upstream saw %s, want %s", got, want)
			}
		})
	}
}

func TestKeyRouteIgnoresKeyForMissingEndpoint(t *testing.T) {
	// a chat-only relay with an Anthropic-limited key: it's skipped, the
	// request is served by the unrestricted key, and nothing errors
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	up := &krUpstream{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Models: []string{"m1"},
		Key: "sk-chat", KeyName: "chat",
		Keys: []provider.KeyAccount{{Name: "anth", Key: "sk-anth", Protocol: provider.Anthropic}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	for _, k := range up.keys() {
		if !strings.HasSuffix(k, "@sk-chat") {
			t.Fatalf("a key without an endpoint was used: %s", k)
		}
	}
	if len(up.keys()) != 1 {
		t.Fatalf("upstream saw %v", up.keys())
	}
}

func TestKeyRouteCountTokensNeverReachesAnthropicWithChatKey(t *testing.T) {
	// only an OpenAI-class key is usable: no request to the Anthropic endpoint
	p := provider.Provider{ID: "relay", Name: "Relay", Models: []string{"claude-opus-4-8"},
		Key: "sk-chat", KeyName: "chat", KeyProtocol: provider.Chat}
	up := krSetup(t, p)
	code, body := post(t, "/v1/messages/count_tokens", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hello there"}]}`)
	if code != 200 || !strings.Contains(body, "input_tokens") {
		t.Fatalf("%d %s", code, body)
	}
	if got := up.keys(); len(got) != 0 {
		t.Fatalf("upstream was asked: %v", got)
	}
}

func TestKeyRouteCountTokensUsesAnthropicKey(t *testing.T) {
	p := provider.Provider{ID: "relay", Name: "Relay", Models: []string{"claude-opus-4-8"},
		Key: "sk-chat", KeyName: "chat", KeyProtocol: provider.Chat,
		Keys: []provider.KeyAccount{{Name: "anth", Key: "sk-anth", Protocol: provider.Anthropic}}}
	up := krSetup(t, p)
	code, body := post(t, "/v1/messages/count_tokens", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "42") {
		t.Fatalf("%d %s", code, body)
	}
	if got := strings.Join(up.keys(), " "); got != "/v1/messages/count_tokens@sk-anth" {
		t.Fatalf("upstream saw %s", got)
	}
}
