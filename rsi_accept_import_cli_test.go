package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcceptImportCLINoTTY(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "magpie-bin")
	if out, err := exec.Command("go", "build", "-tags", "nogui", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Skipf("build: %v\n%s", err, out)
	}
	home := filepath.Join(dir, "home")
	os.MkdirAll(home, 0o755)
	env := append(os.Environ(), "HOME="+home, "USERPROFILE="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "APPDATA="+home)
	run := func(args ...string) string {
		c := exec.Command(bin, args...)
		c.Env = env
		c.Stdin = strings.NewReader("")
		out, err := c.CombinedOutput()
		if err == nil {
			t.Fatalf("%v: expected failure, got\n%s", args, out)
		}
		return string(out)
	}
	if out := run("import", "magpie://import?preset=deepseek&key=sk-test"); !strings.Contains(out, "not a terminal: add -y to import without asking") {
		t.Fatalf("output: %s", out)
	}
	if out := run("magpie://import?preset=deepseek&key=sk-test"); !strings.Contains(out, "not a terminal: add -y to import without asking") {
		t.Fatalf("link arg output: %s", out)
	}
	if out := run("import"); !strings.Contains(out, "magpie import [-y] 'magpie://import?") {
		t.Fatalf("usage output: %s", out)
	}
	if out := run("import", "-y", "magpie://delete?id=x"); !strings.Contains(out, "is not something magpie knows") {
		t.Fatalf("bad link output: %s", out)
	}
	filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "sk-test") {
				t.Errorf("key saved in %s", p)
			}
		}
		return nil
	})
}
