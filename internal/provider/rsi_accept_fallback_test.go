package provider_test

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestAcceptFallbackNormalizedAndKept(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "k", Chat: "https://plan.example/v1",
		Fallback: []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("plan")
	if err != nil || !slices.Equal(p.Fallback, []string{"codex/gpt-5.5", "other/m"}) {
		t.Fatalf("%v %v", p, err)
	}
	for _, q := range provider.All() {
		if q.ID == "plan" && !slices.Equal(q.Fallback, []string{"codex/gpt-5.5", "other/m"}) {
			t.Fatalf("reload %v", q.Fallback)
		}
	}
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "k", Chat: "https://plan.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := provider.Find("plan"); len(p.Fallback) != 0 {
		t.Fatalf("not cleared: %v", p.Fallback)
	}
}
