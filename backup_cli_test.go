package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

// withStdin points os.Stdin at a pipe holding s for the rest of the test.
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

func TestParseBackupArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		noKeys  bool
		file    string
		wantErr bool
	}{
		{"default", nil, false, "magpie.magpie-backup", false},
		{"no keys", []string{"--no-keys"}, true, "magpie.magpie-backup", false},
		{"file", []string{"b.magpie-backup"}, false, "b.magpie-backup", false},
		{"flag after file", []string{"x.magpie-backup", "--no-keys"}, true, "x.magpie-backup", false},
		{"unknown flag", []string{"--bogus"}, false, "", true},
		{"unknown flag with file", []string{"--no-agents", "a"}, false, "", true},
		{"extra positional", []string{"a", "b"}, false, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noKeys, file, err := parseBackupArgs(c.args)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if c.wantErr {
				if !strings.Contains(err.Error(), "usage") {
					t.Fatalf("error should show usage: %v", err)
				}
				return
			}
			if noKeys != c.noKeys || file != c.file {
				t.Fatalf("got (%v, %q), want (%v, %q)", noKeys, file, c.noKeys, c.file)
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
		{"missing file", nil, false, "", true},
		{"file", []string{"b.magpie-backup"}, false, "b.magpie-backup", false},
		{"no agents", []string{"--no-agents", "b"}, true, "b", false},
		{"extra positional", []string{"a", "b"}, false, "", true},
		{"unknown flag", []string{"--no-keys", "b"}, false, "", true},
		{"only flag", []string{"--no-agents"}, false, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noAgents, file, err := parseRestoreArgs(c.args)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if c.wantErr {
				if !strings.Contains(err.Error(), "usage") {
					t.Fatalf("error should show usage: %v", err)
				}
				return
			}
			if noAgents != c.noAgents || file != c.file {
				t.Fatalf("got (%v, %q), want (%v, %q)", noAgents, file, c.noAgents, c.file)
			}
		})
	}
}

func TestReadFirstLine(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"pw\nrest\n", "pw", false},
		{"pw\r\n", "pw", false},
		{"pw", "pw", false},
		{"with space \n", "with space ", false},
		{"", "", true},
		{"\n", "", true},
		{"\r\n", "", true},
	}
	for _, c := range cases {
		got, err := readFirstLine(strings.NewReader(c.in))
		if (err != nil) != c.wantErr {
			t.Fatalf("%q: err = %v, wantErr %v", c.in, err, c.wantErr)
		}
		if err == nil && got != c.want {
			t.Fatalf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestReadPassphraseNonTerminalEmptyIsError(t *testing.T) {
	withStdin(t, "\n")
	if _, err := readPassphrase(os.Stdin, true); !errors.Is(err, errEmptyPassphrase) {
		t.Fatalf("err = %v, want empty passphrase error", err)
	}
}

func TestBackupEmptyPassphraseWritesNothing(t *testing.T) {
	withStdin(t, "\n")
	path := filepath.Join(t.TempDir(), "b.magpie-backup")
	if err := backupCmd([]string{"backup", path}); err == nil {
		t.Fatal("empty passphrase should fail")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("no file should be written, stat err = %v", err)
	}
}

func TestBackupBadUsageWritesNothing(t *testing.T) {
	withStdin(t, "pw\n")
	dir := t.TempDir()
	if err := backupCmd([]string{"backup", "--bogus", filepath.Join(dir, "b")}); err == nil {
		t.Fatal("unknown option should fail")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("usage error should leave the directory empty, found %d entries", len(entries))
	}
}

func TestWriteBackupFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.magpie-backup")
	if err := writeBackupFile(path, []byte("sealed")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 && runtime.GOOS != "windows" {
		t.Fatalf("mode = %o, want 0600", mode)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "sealed" {
		t.Fatalf("content = %q", data)
	}
}

func TestRestoreRejectsNonBackupWithoutPassphraseError(t *testing.T) {
	cases := map[string]string{
		"empty":  "",
		"text":   "hello",
		"object": "{}",
		"newer":  `{"format":"magpie-backup","version":99,"kdf":"pbkdf2-sha256","iterations":600000}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			withStdin(t, "pw\n")
			path := filepath.Join(t.TempDir(), "x.magpie-backup")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			err := restoreCmd([]string{"restore", path})
			if err == nil {
				t.Fatal("expected an error")
			}
			if errors.Is(err, backup.ErrPassphrase) || strings.Contains(strings.ToLower(err.Error()), "passphrase") {
				t.Fatalf("should not be a passphrase error: %v", err)
			}
			if name == "newer" && !strings.Contains(err.Error(), "升级") {
				t.Fatalf("newer backup should ask for an upgrade: %v", err)
			}
			if name != "newer" && !strings.Contains(err.Error(), "不是 magpie 备份") {
				t.Fatalf("should say it is not a backup: %v", err)
			}
		})
	}
}

func TestRestoreWrongPassphraseIsReported(t *testing.T) {
	b := backup.Bundle{Version: 1}
	data, err := backup.Seal(b, "right")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "x.magpie-backup")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	withStdin(t, "wrong\n")
	err = restoreCmd([]string{"restore", path})
	if err == nil || !strings.Contains(err.Error(), "口令错误或文件被改动") {
		t.Fatalf("err = %v, want wrong passphrase message", err)
	}
}

func TestRestoreEmptyPassphraseIsError(t *testing.T) {
	b := backup.Bundle{Version: 1}
	data, err := backup.Seal(b, "right")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "x.magpie-backup")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	withStdin(t, "\n")
	if err := restoreCmd([]string{"restore", path}); !errors.Is(err, errEmptyPassphrase) {
		t.Fatalf("err = %v, want empty passphrase error", err)
	}
}

func TestRestoreMissingFileIsError(t *testing.T) {
	withStdin(t, "pw\n")
	if err := restoreCmd([]string{"restore", filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("missing file should fail")
	}
}

func TestProviderIDsByName(t *testing.T) {
	b := backup.Bundle{}
	b.Providers = append(b.Providers,
		provider.Provider{ID: "deepseek"},
		provider.Provider{ID: "moonshot", Name: "Kimi"})
	got := providerIDsByName(b)
	want := map[string]string{"deepseek": "deepseek", "Kimi": "moonshot"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
