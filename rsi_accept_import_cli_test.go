package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "magpie-bin")
	cmd := exec.Command("go", "build", "-tags", "nogui", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build: %v\n%s", err, out)
	}
	return bin
}

func run(t *testing.T, bin, cfg string, args ...string) (string, error) {
	t.Helper()
	c := exec.Command(bin, args...)
	c.Env = append(os.Environ(), "XDG_CONFIG_HOME="+cfg, "HOME="+cfg, "APPDATA="+cfg)
	c.Stdin = strings.NewReader("y\n") // not a terminal
	out, err := c.CombinedOutput()
	return string(out), err
}

func saved(cfg string) string {
	b, _ := os.ReadFile(filepath.Join(cfg, "magpie", "providers.json"))
	return string(b)
}

const link = "magpie://import?name=Loc&chat=http://127.0.0.1:1/v1&key=sk-zzz&models=a,b"

func TestAcceptCLINonInteractiveRefuses(t *testing.T) {
	bin := buildBin(t)
	cfg := t.TempDir()
	out, err := run(t, bin, cfg, "import", link)
	if err == nil {
		t.Fatalf("accepted without -y: %s", out)
	}
	if strings.Contains(saved(cfg), "sk-zzz") {
		t.Fatal("saved")
	}
}

func TestAcceptCLIYesAdds(t *testing.T) {
	bin := buildBin(t)
	cfg := t.TempDir()
	out, err := run(t, bin, cfg, "import", "-y", link)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	s := saved(cfg)
	if !strings.Contains(s, `"loc"`) || !strings.Contains(s, "sk-zzz") {
		t.Fatalf("not saved: %s\n%s", s, out)
	}
	if !strings.Contains(out, "127.0.0.1") {
		t.Errorf("summary lacks host: %s", out)
	}
}

func TestAcceptCLIRejectsBadLink(t *testing.T) {
	bin := buildBin(t)
	cfg := t.TempDir()
	for _, l := range []string{"magpie://import?name=X&chat=http://relay.example/v1&key=sk-bad", "magpie://delete?id=x", "https://usemagpie.ai/import?preset=deepseek"} {
		if out, err := run(t, bin, cfg, "import", "-y", l); err == nil {
			t.Errorf("%s accepted: %s", l, out)
		}
	}
	if s := saved(cfg); strings.Contains(s, "sk-bad") {
		t.Fatal("saved bad link")
	}
}

func TestAcceptCLIReplaceWarningAndKeyHint(t *testing.T) {
	bin := buildBin(t)
	cfg := t.TempDir()
	if out, err := run(t, bin, cfg, "import", "-y", link); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, bin, cfg, "import", "-y", "magpie://import?name=Loc&chat=https://other.example/v1&key=sk-new")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(strings.ToLower(out), "replace") {
		t.Errorf("no replace warning: %s", out)
	}
	s := saved(cfg)
	if !strings.Contains(s, "sk-new") || strings.Contains(s, "sk-zzz") {
		t.Errorf("not replaced: %s", s)
	}
	cfg2 := t.TempDir()
	out, _ = run(t, bin, cfg2, "import", "-y", "magpie://import?name=Nokey&chat=https://nokey.example/v1")
	if !strings.Contains(out, "provider key") {
		t.Errorf("no key hint: %s", out)
	}
}

func TestAcceptCLIHelpAndProtocolArg(t *testing.T) {
	bin := buildBin(t)
	cfg := t.TempDir()
	out, _ := run(t, bin, cfg, "help")
	if !strings.Contains(out, "import") {
		t.Errorf("help lacks import: %s", out)
	}
	// protocol invocation without a terminal must not save anything
	run(t, bin, cfg, "MAGPIE://import?name=Loc&chat=http://127.0.0.1:1/v1&key=sk-proto")
	if strings.Contains(saved(cfg), "sk-proto") {
		t.Error("saved without confirmation")
	}
}
