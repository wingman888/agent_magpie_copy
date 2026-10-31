package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func importEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
}

const testKey = "sk-secret-0123456789"

var testLink = "magpie://import?name=Local%20Relay&chat=http://127.0.0.1:1/v1&key=" + testKey

func TestImportNonInteractiveWithoutYes(t *testing.T) {
	importEnv(t)
	var out bytes.Buffer
	err := runImport([]string{testLink}, strings.NewReader("y\n"), &out, func() bool { return false })
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(provider.All()) != 0 {
		t.Fatal("saved something")
	}
	if strings.Contains(out.String(), testKey) || !strings.Contains(out.String(), "127.0.0.1") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestImportPrompt(t *testing.T) {
	importEnv(t)
	var out bytes.Buffer
	if err := runImport([]string{testLink}, strings.NewReader("n\n"), &out, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if len(provider.All()) != 0 || !strings.Contains(out.String(), "Add it? [y/N]") {
		t.Fatalf("%s", out.String())
	}
	out.Reset()
	if err := runImport([]string{testLink}, strings.NewReader("yes\n"), &out, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if p, err := provider.Find("local-relay"); err != nil || p.Key != testKey {
		t.Fatalf("%v %v", p, err)
	}
	out.Reset()
	if err := runImport([]string{"-y", testLink}, nil, &out, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "replace") || strings.Contains(out.String(), testKey) {
		t.Fatalf("%s", out.String())
	}
}

func TestImportInvalidWithYes(t *testing.T) {
	importEnv(t)
	var out bytes.Buffer
	if err := runImport([]string{"-y", "magpie://import?name=x&chat=http://example.com/v1"}, nil, &out, func() bool { return true }); err == nil {
		t.Fatal("expected error")
	}
	if len(provider.All()) != 0 {
		t.Fatal("saved")
	}
}

func TestImportNoKeyHint(t *testing.T) {
	importEnv(t)
	var out bytes.Buffer
	if err := runImport([]string{"--yes", "magpie://import?name=Acme&chat=https://api.acme.example/v1"}, nil, &out, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "magpie provider key acme") {
		t.Fatalf("%s", out.String())
	}
}
