package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, bin, home, stdin string, args ...string) (string, error) {
	t.Helper()
	c := exec.Command(bin, args...)
	c.Dir = home
	c.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home + "/.config", "XDG_CACHE_HOME=" + home + "/.cache", "PATH=" + home}
	c.Stdin = strings.NewReader(stdin)
	out, err := c.CombinedOutput()
	return string(out), err
}

func TestAcceptCLIBackupRestore(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "magpie")
	if out, err := exec.Command("go", "build", "-tags", "nogui", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Skipf("cannot build: %v %s", err, out)
	}
	home := t.TempDir()
	out, err := run(t, bin, home, "pw\n", "backup", "--no-keys", "out.magpie-backup")
	if err != nil {
		t.Fatalf("backup: %v %s", err, out)
	}
	if !strings.Contains(out, "saved") || !strings.Contains(out, "out.magpie-backup") {
		t.Fatalf("summary: %s", out)
	}
	fi, err := os.Stat(filepath.Join(home, "out.magpie-backup"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", fi, err)
	}
	if out, err := run(t, bin, home, "pw\n", "restore", "out.magpie-backup"); err != nil || !strings.Contains(out, "restored") {
		t.Fatalf("restore: %v %s", err, out)
	}
	out, err = run(t, bin, home, "bad\n", "restore", "out.magpie-backup")
	if err == nil || !strings.Contains(out, "wrong passphrase, or the file was changed") {
		t.Fatalf("wrong pass: %v %s", err, out)
	}
	if out, err := run(t, bin, home, "pw\n", "backup", "-x"); err == nil || !strings.Contains(out, "unknown flag") {
		t.Fatalf("flag: %v %s", err, out)
	}
	if out, err := run(t, bin, home, "", "restore"); err == nil || !strings.Contains(out, "usage: magpie restore [--no-agents] <file>") {
		t.Fatalf("usage: %v %s", err, out)
	}
	if out, err := run(t, bin, home, "pw\n", "backup", "a", "b"); err == nil || !strings.Contains(out, "usage: magpie backup [--no-keys] [file]") {
		t.Fatalf("usage: %v %s", err, out)
	}
}
