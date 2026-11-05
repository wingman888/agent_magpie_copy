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
)

func TestParseBackupArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    backupArgs
		wantErr bool
	}{
		{name: "defaults", args: nil, want: backupArgs{file: "magpie.magpie-backup"}},
		{name: "no keys", args: []string{"--no-keys"}, want: backupArgs{noKeys: true, file: "magpie.magpie-backup"}},
		{name: "file", args: []string{"/tmp/x.magpie-backup"}, want: backupArgs{file: "/tmp/x.magpie-backup"}},
		{name: "flag after file", args: []string{"b.magpie-backup", "--no-keys"}, want: backupArgs{noKeys: true, file: "b.magpie-backup"}},
		{name: "help", args: []string{"--help"}, want: backupArgs{help: true}},
		{name: "unknown flag", args: []string{"--bogus"}, wantErr: true},
		{name: "single dash", args: []string{"-"}, wantErr: true},
		{name: "extra positional", args: []string{"a", "b"}, wantErr: true},
		{name: "empty file name", args: []string{""}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseBackupArgs(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseBackupArgs(%q) = %+v, want usage error", c.args, got)
				}
				if !strings.Contains(err.Error(), "用法") && !strings.Contains(err.Error(), "参数") && !strings.Contains(err.Error(), "文件名") {
					t.Fatalf("error %q does not explain the usage", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBackupArgs(%q): %v", c.args, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("parseBackupArgs(%q) = %+v, want %+v", c.args, got, c.want)
			}
		})
	}
}

func TestParseRestoreArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    restoreArgs
		wantErr bool
	}{
		{name: "no file", args: nil, wantErr: true},
		{name: "file", args: []string{"b.magpie-backup"}, want: restoreArgs{file: "b.magpie-backup"}},
		{name: "no agents", args: []string{"--no-agents", "b"}, want: restoreArgs{noAgents: true, file: "b"}},
		{name: "unknown flag", args: []string{"--force", "b"}, wantErr: true},
		{name: "two files", args: []string{"a", "b"}, wantErr: true},
		{name: "help", args: []string{"-h"}, want: restoreArgs{help: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseRestoreArgs(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseRestoreArgs(%q) = %+v, want usage error", c.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRestoreArgs(%q): %v", c.args, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("parseRestoreArgs(%q) = %+v, want %+v", c.args, got, c.want)
			}
		})
	}
}

// pipeWith returns the read end of a pipe holding s, which is not a terminal.
func pipeWith(t *testing.T, s string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(s); err != nil {
		t.Fatal(err)
	}
	w.Close()
	t.Cleanup(func() { r.Close() })
	return r
}

func TestPassphraseFromPipeTakesFirstLine(t *testing.T) {
	got, err := passphrase(pipeWith(t, "s3cret\r\nignored\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cret" {
		t.Fatalf("passphrase = %q, want the first line without its line ending", got)
	}
}

func TestPassphraseFromPipeRejectsEmpty(t *testing.T) {
	for _, in := range []string{"", "\n", "\r\nabc\n"} {
		if _, err := passphrase(pipeWith(t, in), false); err == nil {
			t.Fatalf("passphrase(%q) accepted an empty line", in)
		}
	}
}

func TestWriteSecretFileOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not meaningful on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "magpie.magpie-backup")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSecretFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 0600", mode)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Fatalf("contents = %q", data)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %d entries", len(entries))
	}
}

// A wrong passphrase, or a file that is not a backup, must fail before
// Restore runs, so the machine's configuration is never touched.
func TestRestoreDataFailsBeforeRestore(t *testing.T) {
	sealed, err := backup.Seal(backup.Bundle{Version: backup.FormatVersion}, "right")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		data []byte
		pass string
		want string
		is   error
	}{
		{name: "wrong passphrase", data: sealed, pass: "wrong", want: "口令错误或文件已被改动", is: backup.ErrPassphrase},
		{name: "empty file", data: nil, pass: "x", want: "不是 magpie 备份文件", is: backup.ErrNotBackup},
		{name: "plain text", data: []byte("hello"), pass: "x", want: "不是 magpie 备份文件", is: backup.ErrNotBackup},
		{name: "unrelated json", data: []byte("{}"), pass: "x", want: "不是 magpie 备份文件", is: backup.ErrNotBackup},
		{name: "newer version", data: []byte(`{"format":"magpie-backup","version":99}` + "\nAAAA\n"), pass: "x", want: "升级 magpie", is: backup.ErrNewer},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := restoreData(c.data, c.pass, backup.All)
			if err == nil {
				t.Fatal("restoreData succeeded")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %q, want it to contain %q", err, c.want)
			}
			if strings.Contains(strings.ToLower(err.Error()), "passphrase") {
				t.Fatalf("error %q mentions passphrase", err)
			}
			if c.is != nil && errors.Is(err, backup.ErrPassphrase) != errors.Is(c.is, backup.ErrPassphrase) {
				t.Fatalf("error %q misclassified as passphrase error", err)
			}
			if !reflect.DeepEqual(res, backup.Result{}) {
				t.Fatalf("result = %+v, want zero: nothing should be restored", res)
			}
		})
	}
}
