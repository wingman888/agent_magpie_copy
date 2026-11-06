package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

func TestParseBackupArgs(t *testing.T) {
	ok := []struct {
		args   []string
		file   string
		noKeys bool
	}{
		{nil, "magpie.magpie-backup", false},
		{[]string{"--no-keys"}, "magpie.magpie-backup", true},
		{[]string{"b.magpie-backup"}, "b.magpie-backup", false},
		{[]string{"--no-keys", "/tmp/x.magpie-backup"}, "/tmp/x.magpie-backup", true},
		{[]string{"x", "--no-keys"}, "x", true},
	}
	for _, c := range ok {
		file, noKeys, err := parseBackupArgs(c.args)
		if err != nil || file != c.file || noKeys != c.noKeys {
			t.Errorf("parseBackupArgs(%q) = %q, %v, %v; want %q, %v", c.args, file, noKeys, err, c.file, c.noKeys)
		}
	}
	bad := [][]string{
		{"--force"},
		{"-"},
		{"a", "b"},
		{""},
	}
	for _, args := range bad {
		if _, _, err := parseBackupArgs(args); err == nil {
			t.Errorf("parseBackupArgs(%q) accepted bad arguments", args)
		}
	}
}

func TestParseRestoreArgs(t *testing.T) {
	file, noAgents, err := parseRestoreArgs([]string{"--no-agents", "b.magpie-backup"})
	if err != nil || file != "b.magpie-backup" || !noAgents {
		t.Errorf("got %q, %v, %v", file, noAgents, err)
	}
	file, noAgents, err = parseRestoreArgs([]string{"b.magpie-backup"})
	if err != nil || file != "b.magpie-backup" || noAgents {
		t.Errorf("got %q, %v, %v", file, noAgents, err)
	}
	bad := [][]string{
		nil,
		{"--no-agents"},
		{"a", "b"},
		{"--yes", "a"},
		{""},
	}
	for _, args := range bad {
		if _, _, err := parseRestoreArgs(args); err == nil {
			t.Errorf("parseRestoreArgs(%q) accepted bad arguments", args)
		}
	}
}

func TestReadLineFrom(t *testing.T) {
	cases := map[string]string{
		"pw\n":          "pw",
		"pw\r\nignored": "pw",
		"pw":            "pw",
		"":              "",
		"\n":            "",
	}
	for in, want := range cases {
		got, err := readLineFrom(strings.NewReader(in))
		if err != nil || got != want {
			t.Errorf("readLineFrom(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestCountBundle(t *testing.T) {
	b := backup.Bundle{
		Providers: []provider.Provider{
			{ID: "a", Key: "k"},
			{ID: "b", Keys: []provider.KeyAccount{{}}},
			{ID: "c"},
		},
		Profiles: json.RawMessage(`{"work":{},"home":{}}`),
		Agents:   json.RawMessage(`{"claude.model":"x","codex.model":"y","codex.effort":"high"}`),
	}
	got := countBundle(b)
	want := backupCounts{providers: 3, withKey: 2, profiles: 2, agents: 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("countBundle = %+v; want %+v", got, want)
	}
	if empty := countBundle(backup.Bundle{}); !reflect.DeepEqual(empty, backupCounts{}) {
		t.Errorf("countBundle(empty) = %+v", empty)
	}
}

func TestWriteBackupFileIsOwnerOnly(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("file modes are not meaningful on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "b.magpie-backup")
	// An existing, more open file must be tightened too.
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
		t.Errorf("mode = %o; want 600", st.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Errorf("content = %q", data)
	}
}
