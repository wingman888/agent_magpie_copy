package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestAcceptProviderFallbackCLI(t *testing.T) {
	h, err := os.MkdirTemp("", "magpie-accept")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(h) })
	env := append(os.Environ(), "HOME="+h, "XDG_CONFIG_HOME="+filepath.Join(h, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(h, ".cache"), "NO_COLOR=1")
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	for _, p := range []provider.Provider{
		{ID: "main", Name: "Mainp", Key: "k", Chat: "https://m.example/v1", Models: []string{"m1"}},
		{ID: "a", Name: "Aaa", Key: "k", Chat: "https://a.example/v1", Models: []string{"b"}},
		{ID: "c", Name: "Ccc", Key: "k", Chat: "https://c.example/v1", Models: []string{"d"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(h, "bin-magpie")
	if out, err := exec.Command("go", "build", "-tags", "nogui", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Skipf("cannot build binary: %v\n%s", err, out)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		o := ansi.ReplaceAllString(string(out), "")
		return o, err
	}

	if out, err := run("provider", "fallback", "main"); err != nil || !strings.Contains(out, "Mainp") || !strings.Contains(out, "has no fallback") {
		t.Fatalf("FAIL %s %v", tl(out), err)
	}
	if out, err := run("provider", "fallback", "main", "a/b", "c/d"); err != nil || !strings.Contains(out, "falls back to") {
		t.Fatalf("FAIL %s %v", tl(out), err)
	}
	out, err := run("provider", "fallback", "main")
	if err != nil || !strings.Contains(out, "Mainp") || !strings.Contains(out, "falls back to") || !strings.Contains(out, "a/b → c/d") {
		t.Fatalf("FAIL %s %v", tl(out), err)
	}
	// the provider list shows the chain
	if out, err := run("providers"); err != nil || !strings.Contains(out, "a/b → c/d") {
		t.Fatalf("FAIL list %s %v", tl(out), err)
	}
	// unknown model: error, nothing saved
	out, err = run("provider", "fallback", "main", "a/b", "ghost/zzz")
	if err == nil || !strings.Contains(out, `magpie knows no model "ghost/zzz" (magpie models lists them)`) {
		t.Fatalf("FAIL %s %v", tl(out), err)
	}
	if out, _ := run("provider", "fallback", "main"); !strings.Contains(out, "a/b → c/d") {
		t.Fatalf("FAIL changed %s", tl(out))
	}
	// missing id
	out, err = run("provider", "fallback")
	if err == nil || !strings.Contains(out, "magpie provider fallback <id> [provider/model… | none]") {
		t.Fatalf("FAIL %s %v", tl(out), err)
	}
	// unknown provider
	if _, err := run("provider", "fallback", "nope-such"); err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
	// clear
	if _, err := run("provider", "fallback", "main", "none"); err != nil {
		t.Fatal(err)
	}
	if out, _ := run("provider", "fallback", "main"); !strings.Contains(out, "has no fallback") {
		t.Fatalf("FAIL %s", tl(out))
	}
}

func tl(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " | "), `"`, "~") }
