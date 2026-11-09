package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// bcliHome points magpie's config and cache at a fresh temp home.
func bcliHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// bcliStdin makes the next passphrase read come from s, as a pipe would.
func bcliStdin(t *testing.T, s string) {
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
	stdin = nil
	t.Cleanup(func() {
		os.Stdin = old
		stdin = nil
		r.Close()
	})
}

func TestBcliParseBackupArgs(t *testing.T) {
	cases := []struct {
		args    []string
		noKeys  bool
		file    string
		wantErr bool
	}{
		{nil, false, "magpie.magpie-backup", false},
		{[]string{"--no-keys"}, true, "magpie.magpie-backup", false},
		{[]string{"b.magpie-backup"}, false, "b.magpie-backup", false},
		{[]string{"--no-keys", "x.magpie-backup"}, true, "x.magpie-backup", false},
		{[]string{"--bogus"}, false, "", true},
		{[]string{"-"}, false, "", true},
		{[]string{"a", "b"}, false, "", true},
	}
	for _, c := range cases {
		o, err := parseBackupArgs(c.args)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseBackupArgs(%q): want error", c.args)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseBackupArgs(%q): %v", c.args, err)
			continue
		}
		if o.noKeys != c.noKeys || o.file != c.file {
			t.Errorf("parseBackupArgs(%q) = %+v", c.args, o)
		}
	}
}

func TestBcliParseRestoreArgs(t *testing.T) {
	cases := []struct {
		args     []string
		noAgents bool
		file     string
		wantErr  bool
	}{
		{nil, false, "", true},
		{[]string{"--no-agents"}, false, "", true},
		{[]string{"b.magpie-backup"}, false, "b.magpie-backup", false},
		{[]string{"--no-agents", "b.magpie-backup"}, true, "b.magpie-backup", false},
		{[]string{"b.magpie-backup", "--no-agents"}, true, "b.magpie-backup", false},
		{[]string{"--bogus", "b"}, false, "", true},
		{[]string{"a", "b"}, false, "", true},
	}
	for _, c := range cases {
		o, err := parseRestoreArgs(c.args)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseRestoreArgs(%q): want error", c.args)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseRestoreArgs(%q): %v", c.args, err)
			continue
		}
		if o.noAgents != c.noAgents || o.file != c.file {
			t.Errorf("parseRestoreArgs(%q) = %+v", c.args, o)
		}
	}
}

func TestBcliBackupRestoreRoundTrip(t *testing.T) {
	bcliHome(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "b.magpie-backup")

	// an empty passphrase writes nothing
	bcliStdin(t, "\n")
	if err := backupCmd([]string{"backup", file}); err == nil {
		t.Fatal("backup with empty passphrase: want error")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("backup with empty passphrase created a file: %v", err)
	}

	bcliStdin(t, "pw-one\n")
	if err := backupCmd([]string{"backup", "--no-keys", file}); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Errorf("backup mode = %o, want 600", perm)
		}
	}

	// a wrong passphrase is reported as such
	bcliStdin(t, "wrong\n")
	err := restoreCmd([]string{"restore", file})
	if err == nil || !strings.Contains(err.Error(), "口令错误或文件已被改动") {
		t.Fatalf("restore with wrong passphrase: got %v", err)
	}

	bcliStdin(t, "pw-one\n")
	if err := restoreCmd([]string{"restore", "--no-agents", file}); err != nil {
		t.Fatalf("restore: %v", err)
	}
}

func TestBcliRestoreRejectsNonBackup(t *testing.T) {
	bcliHome(t)
	dir := t.TempDir()
	for name, content := range map[string]string{"empty": "", "obj": "{}", "text": "hello"} {
		file := filepath.Join(dir, name)
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		bcliStdin(t, "\n")
		err := restoreCmd([]string{"restore", file})
		if err == nil {
			t.Errorf("%s: restore want error", name)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "不是 magpie 备份") {
			t.Errorf("%s: got %q, want not-a-backup message", name, msg)
		}
		if strings.Contains(strings.ToLower(msg), "passphrase") || strings.Contains(msg, "口令") {
			t.Errorf("%s: message mentions passphrase: %q", name, msg)
		}
	}
}

func TestBcliNewerBackupNeedsUpgrade(t *testing.T) {
	bcliHome(t)
	file := filepath.Join(t.TempDir(), "new.magpie-backup")
	body := `{"format":"magpie-backup","version":99,"kdf":"pbkdf2-sha256","iterations":600000}` + "\nAAAA\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	bcliStdin(t, "pw\n")
	err := restoreCmd([]string{"restore", file})
	if err == nil || !strings.Contains(err.Error(), "升级") {
		t.Fatalf("restore of newer backup: got %v, want upgrade message", err)
	}
}
