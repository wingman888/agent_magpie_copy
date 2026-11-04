package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/backup"
)

func TestParseBackupArgs(t *testing.T) {
	o, err := parseBackupArgs(nil)
	if err != nil || o.noKeys || o.file != "magpie.magpie-backup" {
		t.Fatalf("defaults: got %+v, %v", o, err)
	}
	o, err = parseBackupArgs([]string{"--no-keys", "b.magpie-backup"})
	if err != nil || !o.noKeys || o.file != "b.magpie-backup" {
		t.Fatalf("--no-keys file: got %+v, %v", o, err)
	}
	o, err = parseBackupArgs([]string{"x.bak", "--no-keys"})
	if err != nil || !o.noKeys || o.file != "x.bak" {
		t.Fatalf("file then flag: got %+v, %v", o, err)
	}
	if _, err := parseBackupArgs([]string{"--bogus"}); err == nil || !strings.Contains(err.Error(), "未知参数") {
		t.Fatalf("unknown flag: want usage error, got %v", err)
	}
	if _, err := parseBackupArgs([]string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "多余的参数") {
		t.Fatalf("extra positional: want usage error, got %v", err)
	}
	if _, err := parseBackupArgs([]string{"-"}); err == nil {
		t.Fatalf("lone dash: want usage error")
	}
}

func TestParseRestoreArgs(t *testing.T) {
	o, err := parseRestoreArgs([]string{"--no-agents", "b.magpie-backup"})
	if err != nil || !o.noAgents || o.file != "b.magpie-backup" {
		t.Fatalf("ok case: got %+v, %v", o, err)
	}
	o, err = parseRestoreArgs([]string{"b.magpie-backup"})
	if err != nil || o.noAgents {
		t.Fatalf("plain: got %+v, %v", o, err)
	}
	if _, err := parseRestoreArgs(nil); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("missing file: want usage error, got %v", err)
	}
	if _, err := parseRestoreArgs([]string{"--no-agents"}); err == nil {
		t.Fatalf("only flag: want usage error")
	}
	if _, err := parseRestoreArgs([]string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "多余的参数") {
		t.Fatalf("extra positional: want usage error, got %v", err)
	}
	if _, err := parseRestoreArgs([]string{"--force", "a"}); err == nil || !strings.Contains(err.Error(), "未知参数") {
		t.Fatalf("unknown flag: want usage error, got %v", err)
	}
}

func TestReadPassphraseFromPipeFirstLine(t *testing.T) {
	cases := map[string]string{
		"pw\nignored\n": "pw",
		"pw\r\n":        "pw",
		"pw":            "pw",
		"  spaced  \n":  "  spaced  ",
	}
	for in, want := range cases {
		var out bytes.Buffer
		got, err := readPassphrase("口令: ", false, strings.NewReader(in), &out, true)
		if err != nil {
			t.Fatalf("input %q: unexpected error %v", in, err)
		}
		if got != want {
			t.Fatalf("input %q: got %q, want %q", in, got, want)
		}
		if out.Len() != 0 {
			t.Fatalf("input %q: pipe mode should not prompt, wrote %q", in, out.String())
		}
	}
}

func TestReadPassphraseEmptyIsError(t *testing.T) {
	for _, in := range []string{"", "\n", "\r\n"} {
		var out bytes.Buffer
		if _, err := readPassphrase("口令: ", false, strings.NewReader(in), &out, true); !errors.Is(err, errEmptyPassphrase) {
			t.Fatalf("input %q: want empty-passphrase error, got %v", in, err)
		}
	}
}

func TestCheckPassphrase(t *testing.T) {
	if err := checkPassphrase("", "", false); !errors.Is(err, errEmptyPassphrase) {
		t.Fatalf("empty: got %v", err)
	}
	if err := checkPassphrase("", "", true); !errors.Is(err, errEmptyPassphrase) {
		t.Fatalf("empty confirmed: got %v", err)
	}
	if err := checkPassphrase("abc", "abd", true); !errors.Is(err, errPassphraseMismatch) {
		t.Fatalf("mismatch: got %v", err)
	}
	if err := checkPassphrase("abc", "abd", false); err != nil {
		t.Fatalf("mismatch without confirm should pass: %v", err)
	}
	if err := checkPassphrase("abc", "abc", true); err != nil {
		t.Fatalf("match: got %v", err)
	}
}

func TestBackupOpenErrorMessages(t *testing.T) {
	// The messages of these errors are shown to the user as they are.
	cases := []struct {
		in   error
		want string
	}{
		{backup.ErrNotBackup, "不是 magpie 备份文件"},
		{backup.ErrNewerVersion, "请升级 magpie"},
	}
	for _, c := range cases {
		got := backupOpenError(c.in)
		if got == nil || !strings.Contains(got.Error(), c.want) {
			t.Fatalf("%v: got %v, want text containing %q", c.in, got, c.want)
		}
		if strings.Contains(strings.ToLower(got.Error()), "passphrase") {
			t.Fatalf("message mentions passphrase: %q", got)
		}
	}
	other := errors.New("boom")
	if got := backupOpenError(other); got != other {
		t.Fatalf("other error should pass through, got %v", got)
	}
}

func TestOpenFormatErrorsAreNotPassphraseErrors(t *testing.T) {
	// Each of these must be reported as a format problem, never as a
	// wrong passphrase, and their messages must not mention one.
	samples := map[string][]byte{
		"empty":  {},
		"text":   []byte("hello"),
		"object": []byte("{}"),
		"newer":  []byte(`{"format":"magpie-backup","version":99}`),
	}
	for name, data := range samples {
		_, err := backup.Open(data, "pw")
		if err == nil {
			t.Fatalf("%s: Open succeeded", name)
		}
		if errors.Is(err, backup.ErrPassphrase) {
			t.Fatalf("%s: got passphrase error %v", name, err)
		}
		if msg := backupOpenError(err).Error(); strings.Contains(strings.ToLower(msg), "passphrase") {
			t.Fatalf("%s: message mentions passphrase: %q", name, msg)
		}
	}
}

func TestCountHelpers(t *testing.T) {
	if n := countProfiles(json.RawMessage(`{"a":{},"b":{}}`)); n != 2 {
		t.Fatalf("profiles: got %d", n)
	}
	if n := countProfiles(nil); n != 0 {
		t.Fatalf("nil profiles: got %d", n)
	}
	if n := countAgentSettings(json.RawMessage(`{"claude":{"model":"x","effort":"high"},"codex":{"model":"y"}}`)); n != 3 {
		t.Fatalf("agent settings: got %d", n)
	}
	if n := countAgentSettings(nil); n != 0 {
		t.Fatalf("nil agents: got %d", n)
	}
}
