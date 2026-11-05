package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestModelFamily(t *testing.T) {
	for model, want := range map[string]family{
		"claude-opus-4-8":           familyAnthropic,
		"relay/claude-opus-4-8":     familyAnthropic,
		"relay/gpt-5.5":             familyOpenAI,
		"gpt-5-codex":               familyOpenAI,
		"relay/gpt-5-codex-mini":    familyOpenAI,
		"openrouter/openai/o3-mini": familyOpenAI,
		"o1":                        familyOpenAI,
		"deepseek-chat":             familyUnknown,
		"openai-compatible-xxx":     familyUnknown,
		"Relay/Claude-Sonnet-4":     familyAnthropic,
		"":                          familyUnknown,
	} {
		if got := modelFamily(model); got != want {
			t.Errorf("modelFamily(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestPerKeyForOrdersByFit(t *testing.T) {
	// the Chat key is first, but a Claude model wants the Anthropic key
	p := provider.Provider{
		ID: "relay", Key: "sk-chat", KeyProtocol: provider.Chat,
		Chat: "https://relay/v1", Anthropic: "https://relay",
		Keys: []provider.KeyAccount{
			{Key: "sk-plain"},
			{Key: "sk-ant", Protocol: provider.Anthropic},
		},
	}
	cands := (&Server{}).candidatesFor(p, "claude-opus-4-8", provider.Chat)
	if len(cands) != 3 {
		t.Fatalf("got %d candidates, want 3", len(cands))
	}
	if cands[0].p.Key != "sk-ant" || cands[0].p.Chat != "" || cands[0].p.Anthropic == "" {
		t.Errorf("first candidate = %+v, want the Anthropic key on the Anthropic endpoint only", cands[0].p)
	}
	// the general key keeps its place ahead of the other vendor's key
	if cands[1].p.Key != "sk-plain" || cands[2].p.Key != "sk-chat" {
		t.Errorf("order = %s, %s, %s; want sk-ant, sk-plain, sk-chat", cands[0].p.Key, cands[1].p.Key, cands[2].p.Key)
	}
	if cands[2].p.Anthropic != "" {
		t.Errorf("Chat key reached the Anthropic endpoint: %+v", cands[2].p)
	}

	// a GPT model wants the OpenAI key, whichever order the keys were saved in
	cands = (&Server{}).candidatesFor(p, "relay/gpt-5.5", provider.Anthropic)
	if cands[0].p.Key != "sk-chat" || cands[0].p.Anthropic != "" {
		t.Errorf("GPT first candidate = %+v, want the Chat key on the Chat endpoint", cands[0].p)
	}
	if cands[len(cands)-1].p.Key != "sk-ant" {
		t.Errorf("Anthropic key should trail for a GPT model, got last %q", cands[len(cands)-1].p.Key)
	}
}

func TestPerKeyForUnknownModelPrefersClientProtocol(t *testing.T) {
	p := provider.Provider{
		ID: "relay", Key: "sk-chat", KeyProtocol: provider.Chat,
		Chat: "https://relay/v1", Anthropic: "https://relay",
		Keys: []provider.KeyAccount{{Key: "sk-ant", Protocol: provider.Anthropic}},
	}
	cands := (&Server{}).candidatesFor(p, "deepseek-chat", provider.Anthropic)
	if cands[0].p.Key != "sk-ant" {
		t.Errorf("first = %q, want the key speaking the client's protocol", cands[0].p.Key)
	}
	cands = (&Server{}).candidatesFor(p, "deepseek-chat", provider.Chat)
	if cands[0].p.Key != "sk-chat" {
		t.Errorf("first = %q, want the Chat key for a Chat client", cands[0].p.Key)
	}
}

func TestPerKeyForIgnoresKeyWithoutEndpoint(t *testing.T) {
	p := provider.Provider{
		ID: "relay", Key: "sk-resp", KeyProtocol: provider.Responses,
		Chat: "https://relay/v1", Anthropic: "https://relay",
		Keys: []provider.KeyAccount{{Key: "sk-ant", Protocol: provider.Anthropic}},
	}
	cands := (&Server{}).candidatesFor(p, "claude-opus-4-8", provider.Anthropic)
	if len(cands) != 1 || cands[0].p.Key != "sk-ant" {
		t.Fatalf("candidates = %d, want only sk-ant", len(cands))
	}

	// every key ignored: the old default, all keys on the provider as it is
	only := provider.Provider{ID: "relay", Key: "sk-resp", KeyProtocol: provider.Responses, Chat: "https://relay/v1"}
	cands = (&Server{}).candidatesFor(only, "gpt-5.5", provider.Chat)
	if len(cands) != 1 || cands[0].p.Key != "sk-resp" || cands[0].p.Chat == "" {
		t.Errorf("default candidate = %+v, want the provider unchanged", cands)
	}
}

func TestCountProviderNeverSendsOpenAIKeyToAnthropic(t *testing.T) {
	onlyChat := provider.Provider{ID: "relay", Key: "sk-chat", KeyProtocol: provider.Chat,
		Chat: "https://relay/v1", Anthropic: "https://relay"}
	if _, ok := countProvider(onlyChat); ok {
		t.Error("OpenAI-only key was allowed to count tokens on the Anthropic endpoint")
	}

	mixed := provider.Provider{ID: "relay", Key: "sk-chat", KeyProtocol: provider.Chat,
		Chat: "https://relay/v1", Anthropic: "https://relay",
		Keys: []provider.KeyAccount{{Key: "sk-ant", Protocol: provider.Anthropic}}}
	q, ok := countProvider(mixed)
	if !ok || q.Key != "sk-ant" || q.Anthropic == "" {
		t.Errorf("countProvider = %q, %v; want the Anthropic key", q.Key, ok)
	}
}
