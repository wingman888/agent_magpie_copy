package main

import (
	"strings"
	"testing"
)

// cliImportTestSetStdin replaces the terminal check for one test.
func cliImportTestSetStdin(t *testing.T, tty bool) {
	t.Helper()
	old := importStdinIsTerminal
	importStdinIsTerminal = func() bool { return tty }
	t.Cleanup(func() { importStdinIsTerminal = old })
}

func TestCLIImportLinkSpellings(t *testing.T) {
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

func TestCLIImportRefusesNonTerminalWithoutYes(t *testing.T) {
	cliImportTestSetStdin(t, false)
	err := importCmd([]string{"import", "magpie://import?preset=deepseek&key=sk-test"})
	if err == nil || !strings.Contains(err.Error(), "-y") {
		t.Fatalf("expected refusal mentioning -y, got %v", err)
	}
}

func TestCLIImportMissingLinkShowsUsage(t *testing.T) {
	cliImportTestSetStdin(t, true)
	err := importCmd([]string{"import"})
	if err == nil || !strings.Contains(err.Error(), "usage: magpie import") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestCLIImportRejectsBadLinkBeforeAnything(t *testing.T) {
	cliImportTestSetStdin(t, true)
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

func TestCLIImportUsageListsImport(t *testing.T) {
	if !strings.Contains(usage, "magpie import") {
		t.Fatal("help usage does not list magpie import")
	}
}
