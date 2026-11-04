package main

import (
	"io"
	"os"
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
		{"defaults", nil, false, defaultBackupFile, false},
		{"file", []string{"b.magpie-backup"}, false, "b.magpie-backup", false},
		{"no keys", []string{"--no-keys"}, true, defaultBackupFile, false},
		{"no keys after file", []string{"x.magpie-backup", "--no-keys"}, true, "x.magpie-backup", false},
		{"unknown flag", []string{"--bogus"}, false, "", true},
		{"extra positional", []string{"a", "b"}, false, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noKeys, file, err := parseBackupArgs(c.args)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if err != nil {
				if !strings.Contains(err.Error(), "usage") && !strings.Contains(err.Error(), "unknown") && !strings.Contains(err.Error(), "too many") {
					t.Errorf("error does not explain usage: %v", err)
				}
				return
			}
			if noKeys != c.noKeys || file != c.file {
				t.Errorf("got (%v, %q), want (%v, %q)", noKeys, file, c.noKeys, c.file)
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
		{"file", []string{"b.magpie-backup"}, false, "b.magpie-backup", false},
		{"no agents", []string{"--no-agents", "b"}, true, "b", false},
		{"missing file", nil, false, "", true},
		{"only flag", []string{"--no-agents"}, false, "", true},
		{"unknown flag", []string{"--force", "b"}, false, "", true},
		{"extra positional", []string{"a", "b"}, false, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noAgents, file, err := parseRestoreArgs(c.args)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if err != nil {
				return
			}
			if noAgents != c.noAgents || file != c.file {
				t.Errorf("got (%v, %q), want (%v, %q)", noAgents, file, c.noAgents, c.file)
			}
		})
	}
}

// pipeWith returns the read end of a pipe holding s, already closed for writing.
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

func TestReadPassphraseNonTerminal(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		confirm bool
		want    string
		wantErr bool
	}{
		{"first line", "pw\nignored\n", false, "pw", false},
		{"crlf", "pw\r\n", true, "pw", false},
		{"no trailing newline", "pw", false, "pw", false},
		{"empty line", "\n", false, "", true},
		{"empty input", "", false, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := readPassphrase(pipeWith(t, c.input), io.Discard, c.confirm)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
