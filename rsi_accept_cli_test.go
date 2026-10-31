package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func run(t *testing.T, bin, home, dir, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"), "PATH=" + home, "TERM=dumb"}
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAcceptCLIBackupRestore(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	bin := filepath.Join(t.TempDir(), "magpie")
	if out, err := exec.Command("go", "build", "-tags", "nogui", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	h1, w1 := t.TempDir(), t.TempDir()
	t.Setenv("HOME", h1)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h1, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h1, ".cache"))
	if err := provider.Save(provider.Provider{ID: "acme", Name: "Acme", Chat: "https://acme.example.com/v1", Key: "sk-acme"}); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, bin, h1, w1, "pw\n", "backup")
	if err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	file := filepath.Join(w1, "magpie.magpie-backup")
	st, err := os.Stat(file)
	if err != nil {
		t.Fatalf("no default file: %v", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("perm %v", st.Mode())
	}
	raw, _ := os.ReadFile(file)
	if strings.Contains(string(raw), "sk-acme") {
		t.Fatal("key in clear")
	}
	// empty passphrase: no file
	if _, err := run(t, bin, h1, w1, "\n", "backup", "empty.magpie-backup"); err == nil {
		t.Fatal("empty passphrase accepted")
	}
	if _, err := os.Stat(filepath.Join(w1, "empty.magpie-backup")); err == nil {
		t.Fatal("file written with empty passphrase")
	}
	// usage errors
	if _, err := run(t, bin, h1, w1, "pw\n", "backup", "--bogus"); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if _, err := run(t, bin, h1, w1, "pw\n", "backup", "a", "b"); err == nil {
		t.Fatal("extra arg accepted")
	}
	if _, err := run(t, bin, h1, w1, "pw\n", "restore"); err == nil {
		t.Fatal("restore without file accepted")
	}

	// other machine
	h2 := t.TempDir()
	if out, err := run(t, bin, h2, w1, "bad\n", "restore", file); err == nil {
		t.Fatalf("wrong passphrase accepted: %s", out)
	}
	if _, err := os.Stat(filepath.Join(h2, ".config")); err == nil {
		if out, _ := run(t, bin, h2, w1, "", "provider", "list"); strings.Contains(out, "acme") {
			t.Fatal("wrong passphrase changed config")
		}
	}
	if out, err := run(t, bin, h2, w1, "pw\n", "restore", "--no-agents", file); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	t.Setenv("HOME", h2)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h2, ".config"))
	p, err := provider.Find("acme")
	if err != nil || p.Key != "sk-acme" {
		t.Fatalf("restored: %+v %v", p, err)
	}
	// not a backup
	junk := filepath.Join(w1, "junk")
	os.WriteFile(junk, []byte("{}"), 0o600)
	out, err = run(t, bin, h2, w1, "pw\n", "restore", junk)
	if err == nil || strings.Contains(strings.ToLower(out), "passphrase") && !strings.Contains(strings.ToLower(out), "not a") {
		t.Fatalf("junk: %v %s", err, out)
	}
}
