package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestParseImportArgs(t *testing.T) {
	link := "magpie://import?name=X&chat=https://x.example/v1"
	cases := []struct {
		args    []string
		link    string
		yes     bool
		wantErr bool
	}{
		{[]string{link}, link, false, false},
		{[]string{"-y", link}, link, true, false},
		{[]string{link, "--yes"}, link, true, false},
		{[]string{}, "", false, true},
		{[]string{"-y"}, "", true, true},
		{[]string{"--bogus", link}, "", false, true},
		{[]string{link, link}, "", false, true},
	}
	for _, c := range cases {
		link, yes, err := parseImportArgs(c.args)
		if (err != nil) != c.wantErr {
			t.Errorf("parseImportArgs(%q) err = %v, wantErr %v", c.args, err, c.wantErr)
			continue
		}
		if err == nil && (link != c.link || yes != c.yes) {
			t.Errorf("parseImportArgs(%q) = %q, %v; want %q, %v", c.args, link, yes, c.link, c.yes)
		}
	}
}

func TestRequireConsent(t *testing.T) {
	if err := requireConsent(false, false); err == nil {
		t.Error("non-interactive without -y must be refused")
	}
	if err := requireConsent(true, false); err != nil {
		t.Errorf("-y must allow non-interactive use: %v", err)
	}
	if err := requireConsent(false, true); err != nil {
		t.Errorf("interactive must be allowed to ask: %v", err)
	}
}

func TestConfirmAnswers(t *testing.T) {
	cases := map[string]bool{
		"y\n":    true,
		"Y\n":    true,
		"yes\n":  true,
		"YES\n":  true,
		"y":      true, // no trailing newline at end of input
		"n\n":    false,
		"\n":     false,
		"":       false,
		"sure\n": false,
	}
	for in, want := range cases {
		var out bytes.Buffer
		got, err := confirm(bufio.NewReader(strings.NewReader(in)), &out)
		if err != nil {
			t.Fatalf("confirm(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("confirm(%q) = %v, want %v", in, got, want)
		}
		if !strings.Contains(out.String(), "Add it? [y/N]") {
			t.Errorf("confirm(%q) did not print the prompt: %q", in, out.String())
		}
	}
}

func TestSummaryShowsHostsAndKeyAndReplaces(t *testing.T) {
	p, err := provider.ParseImport("magpie://import?name=My%20Relay&chat=https://relay.example/v1&anthropic=https://api.relay.example&key=sk-abcdefghijkl&models=a,%20b")
	if err != nil {
		t.Fatal(err)
	}
	existing := &provider.Provider{ID: "my-relay", Name: "Old Relay"}

	var out bytes.Buffer
	printImportSummary(&out, p, existing)
	s := out.String()
	for _, want := range []string{"my-relay", "relay.example", "api.relay.example", "a, b", "sk-a…ijkl", "replace"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q:\n%s", want, s)
		}
	}

	var fresh bytes.Buffer
	printImportSummary(&fresh, p, nil)
	if strings.Contains(strings.ToLower(fresh.String()), "replace") {
		t.Errorf("summary warns about replacing although nothing exists:\n%s", fresh.String())
	}
}

func TestSummaryWithoutKeyMentionsKeyCommand(t *testing.T) {
	p, err := provider.ParseImport("magpie://import?name=Nokey&chat=https://nokey.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printImportSummary(&out, p, nil)
	if !strings.Contains(out.String(), "magpie provider key nokey <key>") {
		t.Errorf("summary does not say how to add the key:\n%s", out.String())
	}
}

func TestIsImportLink(t *testing.T) {
	yes := []string{
		"magpie://import?preset=deepseek",
		"magpie:import?preset=deepseek",
		"magpie:///import?preset=deepseek",
		"MAGPIE://import/?preset=deepseek",
	}
	no := []string{"", "magpie", "https://usemagpie.ai/import", "import", "magpi:x", "providers"}
	for _, s := range yes {
		if !isImportLink(s) {
			t.Errorf("isImportLink(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isImportLink(s) {
			t.Errorf("isImportLink(%q) = true, want false", s)
		}
	}
}
