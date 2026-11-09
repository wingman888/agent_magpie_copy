package main

import (
	"bufio"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/backup"
)

func TestCLIBackup_ParseBackupArgs(t *testing.T) {
	cases := []struct {
		args    []string
		noKeys  bool
		file    string
		wantErr bool
	}{
		{nil, false, "magpie" + backup.Ext, false},
		{[]string{"--no-keys"}, true, "magpie" + backup.Ext, false},
		{[]string{"--no-keys", "x.magpie-backup"}, true, "x.magpie-backup", false},
		{[]string{"x.magpie-backup"}, false, "x.magpie-backup", false},
		{[]string{"--bogus"}, false, "", true},
		{[]string{"a", "b"}, false, "", true},
	}
	for _, c := range cases {
		noKeys, file, err := parseBackupArgs(c.args)
		if (err != nil) != c.wantErr {
			t.Fatalf("parseBackupArgs(%q) error = %v, want error %v", c.args, err, c.wantErr)
		}
		if err == nil && (noKeys != c.noKeys || file != c.file) {
			t.Fatalf("parseBackupArgs(%q) = %v, %q; want %v, %q", c.args, noKeys, file, c.noKeys, c.file)
		}
	}
}

func TestCLIBackup_ParseRestoreArgs(t *testing.T) {
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
		{[]string{"-x", "b.magpie-backup"}, false, "", true},
		{[]string{"a", "b"}, false, "", true},
	}
	for _, c := range cases {
		noAgents, file, err := parseRestoreArgs(c.args)
		if (err != nil) != c.wantErr {
			t.Fatalf("parseRestoreArgs(%q) error = %v, want error %v", c.args, err, c.wantErr)
		}
		if err == nil && (noAgents != c.noAgents || file != c.file) {
			t.Fatalf("parseRestoreArgs(%q) = %v, %q; want %v, %q", c.args, noAgents, file, c.noAgents, c.file)
		}
	}
}

func TestCLIBackup_PipedPassphraseFirstLine(t *testing.T) {
	in := bufio.NewReader(strings.NewReader("pw one\r\nsecond\n"))
	got, err := readPass(in, false, "")
	if err != nil || got != "pw one" {
		t.Fatalf("readPass = %q, %v; want %q, nil", got, err, "pw one")
	}
	got, err = readPass(in, false, "")
	if err != nil || got != "second" {
		t.Fatalf("second readPass = %q, %v; want %q, nil", got, err, "second")
	}
}

func TestCLIBackup_PipedEmptyPassphrase(t *testing.T) {
	got, err := readPass(bufio.NewReader(strings.NewReader("")), false, "")
	if err != nil || got != "" {
		t.Fatalf("readPass on empty input = %q, %v; want empty, nil", got, err)
	}
}

func TestCLIBackup_CheckHeaderKinds(t *testing.T) {
	notBackup := []string{"", "hello", "{}"}
	for _, s := range notBackup {
		err := checkBackupHeader([]byte(s))
		if err == nil || !strings.Contains(err.Error(), "不是 magpie 备份") {
			t.Fatalf("checkBackupHeader(%q) = %v; want not-a-backup error", s, err)
		}
	}
	newer := `{"format":"magpie-backup","version":2}`
	err := checkBackupHeader([]byte(newer))
	if err == nil || !strings.Contains(err.Error(), "需升级 magpie") {
		t.Fatalf("checkBackupHeader(newer) = %v; want upgrade error", err)
	}
	for _, e := range []error{checkBackupHeader(nil), err} {
		if strings.Contains(strings.ToLower(e.Error()), "passphrase") {
			t.Fatalf("header error mentions passphrase: %v", e)
		}
	}
	if err := checkBackupHeader([]byte(`{"format":"magpie-backup","version":1}`)); err != nil {
		t.Fatalf("checkBackupHeader(version 1) = %v; want nil", err)
	}
}
