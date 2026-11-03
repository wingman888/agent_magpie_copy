package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/backup"
)

func TestParseBackupArgs(t *testing.T) {
	cases := []struct {
		args []string
		keys bool
		file string
		bad  bool
	}{
		{nil, true, "magpie" + backup.Ext, false},
		{[]string{"--no-keys"}, false, "magpie" + backup.Ext, false},
		{[]string{"b.magpie-backup"}, true, "b.magpie-backup", false},
		{[]string{"--no-keys", "x/b.magpie-backup"}, false, "x/b.magpie-backup", false},
		{[]string{"a", "b"}, false, "", true},
		{[]string{"--bogus"}, false, "", true},
		{[]string{"-x", "a"}, false, "", true},
		{[]string{""}, false, "", true},
	}
	for _, c := range cases {
		keys, file, err := parseBackupArgs(c.args)
		if c.bad {
			if !errors.Is(err, errBackupUsage) {
				t.Errorf("parseBackupArgs(%q): want usage error, got %v", c.args, err)
			}
			continue
		}
		if err != nil || keys != c.keys || file != c.file {
			t.Errorf("parseBackupArgs(%q) = %v, %q, %v; want %v, %q", c.args, keys, file, err, c.keys, c.file)
		}
	}
}

func TestParseRestoreArgs(t *testing.T) {
	cases := []struct {
		args   []string
		agents bool
		file   string
		bad    bool
	}{
		{[]string{"b.magpie-backup"}, true, "b.magpie-backup", false},
		{[]string{"--no-agents", "b"}, false, "b", false},
		{[]string{"b", "--no-agents"}, false, "b", false},
		{nil, false, "", true},
		{[]string{"a", "b"}, false, "", true},
		{[]string{"--bogus", "a"}, false, "", true},
		{[]string{""}, false, "", true},
	}
	for _, c := range cases {
		agents, file, err := parseRestoreArgs(c.args)
		if c.bad {
			if !errors.Is(err, errRestoreUsage) {
				t.Errorf("parseRestoreArgs(%q): want usage error, got %v", c.args, err)
			}
			continue
		}
		if err != nil || agents != c.agents || file != c.file {
			t.Errorf("parseRestoreArgs(%q) = %v, %q, %v; want %v, %q", c.args, agents, file, err, c.agents, c.file)
		}
	}
}

func TestReadLineFirstLineOnly(t *testing.T) {
	cases := map[string]string{
		"pw\nsecond\n": "pw",
		"pw\r\n":       "pw",
		"no newline":   "no newline",
		"":             "",
		"\n":           "",
	}
	for in, want := range cases {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(in)); err != nil {
			t.Fatal(err)
		}
		w.Close()
		got, err := readLine(r)
		r.Close()
		if err != nil {
			t.Fatalf("readLine(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("readLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteBackupFileOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not kept on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "magpie.magpie-backup")
	// an existing file with loose permissions must end up owner-only too
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
		t.Errorf("mode = %o, want 600", st.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Errorf("content = %q, %v; want \"new\"", data, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func TestNeedKeyLinesWithoutProviders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	got := needKeyLines([]string{"Missing Name"})
	want := []string{"Missing Name"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("needKeyLines = %q, want %q", got, want)
	}
}
