package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseBackupArgs(t *testing.T) {
	noKeys, file, err := parseBackupArgs(nil)
	if err != nil || noKeys || file != "magpie.magpie-backup" {
		t.Fatalf("defaults: got noKeys=%v file=%q err=%v", noKeys, file, err)
	}
	noKeys, file, err = parseBackupArgs([]string{"--no-keys", "b.magpie-backup"})
	if err != nil || !noKeys || file != "b.magpie-backup" {
		t.Fatalf("--no-keys file: got noKeys=%v file=%q err=%v", noKeys, file, err)
	}
	if _, _, err := parseBackupArgs([]string{"--bogus"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("unknown option: want usage error, got %v", err)
	}
	if _, _, err := parseBackupArgs([]string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("extra positional: want usage error, got %v", err)
	}
}

func TestParseRestoreArgs(t *testing.T) {
	noAgents, file, err := parseRestoreArgs([]string{"--no-agents", "b.magpie-backup"})
	if err != nil || !noAgents || file != "b.magpie-backup" {
		t.Fatalf("got noAgents=%v file=%q err=%v", noAgents, file, err)
	}
	for _, args := range [][]string{nil, {"--no-agents"}, {"a", "b"}, {"--x", "a"}} {
		if _, _, err := parseRestoreArgs(args); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Fatalf("%v: want usage error, got %v", args, err)
		}
	}
}

func TestReadPassphraseFromPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := w.WriteString("pw\r\nsecond line\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	pass, err := readPassphrase(r, "Passphrase: ", true)
	if err != nil || pass != "pw" {
		t.Fatalf("got %q, %v; want first line only", pass, err)
	}
}

func TestReadPassphraseEmptyPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	w.Close()
	pass, err := readPassphrase(r, "Passphrase: ", true)
	if err != nil || pass != "" {
		t.Fatalf("got %q, %v; want empty passphrase and no error", pass, err)
	}
}

func TestWriteBackupPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not meaningful on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "magpie.magpie-backup")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeBackup(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Fatalf("content = %q", data)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %d entries", len(entries))
	}
}

// swapStdin makes f the process stdin for the duration of the test.
func swapStdin(t *testing.T, f *os.File) {
	t.Helper()
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = old })
}

// emptyPipe is a stdin with nothing in it and no terminal behind it.
func emptyPipe(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	w.Close()
	return r
}

// TestRestorePreflightRejectsNonBackupWithoutPassphrase: a file that is not a
// backup (or is from a newer magpie) must fail with its own message, never
// with the passphrase message, and never after asking for a passphrase.
func TestRestorePreflightRejectsNonBackupWithoutPassphrase(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"empty.magpie-backup": "",
		"obj.magpie-backup":   "{}",
		"newer.magpie-backup": `{"format": "magpie-backup", "version": 99, "kdf": "x"}` + "\nAAAA\n",
	}
	swapStdin(t, emptyPipe(t))
	for name, content := range cases {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		err := restoreCmd([]string{"restore", path})
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		if strings.Contains(err.Error(), "口令") || strings.Contains(strings.ToLower(err.Error()), "passphrase") {
			t.Errorf("%s: got passphrase message: %q", name, err)
		}
	}
}

func TestBackupEmptyPassphraseWritesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	out := filepath.Join(dir, "b.magpie-backup")
	swapStdin(t, emptyPipe(t))
	err := backupCmd([]string{"backup", out})
	if err == nil {
		t.Fatal("empty passphrase accepted")
	}
	if strings.Contains(strings.ToLower(err.Error()), "passphrase") {
		t.Errorf("message mentions passphrase: %q", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Errorf("backup file written despite empty passphrase: %v", statErr)
	}
}
