package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Once the vendor's list declares endpoints for a model, the self-test probes
// only models declared on the endpoint; undeclared models are not a fallback.
func TestTestModelFollowsDeclaredEndpoints(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("decl", "https://decl.test", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat"}},
		{ID: "other"},
	}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "decl", Name: "Decl", Chat: "https://decl.test/v1",
		Responses: "https://decl.test/v1", Anthropic: "https://decl.test",
		Models: []string{"other", "claude-x", "gpt-x"}}
	if got := p.testModel(p, Responses); got != "gpt-x" {
		t.Errorf("responses probe model = %q, want gpt-x", got)
	}
	if got := p.testModel(p, Chat); got != "claude-x" {
		t.Errorf("chat probe model = %q, want claude-x", got)
	}
	if got := p.testModel(p, Anthropic); got != "" {
		t.Errorf("anthropic probe model = %q, want none (no model declares it)", got)
	}
}

// With no endpoint information at all, the older fallbacks still apply.
func TestTestModelFallsBackWhenListIsUnknown(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := Provider{ID: "nolist", Name: "NoList", Chat: "https://nolist.test/v1",
		Anthropic: "https://nolist.test", Models: []string{"m1", "claude-y"}}
	if got := p.testModel(p, Chat); got != "m1" {
		t.Errorf("chat probe model = %q, want m1", got)
	}
	if got := p.testModel(p, Anthropic); got != "claude-y" {
		t.Errorf("anthropic probe model = %q, want claude-y", got)
	}
}
