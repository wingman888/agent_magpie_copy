package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

// withStdin makes os.Stdin a pipe that holds s, for the duration of the test.
func withStdin(t *testing.T, s string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(s); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		r.Close()
	})
}

// isolate points every magpie directory at a temp dir.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	return dir
}

func TestBackupUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--bogus"},
		{"-x", "file"},
		{"a", "b"},
	} {
		if err := backupCmd(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("backupCmd(%q) = %v, want usage error", args, err)
		}
	}
}

func TestRestoreUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--bogus", "file"},
		{"a", "b"},
		{"--no-agents"},
	} {
		if err := restoreCmd(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("restoreCmd(%q) = %v, want usage error", args, err)
		}
	}
}

func TestPassSourceStdinLine(t *testing.T) {
	s := passSource{tty: false, in: bufio.NewReader(strings.NewReader("secret\r\nother\n"))}
	got, err := s.ask("")
	if err != nil || got != "secret" {
		t.Fatalf("ask = %q, %v; want secret", got, err)
	}
	got, err = s.ask("")
	if err != nil || got != "other" {
		t.Fatalf("second ask = %q, %v; want other", got, err)
	}
}

func TestPassphraseEmptyRejected(t *testing.T) {
	s := passSource{tty: false, in: bufio.NewReader(strings.NewReader("\n"))}
	if _, err := s.passphrase(true); err == nil {
		t.Fatal("empty passphrase accepted")
	}
	s = passSource{tty: false, in: bufio.NewReader(strings.NewReader(""))}
	if _, err := s.passphrase(false); err == nil {
		t.Fatal("missing passphrase accepted")
	}
}

func TestWriteBackupFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "magpie.magpie-backup")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeBackupFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Errorf("content = %q", data)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %d entries", len(entries))
	}
}

func TestProviderHasKey(t *testing.T) {
	if providerHasKey(provider.Provider{}) {
		t.Error("keyless provider reported a key")
	}
	if !providerHasKey(provider.Provider{Key: "k"}) {
		t.Error("main key not seen")
	}
	if !providerHasKey(provider.Provider{Keys: []provider.KeyAccount{{Key: "k"}}}) {
		t.Error("multi-key account not seen")
	}
}

func TestBackupRestoreRoundTripCLI(t *testing.T) {
	isolate(t)
	file := filepath.Join(t.TempDir(), "b"+backup.Ext)

	withStdin(t, "pw-test\n")
	if err := backupCmd([]string{"--no-keys", file}); err != nil {
		t.Fatalf("backup: %v", err)
	}
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("backup mode = %o, want 600", perm)
	}

	withStdin(t, "wrong\n")
	err = restoreCmd([]string{file})
	if err == nil || !strings.Contains(err.Error(), "口令错误或文件被改动") {
		t.Fatalf("wrong passphrase: err = %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "passphrase") {
		t.Errorf("error mentions passphrase: %v", err)
	}

	withStdin(t, "pw-test\n")
	if err := restoreCmd([]string{"--no-agents", file}); err != nil {
		t.Fatalf("restore: %v", err)
	}
}

func TestRestoreNotBackupNoPrompt(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	for name, content := range map[string]string{"empty": "", "obj": "{}", "text": "hello"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		withStdin(t, "")
		err := restoreCmd([]string{path})
		if err == nil || !strings.Contains(err.Error(), "不是 magpie 备份") {
			t.Errorf("%s: err = %v, want not-a-backup", name, err)
		}
	}
}
