package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

func cliHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	return home
}

// pipe makes the passphrase come from stdin, as in `echo pw | magpie …`.
func pipe(t *testing.T, in string) {
	t.Helper()
	oldIn, oldTTY := stdin, stdinTTY
	stdin, stdinTTY = strings.NewReader(in), func() bool { return false }
	t.Cleanup(func() { stdin, stdinTTY = oldIn, oldTTY })
}

func TestBackupRestoreCLI(t *testing.T) {
	cliHome(t)
	dir := t.TempDir()
	t.Chdir(dir)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-cli-secret"}); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"--bogus"}, {"a", "b"}, {"-x", "a"}} {
		pipe(t, "pw\n")
		if err := backupCmd(args); err == nil {
			t.Errorf("backup %v: no usage error", args)
		}
	}
	pipe(t, "\n")
	if err := backupCmd(nil); err == nil {
		t.Error("empty passphrase taken")
	}
	pipe(t, "")
	if err := backupCmd(nil); err == nil {
		t.Error("no passphrase taken")
	}
	if _, err := os.Stat("magpie" + backup.Ext); err == nil {
		t.Fatal("a file was written without a passphrase")
	}

	pipe(t, "pw\r\n")
	if err := backupCmd(nil); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "magpie.magpie-backup"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	data, _ := os.ReadFile("magpie.magpie-backup")
	if strings.Contains(string(data), "sk-cli-secret") {
		t.Error("key readable")
	}

	for _, args := range [][]string{nil, {"--bogus", "f"}, {"a", "b"}} {
		pipe(t, "pw\n")
		if err := restoreCmd(args); err == nil {
			t.Errorf("restore %v: no usage error", args)
		}
	}

	// a fresh machine
	cliHome(t)
	pipe(t, "wrong\n")
	err = restoreCmd([]string{"magpie.magpie-backup"})
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("wrong passphrase: %v", err)
	}
	if len(provider.Stored()) != 0 {
		t.Error("a failed restore changed providers")
	}
	os.WriteFile("empty", nil, 0o600)
	os.WriteFile("obj", []byte("{}"), 0o600)
	for _, f := range []string{"empty", "obj"} {
		pipe(t, "pw\n")
		err := restoreCmd([]string{f})
		if err == nil || !strings.Contains(err.Error(), "不是 magpie 备份") || strings.Contains(err.Error(), "passphrase") {
			t.Errorf("%s: %v", f, err)
		}
	}
	pipe(t, "pw\n")
	if err := restoreCmd([]string{"--no-agents", "magpie.magpie-backup"}); err != nil {
		t.Fatal(err)
	}
	if p, err := provider.Find("relay"); err != nil || p.Key != "sk-cli-secret" {
		t.Errorf("restored %+v %v", p, err)
	}

	// --no-keys to a named file
	pipe(t, "pw\n")
	if err := backupCmd([]string{"--no-keys", "shared" + backup.Ext}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile("shared" + backup.Ext)
	b, err := backup.Open(data, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Providers) != 1 || b.Providers[0].Key != "" {
		t.Errorf("no-keys backup %+v", b.Providers)
	}
}
