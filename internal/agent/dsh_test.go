package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestDsh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".dsh", "config.yaml")
	a := dsh(home)
	f := a.Field("model")
	read := func() string { b, _ := os.ReadFile(path); return string(b) }

	// nothing there, nothing written
	if err := f.Set(""); err != nil || f.Get() != "" {
		t.Fatalf("empty: %v %q", err, f.Get())
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("reset wrote a file")
	}

	own := "# mine\n- id: tools\n  config:\n    disabled: []\n\n- id: llm-deepseek\n  config:\n    thinking: disabled\n    reasoningEffort: \"off\"\n"
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(own), 0o644)

	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	s := read()
	for _, want := range []string{"# mine", "- id: tools", "- id: llm-deepseek # magpie", `baseURL: "http://`, `apiKey: "magpie"`, `- id: "deepseek/flash"`, "- id: agent-loop # magpie", `model: "deepseek/pro"`, "cwd: !!js process.cwd()", "- id: api-gateway # magpie"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "thinking: disabled") || strings.Count(s, "id: llm-deepseek") != 1 {
		t.Fatalf("the user's entry should be replaced:\n%s", s)
	}
	if f.Get() != "magpie/deepseek/pro" {
		t.Fatalf("get: %q", f.Get())
	}

	// dsh's own model: the user's endpoint entry comes back
	if err := f.Set("deepseek-v4-flash"); err != nil {
		t.Fatal(err)
	}
	s = read()
	if f.Get() != "deepseek-v4-flash" || !strings.Contains(s, "thinking: disabled") || strings.Contains(s, "llm-deepseek # magpie") || strings.Contains(s, "api-gateway") {
		t.Fatalf("own model: %q\n%s", f.Get(), s)
	}

	// through magpie again, then back to dsh as it ships
	if err := f.Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != own {
		t.Fatalf("reset should leave the user's file as it was:\n%s", got)
	}

	if err := f.Set("magpie/nope/x"); err == nil {
		t.Fatal("unknown catalog model accepted")
	}

	// a file magpie cannot read as a list is left alone
	os.WriteFile(path, []byte("llm-deepseek:\n  x: 1\n"), 0o644)
	if err := f.Set("magpie/deepseek/pro"); err == nil {
		t.Fatal("a mapping was edited as a list")
	}
}

func TestDshSettingsEndpoint(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.yaml")
	os.WriteFile(p, []byte("ui:\n  theme: dark\nllm-deepseek:\n  thinking: enabled\n"), 0o644)
	if dshSettingsEndpoint(p) {
		t.Fatal("thinking alone is not an endpoint")
	}
	os.WriteFile(p, []byte("llm-deepseek:\n  baseURL: https://x\nui:\n  apiKey: y\n"), 0o644)
	if !dshSettingsEndpoint(p) {
		t.Fatal("baseURL missed")
	}
}
