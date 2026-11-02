package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
)

func TestParseBackupArgs(t *testing.T) {
	for _, c := range []struct {
		args   []string
		noKeys bool
		file   string
		bad    bool
	}{
		{nil, false, "magpie.magpie-backup", false},
		{[]string{"--no-keys"}, true, "magpie.magpie-backup", false},
		{[]string{"--no-keys", "b.magpie-backup"}, true, "b.magpie-backup", false},
		{[]string{"x", "--no-keys"}, true, "x", false},
		{[]string{"--keys"}, false, "", true},
		{[]string{"-n"}, false, "", true},
		{[]string{"a", "b"}, false, "", true},
	} {
		nk, f, err := parseBackupArgs(c.args)
		if (err != nil) != c.bad || (!c.bad && (nk != c.noKeys || f != c.file)) {
			t.Errorf("%v: %v %q %v", c.args, nk, f, err)
		}
	}
}

func TestParseRestoreArgs(t *testing.T) {
	for _, c := range []struct {
		args     []string
		noAgents bool
		file     string
		bad      bool
	}{
		{nil, false, "", true},
		{[]string{"--no-agents"}, false, "", true},
		{[]string{"f"}, false, "f", false},
		{[]string{"--no-agents", "f"}, true, "f", false},
		{[]string{"f", "g"}, false, "", true},
		{[]string{"--force", "f"}, false, "", true},
	} {
		na, f, err := parseRestoreArgs(c.args)
		if (err != nil) != c.bad || (!c.bad && (na != c.noAgents || f != c.file)) {
			t.Errorf("%v: %v %q %v", c.args, na, f, err)
		}
	}
}

func TestFirstLine(t *testing.T) {
	for in, want := range map[string]string{"pw\r\nmore\n": "pw", "pw": "pw", "": "", "\n": "", "a b\n": "a b"} {
		if got := firstLine(strings.NewReader(in)); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func cliHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir())
	return home
}

// withStdin runs f with standard input reading s from a pipe.
func withStdin(t *testing.T, s string, f func() error) error {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = w.WriteString(s)
		w.Close()
	}()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; r.Close() }()
	return f()
}

func TestBackupRestoreCLI(t *testing.T) {
	cliHome(t)
	dir := t.TempDir()
	if err := provider.Save(provider.Provider{ID: "relay", Name: "My Relay", Chat: "https://relay.example/v1", Key: "sk-relay-1"}); err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("work", profile.Profile{"claude.model": "opus"}); err != nil {
		t.Fatal(err)
	}

	// empty passphrase: an error and no file
	file := filepath.Join(dir, "b"+backup.Ext)
	if err := withStdin(t, "\n", func() error { return run([]string{"backup", file}) }); err == nil {
		t.Fatal("empty passphrase accepted")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("file written with an empty passphrase")
	}
	if err := run([]string{"backup", "--bogus", file}); err == nil {
		t.Fatal("unknown option accepted")
	}

	if err := withStdin(t, "pw\n", func() error { return run([]string{"backup", file}) }); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	data, _ := os.ReadFile(file)
	for _, s := range []string{"sk-relay-1", "relay.example", "work"} {
		if strings.Contains(string(data), s) {
			t.Fatalf("%q in the file", s)
		}
	}

	// a new machine
	cliHome(t)
	err = withStdin(t, "nope\n", func() error { return run([]string{"restore", file}) })
	if !errors.Is(err, backup.ErrPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if len(provider.Stored()) != 0 {
		t.Fatal("a wrong passphrase changed providers")
	}
	if err := withStdin(t, "pw\n", func() error { return run([]string{"restore", "--no-agents", file}) }); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil || p.Key != "sk-relay-1" {
		t.Fatalf("relay %+v %v", p, err)
	}
	ps, _ := profile.Load()
	if _, ok := ps["work"]; !ok {
		t.Fatal("profile not restored")
	}
	if err := run([]string{"restore"}); err == nil {
		t.Fatal("restore without a file accepted")
	}
}

func TestRestoreNotBackup(t *testing.T) {
	cliHome(t)
	dir := t.TempDir()
	for name, content := range map[string]string{"empty": "", "obj": "{}", "hello": "hello"} {
		f := filepath.Join(dir, name)
		if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		// no passphrase given at all: the format is told first
		err := withStdin(t, "", func() error { return run([]string{"restore", f}) })
		if !errors.Is(err, backup.ErrNotBackup) || errors.Is(err, backup.ErrPassphrase) {
			t.Fatalf("%s: %v", name, err)
		}
		msg := "magpie: " + err.Error()
		if !strings.Contains(msg, "不是 magpie 备份") || strings.Contains(strings.ToLower(msg), "passphrase") {
			t.Fatalf("%s: %q", name, msg)
		}
	}
	f := filepath.Join(dir, "new")
	_ = os.WriteFile(f, []byte(`{"format": "magpie-backup", "version": 9, "iterations": 600000}`+"\nAAAA\n"), 0o600)
	err := withStdin(t, "pw\n", func() error { return run([]string{"restore", f}) })
	if !errors.Is(err, backup.ErrTooNew) || strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("too new: %v", err)
	}
}
