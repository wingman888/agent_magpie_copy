package main

import (
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// f07TestCLISetStdin replaces the terminal check for one test.
func f07TestCLISetStdin(t *testing.T, tty bool) {
	t.Helper()
	old := importStdinIsTerminal
	importStdinIsTerminal = func() bool { return tty }
	t.Cleanup(func() { importStdinIsTerminal = old })
}

func TestF07CLIImportLinkSpellings(t *testing.T) {
	for _, s := range []string{
		"magpie://import?preset=deepseek",
		"magpie:import?preset=deepseek",
		"magpie:///import?preset=deepseek",
		"MAGPIE://import/?preset=deepseek",
	} {
		if !isImportLink(s) {
			t.Errorf("isImportLink(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"https://usemagpie.ai/import", "magpi:x", "", "magpie"} {
		if isImportLink(s) {
			t.Errorf("isImportLink(%q) = true, want false", s)
		}
	}
}

func TestF07CLIImportRefusesNonTerminalWithoutYes(t *testing.T) {
	f07TestCLISetStdin(t, false)
	err := importCmd([]string{"import", "magpie://import?preset=deepseek&key=sk-test"})
	if err == nil || !strings.Contains(err.Error(), "-y") {
		t.Fatalf("expected refusal mentioning -y, got %v", err)
	}
}

func TestF07CLIImportMissingLinkShowsUsage(t *testing.T) {
	f07TestCLISetStdin(t, true)
	err := importCmd([]string{"import"})
	if err == nil || !strings.Contains(err.Error(), "usage: magpie import") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestF07CLIImportRejectsBadLinkBeforeAnything(t *testing.T) {
	f07TestCLISetStdin(t, true)
	for _, link := range []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"magpie://delete?id=x",
		"magpie://import?preset=nope",
	} {
		if err := importCmd([]string{"import", "-y", link}); err == nil {
			t.Errorf("importCmd(%q) accepted a bad link", link)
		}
	}
}

func TestF07CLIImportUsageListsImport(t *testing.T) {
	if !strings.Contains(usage, "magpie import") {
		t.Fatal("help usage does not list magpie import")
	}
}

func TestF07CLIImportStdinDevNullIsNotTerminal(t *testing.T) {
	// the real check, not the test stub: /dev/null is a character device
	// but nobody can answer on it
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	defer f.Close()
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()

	if importStdinIsTerminalReal() {
		t.Fatal("stdin on /dev/null was treated as a terminal")
	}
}

func TestF07CLIImportRefusesDevNullWithoutYes(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	defer f.Close()
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()

	oldTTY := importStdinIsTerminal
	importStdinIsTerminal = importStdinIsTerminalReal
	t.Cleanup(func() { importStdinIsTerminal = oldTTY })

	err = importCmd([]string{"import", "magpie://import?preset=deepseek&key=sk-test"})
	if err == nil || !strings.Contains(err.Error(), "-y") {
		t.Fatalf("expected refusal mentioning -y on /dev/null, got %v", err)
	}
}

func TestF07CLIImportAccountIDConflict(t *testing.T) {
	accts := []provider.Provider{{ID: "codex", Name: "Codex"}, {ID: "claude", Name: "Claude Code"}}
	if err := importCheckAccountID("codex", accts); err == nil || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("expected account id conflict error, got %v", err)
	}
	if err := importCheckAccountID("deepseek", accts); err != nil {
		t.Fatalf("non-conflicting id rejected: %v", err)
	}
}
