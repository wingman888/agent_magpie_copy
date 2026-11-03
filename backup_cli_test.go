package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBackupArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		noKeys  bool
		file    string
		wantErr bool
	}{
		{name: "defaults", args: nil, file: "magpie.magpie-backup"},
		{name: "file", args: []string{"b.magpie-backup"}, file: "b.magpie-backup"},
		{name: "no keys then file", args: []string{"--no-keys", "~/b.magpie-backup"}, noKeys: true, file: "~/b.magpie-backup"},
		{name: "file then no keys", args: []string{"x", "--no-keys"}, noKeys: true, file: "x"},
		{name: "unknown flag", args: []string{"--force"}, wantErr: true},
		{name: "short flag", args: []string{"-k"}, wantErr: true},
		{name: "too many positionals", args: []string{"a", "b"}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noKeys, file, err := parseBackupArgs(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want usage error, got noKeys=%v file=%q", noKeys, file)
				}
				if !strings.Contains(err.Error(), "usage") {
					t.Fatalf("error should show usage: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if noKeys != c.noKeys || file != c.file {
				t.Fatalf("got noKeys=%v file=%q, want noKeys=%v file=%q", noKeys, file, c.noKeys, c.file)
			}
		})
	}
}

func TestParseRestoreArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		noAgents bool
		file     string
		wantErr  bool
	}{
		{name: "file", args: []string{"b.magpie-backup"}, file: "b.magpie-backup"},
		{name: "no agents", args: []string{"--no-agents", "b"}, noAgents: true, file: "b"},
		{name: "missing file", args: nil, wantErr: true},
		{name: "only flag", args: []string{"--no-agents"}, wantErr: true},
		{name: "unknown flag", args: []string{"--all", "b"}, wantErr: true},
		{name: "too many positionals", args: []string{"a", "b"}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noAgents, file, err := parseRestoreArgs(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want usage error, got noAgents=%v file=%q", noAgents, file)
				}
				if !strings.Contains(err.Error(), "usage") && !strings.Contains(err.Error(), "missing") {
					t.Fatalf("error should explain the usage: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if noAgents != c.noAgents || file != c.file {
				t.Fatalf("got noAgents=%v file=%q, want noAgents=%v file=%q", noAgents, file, c.noAgents, c.file)
			}
		})
	}
}

func noSecret(t *testing.T) func(string) (string, error) {
	t.Helper()
	return func(string) (string, error) {
		t.Fatal("terminal prompt must not be used here")
		return "", nil
	}
}

func TestReadPassphraseFromPipeTakesFirstLine(t *testing.T) {
	in := strings.NewReader("s3cret\r\nignored\n")
	got, err := readPassphrase(noSecret(t), false, in, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cret" {
		t.Fatalf("got %q, want first line without line ending", got)
	}
}

func TestReadPassphraseFromPipeWithoutNewline(t *testing.T) {
	got, err := readPassphrase(noSecret(t), false, strings.NewReader("pw"), false)
	if err != nil || got != "pw" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestReadPassphraseEmptyPipeIsError(t *testing.T) {
	for _, in := range []string{"", "\n", "\r\n"} {
		if _, err := readPassphrase(noSecret(t), false, strings.NewReader(in), true); !errors.Is(err, errEmptyPassphrase) {
			t.Fatalf("input %q: want empty passphrase error, got %v", in, err)
		}
	}
}

func TestReadPassphraseTerminalConfirmsMatch(t *testing.T) {
	answers := []string{"abc", "abc"}
	prompts := 0
	secret := func(string) (string, error) {
		prompts++
		a := answers[0]
		answers = answers[1:]
		return a, nil
	}
	got, err := readPassphrase(secret, true, nil, true)
	if err != nil || got != "abc" || prompts != 2 {
		t.Fatalf("got %q, %v after %d prompts", got, err, prompts)
	}
}

func TestReadPassphraseTerminalMismatchIsError(t *testing.T) {
	answers := []string{"abc", "abd"}
	secret := func(string) (string, error) {
		a := answers[0]
		answers = answers[1:]
		return a, nil
	}
	if _, err := readPassphrase(secret, true, nil, true); err == nil || strings.Contains(err.Error(), "abc") {
		t.Fatalf("want mismatch error without echoing the passphrase, got %v", err)
	}
}

func TestReadPassphraseTerminalEmptyIsError(t *testing.T) {
	prompts := 0
	secret := func(string) (string, error) {
		prompts++
		return "", nil
	}
	if _, err := readPassphrase(secret, true, nil, true); !errors.Is(err, errEmptyPassphrase) {
		t.Fatalf("want empty passphrase error, got %v", err)
	}
	if prompts != 1 {
		t.Fatalf("an empty passphrase should not be asked twice, got %d prompts", prompts)
	}
}

func TestReadPassphraseTerminalNoConfirmForRestore(t *testing.T) {
	prompts := 0
	secret := func(string) (string, error) {
		prompts++
		return "pw", nil
	}
	got, err := readPassphrase(secret, true, nil, false)
	if err != nil || got != "pw" || prompts != 1 {
		t.Fatalf("got %q, %v after %d prompts", got, err, prompts)
	}
}

func TestWriteBackupFileIsOwnerOnlyAndReplacesPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "magpie.magpie-backup")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeBackupFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", st.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Fatalf("content %q", data)
	}
}
