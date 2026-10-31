package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestModelFamily(t *testing.T) {
	for m, want := range map[string]family{
		"claude-opus-4-8": familyAnthropic, "relay/claude-x": familyAnthropic,
		"gpt-5.5": familyOpenAI, "openrouter/openai/o3-mini": familyOpenAI, "o1": familyOpenAI,
		"gpt-5-codex": familyOpenAI, "deepseek-chat": familyUnknown, "omni": familyUnknown,
	} {
		if got := modelFamily(m); got != want {
			t.Errorf("%s: %v want %v", m, got, want)
		}
	}
}

func TestKeyCandidatesOrder(t *testing.T) {
	p := provider.Provider{ID: "r", Chat: "http://c", Anthropic: "http://a"}
	keys := []provider.KeyAccount{{Key: "k1", Protocol: provider.Chat}, {Key: "k2", Protocol: provider.Anthropic}, {Key: "k3", Protocol: provider.Responses}}
	cs := keyCandidates(p, "claude-x", provider.Chat, keys)
	if len(cs) != 2 || cs[0].p.Key != "k2" || cs[0].p.Chat != "" || cs[1].p.Anthropic != "" {
		t.Fatalf("%+v", cs)
	}
	cs = keyCandidates(p, "gpt-5", provider.Anthropic, keys)
	if cs[0].p.Key != "k1" || cs[0].p.Anthropic != "" {
		t.Fatalf("%+v", cs)
	}
	cs = keyCandidates(p, "deepseek-chat", provider.Anthropic, keys)
	if cs[0].p.Key != "k2" {
		t.Fatalf("%+v", cs)
	}
}
