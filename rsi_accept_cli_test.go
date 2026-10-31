package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

var bin string

func TestMain(m *testing.M) {
	d, _ := os.MkdirTemp("", "magpiebin")
	bin = filepath.Join(d, "magpie")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		println(string(out))
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(d)
	os.Exit(code)
}

func run(t *testing.T, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func setup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	for _, p := range []provider.Provider{
		{ID: "aa", Name: "AA", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}},
		{ID: "bb", Name: "BB", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m2"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
}

func fb(t *testing.T) []string {
	p, err := provider.Find("aa")
	if err != nil {
		t.Fatal(err)
	}
	return p.Fallback
}

func TestAcceptCLIFallbackSetShowClear(t *testing.T) {
	setup(t)
	out, err := run(t, "provider", "fallback", "aa", "bb/m2")
	if err != nil || !strings.Contains(out, "bb/m2") {
		t.Fatalf("%v %s", err, out)
	}
	if !slices.Equal(fb(t), []string{"bb/m2"}) {
		t.Fatalf("%v", fb(t))
	}
	out, err = run(t, "provider", "fallback", "aa")
	if err != nil || !strings.Contains(out, "bb/m2") {
		t.Fatalf("%v %s", err, out)
	}
	if !slices.Equal(fb(t), []string{"bb/m2"}) {
		t.Fatal("view changed config")
	}
	out, _ = run(t, "providers")
	if !strings.Contains(out, "bb/m2") {
		t.Fatalf("list: %s", out)
	}
	// unknown model: error, nothing saved
	out, err = run(t, "provider", "fallback", "aa", "nowhere/x")
	if err == nil || !strings.Contains(out, "nowhere/x") || !strings.Contains(out, "magpie models") {
		t.Fatalf("%v %s", err, out)
	}
	if !slices.Equal(fb(t), []string{"bb/m2"}) {
		t.Fatalf("changed: %v", fb(t))
	}
	// a mix with one bad entry saves nothing
	run(t, "provider", "fallback", "aa", "bb/m2", "nowhere/x")
	if !slices.Equal(fb(t), []string{"bb/m2"}) {
		t.Fatalf("changed: %v", fb(t))
	}
	if _, err = run(t, "provider", "fallback", "aa", "none"); err != nil {
		t.Fatal(err)
	}
	if len(fb(t)) != 0 {
		t.Fatalf("not cleared: %v", fb(t))
	}
	out, _ = run(t, "provider", "fallback", "aa")
	if !strings.Contains(out, "fallback") {
		t.Fatalf("%s", out)
	}
}

func TestAcceptCLIFallbackBadArgs(t *testing.T) {
	setup(t)
	if _, err := run(t, "provider", "fallback"); err == nil {
		t.Fatal("expected error without id")
	}
	if _, err := run(t, "provider", "fallback", "zz", "bb/m2"); err == nil {
		t.Fatal("expected error for unknown id")
	}
}

func TestAcceptCLIHelpMentionsFallback(t *testing.T) {
	setup(t)
	for _, a := range [][]string{{"help"}, {"--help"}, {"provider"}} {
		out, _ := run(t, a...)
		if strings.Contains(out, "fallback") {
			return
		}
	}
	t.Fatal("no help mentions fallback")
}
