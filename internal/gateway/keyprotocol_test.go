package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func TestModelFamily(t *testing.T) {
	for model, want := range map[string]int{
		"claude-opus-4-8":             familyAnthropic,
		"Claude-3":                    familyAnthropic,
		"gpt-5.5":                     familyOpenAI,
		"gpt-5-codex":                 familyOpenAI,
		"o3-mini":                     familyOpenAI,
		"openrouter/openai/o3-mini":   familyOpenAI,
		"deepseek-chat":               familyUnknown,
		"o":                           familyUnknown,
		"o0":                          familyUnknown,
		"openrouter/anthropic/Claude": familyAnthropic,
	} {
		if got := modelFamily(model); got != want {
			t.Errorf("modelFamily(%q) = %d, want %d", model, got, want)
		}
	}
}

// relayUpstream is a relay with one key per vendor: it answers 401 when the
// Anthropic path gets a key that isn't the Anthropic one, or any other path
// gets the Anthropic one.
type relayUpstream struct {
	mu   sync.Mutex
	hits []string
}

const (
	relayChatKey = "sk-openai-key"
	relayAnthKey = "sk-anthropic-key"
)

func (u *relayUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.hits = append(u.hits, r.URL.Path+" "+r.Header.Get("x-api-key")+r.Header.Get("Authorization"))
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	anth := r.URL.Path == "/v1/messages"
	got := r.Header.Get("Authorization")
	if anth {
		got = r.Header.Get("x-api-key")
	}
	if anth != (got == relayAnthKey || got == "Bearer "+relayAnthKey) || (!anth && got != "Bearer "+relayChatKey) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"bad key"}}`))
		return
	}
	if anth {
		w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		return
	}
	w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
}

func relayProvider(t *testing.T, keys []provider.KeyAccount, chat, anth bool) *relayUpstream {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	up := &relayUpstream{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: "relay", Name: "Relay", Key: keys[0].Key, KeyName: keys[0].Name, KeyProtocol: keys[0].Protocol,
		Keys: keys[1:], Models: []string{"claude-opus-4-8", "gpt-5.5", "deepseek-chat"}}
	if chat {
		p.Chat = srv.URL + "/v1"
	}
	if anth {
		p.Anthropic = srv.URL
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return up
}

func TestKeyProtocolRouting(t *testing.T) {
	chatKey := provider.KeyAccount{Name: "openai", Key: relayChatKey, Protocol: provider.Chat}
	anthKey := provider.KeyAccount{Name: "anthropic", Key: relayAnthKey, Protocol: provider.Anthropic}
	orders := map[string][]provider.KeyAccount{
		"chat first": {chatKey, anthKey},
		"anth first": {anthKey, chatKey},
	}
	for name, keys := range orders {
		for _, tc := range []struct{ name, path, body, want string }{
			{"messages claude", "/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages " + relayAnthKey},
			{"chat claude", "/v1/chat/completions", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`, "/v1/messages " + relayAnthKey},
			{"messages gpt", "/v1/messages", `{"model":"relay/gpt-5.5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions Bearer " + relayChatKey},
			{"chat gpt", "/v1/chat/completions", `{"model":"relay/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions Bearer " + relayChatKey},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				up := relayProvider(t, keys, true, true)
				code, body := post(t, tc.path, tc.body)
				if code != 200 {
					t.Fatalf("%d %s (hits %v)", code, body, up.hits)
				}
				if len(up.hits) != 1 || up.hits[0] != tc.want {
					t.Fatalf("upstream saw %v, want one %q", up.hits, tc.want)
				}
			})
		}
	}
}

func TestOnlyKeyOfOtherProtocolIsNotACandidate(t *testing.T) {
	relayProvider(t, []provider.KeyAccount{{Key: "sk-a", Protocol: provider.Anthropic}}, true, false)
	p, model, ok := provider.Resolve("relay/gpt-5.5")
	if !ok {
		t.Fatal("no provider")
	}
	cs := New().candidates(p, model, provider.Chat)
	if len(cs) != 1 || cs[0].rest != "relay" || cs[0].p.Key != "sk-a" || cs[0].p.Chat == "" {
		t.Fatalf("candidates %+v", cs)
	}
}

func TestUnrestrictedKeysKeepOrder(t *testing.T) {
	relayProvider(t, []provider.KeyAccount{{Key: "k1"}, {Key: "k2"}}, true, true)
	p, model, _ := provider.Resolve("relay/claude-opus-4-8")
	cs := New().candidates(p, model, provider.Chat)
	if len(cs) != 2 || cs[0].p.Key != "k1" || cs[1].p.Key != "k2" || cs[0].p.Anthropic == "" || cs[0].rest != "relay#"+provider.KeyID("k1") {
		t.Fatalf("candidates %+v", cs)
	}
}

func TestCountTokensSkipsOpenAIOnlyKeys(t *testing.T) {
	up := relayProvider(t, []provider.KeyAccount{{Key: relayChatKey, Protocol: provider.Chat}}, true, true)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages/count_tokens",
		strings.NewReader(`{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hello there"}]}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "input_tokens") || len(up.hits) != 0 {
		t.Fatalf("%d %s hits %v", rec.Code, rec.Body.String(), up.hits)
	}
}

func TestCountTokensUsesAnthropicKey(t *testing.T) {
	up := relayProvider(t, []provider.KeyAccount{{Key: relayChatKey, Protocol: provider.Chat}, {Key: relayAnthKey, Protocol: provider.Anthropic}}, true, true)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages/count_tokens",
		strings.NewReader(`{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hello there"}]}`)))
	if len(up.hits) != 1 || !strings.HasSuffix(up.hits[0], relayAnthKey) {
		t.Fatalf("hits %v (%d %s)", up.hits, rec.Code, rec.Body.String())
	}
}
