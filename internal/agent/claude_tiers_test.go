package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// claudeHome sets up a home with two magpie providers and a Claude Code
// settings file holding an unrelated theme.
func claudeHome(t *testing.T) (settings string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"deepseek-pro", "deepseek-flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "acme", Name: "Acme", Chat: "https://api.acme.example/v1", Key: "k", Models: []string{"big"}}); err != nil {
		t.Fatal(err)
	}
	settings = filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"theme": "dark"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return settings
}

func claudeAgent(t *testing.T) *Agent {
	t.Helper()
	a, err := Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// claudeEnvOf reads one env var from the settings file ("" when absent).
func claudeEnvOf(t *testing.T, settings, k string) string {
	t.Helper()
	v, _ := edit.GetJSON(settings, "env."+k)
	return v
}

func expectTiers(t *testing.T, settings, main string, tiers map[string]string, subagent string) {
	t.Helper()
	if got := claudeEnvOf(t, settings, "ANTHROPIC_MODEL"); got != main {
		t.Fatalf("main model %q, want %q", got, main)
	}
	for tier, want := range tiers {
		env := map[string]string{"opus": "ANTHROPIC_DEFAULT_OPUS_MODEL", "sonnet": "ANTHROPIC_DEFAULT_SONNET_MODEL",
			"haiku": "ANTHROPIC_DEFAULT_HAIKU_MODEL", "fable": "ANTHROPIC_DEFAULT_FABLE_MODEL"}[tier]
		if got := claudeEnvOf(t, settings, env); got != want {
			t.Fatalf("%s tier env %s = %q, want %q", tier, env, got, want)
		}
	}
	if got := claudeEnvOf(t, settings, "ANTHROPIC_SMALL_FAST_MODEL"); got != tiers["haiku"] {
		t.Fatalf("small fast model %q, want haiku tier %q", got, tiers["haiku"])
	}
	if got := claudeEnvOf(t, settings, "CLAUDE_CODE_SUBAGENT_MODEL"); got != subagent {
		t.Fatalf("subagent model %q, want %q", got, subagent)
	}
}

func TestClaudeTiersFollowAndPin(t *testing.T) {
	settings := claudeHome(t)
	a := claudeAgent(t)
	model := a.Field("model")
	haiku := a.Field("haiku")

	if err := model.Set("deepseek/deepseek-pro"); err != nil {
		t.Fatal(err)
	}
	all := map[string]string{"opus": "deepseek/deepseek-pro", "sonnet": "deepseek/deepseek-pro",
		"haiku": "deepseek/deepseek-pro", "fable": "deepseek/deepseek-pro"}
	expectTiers(t, settings, "deepseek/deepseek-pro", all, "deepseek/deepseek-pro")
	if v := haiku.Get(); v != "" {
		t.Fatalf("following tier should read empty, got %q", v)
	}

	// pin haiku: only the small fast model moves with it, subagent unification ends
	if err := haiku.Set("deepseek/deepseek-flash"); err != nil {
		t.Fatal(err)
	}
	pinned := map[string]string{"opus": "deepseek/deepseek-pro", "sonnet": "deepseek/deepseek-pro",
		"haiku": "deepseek/deepseek-flash", "fable": "deepseek/deepseek-pro"}
	expectTiers(t, settings, "deepseek/deepseek-pro", pinned, "")
	if v := haiku.Get(); v != "deepseek/deepseek-flash" {
		t.Fatalf("haiku Get = %q", v)
	}

	// changing the main model moves the followers and keeps the pin
	if err := model.Set("acme/big"); err != nil {
		t.Fatal(err)
	}
	expectTiers(t, settings, "acme/big", map[string]string{"opus": "acme/big", "sonnet": "acme/big",
		"haiku": "deepseek/deepseek-flash", "fable": "acme/big"}, "")

	// clearing the pin returns haiku to following and restores unification
	if err := haiku.Set(""); err != nil {
		t.Fatal(err)
	}
	expectTiers(t, settings, "acme/big", map[string]string{"opus": "acme/big", "sonnet": "acme/big",
		"haiku": "acme/big", "fable": "acme/big"}, "acme/big")
	if v := haiku.Get(); v != "" {
		t.Fatalf("unpinned haiku Get = %q", v)
	}

	// restoring Claude Code's own setup clears every env var and keeps theme
	if err := model.Set(""); err != nil {
		t.Fatal(err)
	}
	for _, k := range claudeEnv {
		if v := claudeEnvOf(t, settings, k); v != "" {
			t.Fatalf("%s still set to %q", k, v)
		}
	}
	if v, _ := edit.GetJSON(settings, "theme"); v != "dark" {
		t.Fatalf("theme lost: %q", v)
	}
}

func TestClaudeTierErrors(t *testing.T) {
	settings := claudeHome(t)
	a := claudeAgent(t)
	haiku := a.Field("haiku")

	// clearing without a magpie main model is a no-op
	if err := haiku.Set(""); err != nil {
		t.Fatalf("clearing an unrouted tier: %v", err)
	}
	if v, _ := edit.GetJSON(settings, "env.ANTHROPIC_MODEL"); v != "" {
		t.Fatalf("clearing wrote %q", v)
	}
	// setting one needs a main model through magpie first
	if err := haiku.Set("deepseek/deepseek-flash"); err == nil || !strings.Contains(err.Error(), "main model") {
		t.Fatalf("want main-model error, got %v", err)
	}

	if err := a.Field("model").Set("deepseek/deepseek-pro"); err != nil {
		t.Fatal(err)
	}
	if err := haiku.Set("not-a-model"); err == nil {
		t.Fatal("non provider/model tier accepted")
	}
	if err := haiku.Set("claude-sonnet-5"); err == nil {
		t.Fatal("native model accepted for a tier")
	}
}

// A bare value on the command line goes to the first field that is not Extra
// (see fieldForValue in main.go), so the main model must come first and every
// tier must be Extra.
func TestClaudeBareModelIsMainField(t *testing.T) {
	claudeHome(t)
	a := claudeAgent(t)
	if a.Fields[0].Key != "model" || a.Fields[0].Extra {
		t.Fatalf("first field %q (extra=%v), want the main model", a.Fields[0].Key, a.Fields[0].Extra)
	}
	for _, f := range a.Fields[1:] {
		if !f.Extra {
			t.Fatalf("tier %s not marked Extra", f.Key)
		}
	}
}

func TestClaudeUnchangedWithoutTiers(t *testing.T) {
	settings := claudeHome(t)
	if err := claudeAgent(t).Field("model").Set("deepseek/deepseek-pro"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(settings, "env.ANTHROPIC_BASE_URL"); v != gateway.URL() {
		t.Fatalf("base url %q", v)
	}
	for _, k := range []string{"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"} {
		if v := claudeEnvOf(t, settings, k); v != "deepseek/deepseek-pro" {
			t.Fatalf("%s = %q", k, v)
		}
	}
}

func TestPiThinkingLevels(t *testing.T) {
	cases := []struct {
		in   []string
		want map[string]string
	}{
		{nil, nil},
		{[]string{"low", "medium", "high"}, nil},
		{[]string{"low", "high", "max"}, map[string]string{"max": "max"}},
		{[]string{"xhigh", "high"}, map[string]string{"xhigh": "xhigh"}},
		{[]string{"low", "xhigh", "max"}, map[string]string{"xhigh": "xhigh", "max": "max"}},
	}
	for _, c := range cases {
		if got := piThinkingLevels(c.in); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("piThinkingLevels(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
