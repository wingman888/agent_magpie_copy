package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A custom link whose endpoint is a closed localhost port: saving needs no
// network (localhost needs no key) and the model fetch fails at once.
const testLink = "magpie://import?name=Test%20Relay&chat=http://127.0.0.1:1/v1&key=sk-test-1234"

// isolateConfig points HOME and the XDG dirs at a temp directory.
func isolateConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func savedKey(t *testing.T, id string) (string, bool) {
	t.Helper()
	p, err := provider.Find(id)
	if err != nil {
		return "", false
	}
	return p.Key, true
}

func TestImportNonInteractiveRefuses(t *testing.T) {
	isolateConfig(t)
	var out bytes.Buffer
	err := runImport([]string{testLink}, strings.NewReader("y\n"), &out, false)
	if err == nil || !strings.Contains(err.Error(), "nothing added") {
		t.Fatalf("err = %v, want a nothing-added refusal", err)
	}
	if _, ok := savedKey(t, "test-relay"); ok {
		t.Fatal("provider was saved without confirmation")
	}
	if !strings.Contains(out.String(), "127.0.0.1") {
		t.Fatalf("summary does not name the receiving host:\n%s", out.String())
	}
}

func TestImportYesAddsWithoutPrompt(t *testing.T) {
	isolateConfig(t)
	var out bytes.Buffer
	if err := runImport([]string{"-y", testLink}, strings.NewReader(""), &out, false); err != nil {
		t.Fatal(err)
	}
	if key, ok := savedKey(t, "test-relay"); !ok || key != "sk-test-1234" {
		t.Fatalf("saved=%v key=%q", ok, key)
	}
	if strings.Contains(out.String(), "Add it?") {
		t.Fatalf("-y still asked:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Your prompts and API key will be sent to: 127.0.0.1:1") {
		t.Fatalf("summary does not say what is sent where:\n%s", out.String())
	}
}

func TestImportConfirmDeclined(t *testing.T) {
	isolateConfig(t)
	for _, in := range []string{"n\n", "\n", ""} {
		var out bytes.Buffer
		err := runImport([]string{testLink}, strings.NewReader(in), &out, true)
		if err == nil || !strings.Contains(err.Error(), "nothing added") {
			t.Fatalf("input %q: err = %v", in, err)
		}
		if !strings.Contains(out.String(), "Add it? [y/N]") {
			t.Fatalf("input %q: no prompt shown", in)
		}
	}
	if _, ok := savedKey(t, "test-relay"); ok {
		t.Fatal("declined import was saved")
	}
}

func TestImportConfirmAccepted(t *testing.T) {
	isolateConfig(t)
	for _, in := range []string{"y\n", "YES\n", "  Yes  \n"} {
		if err := runImport([]string{testLink}, strings.NewReader(in), &bytes.Buffer{}, true); err != nil {
			t.Fatalf("input %q: %v", in, err)
		}
	}
	if key, ok := savedKey(t, "test-relay"); !ok || key != "sk-test-1234" {
		t.Fatalf("saved=%v key=%q", ok, key)
	}
}

func TestImportReplaceWarningBeforePrompt(t *testing.T) {
	isolateConfig(t)
	if err := provider.Save(provider.Provider{ID: "test-relay", Name: "Old", Chat: "http://127.0.0.1:1/v1", Key: "sk-old"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runImport([]string{testLink}, strings.NewReader("n\n"), &out, true)
	if err == nil || !strings.Contains(err.Error(), "nothing added") {
		t.Fatalf("err = %v", err)
	}
	text := out.String()
	summary := strings.Index(text, "Import:")
	warn := strings.Index(strings.ToLower(text), "replace")
	prompt := strings.Index(text, "Add it? [y/N]")
	if summary < 0 || warn < 0 || prompt < 0 {
		t.Fatalf("missing summary/warning/prompt (%d, %d, %d):\n%s", summary, warn, prompt, text)
	}
	if !(summary < warn && warn < prompt) {
		t.Fatalf("order wrong: summary=%d warn=%d prompt=%d", summary, warn, prompt)
	}
	if !strings.Contains(text, "This will replace the existing provider test-relay") {
		t.Fatalf("warning text: %s", text)
	}
	if key, _ := savedKey(t, "test-relay"); key != "sk-old" {
		t.Fatalf("declined replacement changed the provider: key=%q", key)
	}
	if err := runImport([]string{"-y", testLink}, strings.NewReader(""), &bytes.Buffer{}, false); err != nil {
		t.Fatal(err)
	}
	if key, _ := savedKey(t, "test-relay"); key != "sk-test-1234" {
		t.Fatalf("confirmed replacement not applied: key=%q", key)
	}
}

func TestImportNoWarningForNewProvider(t *testing.T) {
	isolateConfig(t)
	var out bytes.Buffer
	_ = runImport([]string{testLink}, strings.NewReader("n\n"), &out, true)
	if strings.Contains(strings.ToLower(out.String()), "replace") {
		t.Fatalf("warning shown for a new provider:\n%s", out.String())
	}
}

func TestImportUsageAndBadLinks(t *testing.T) {
	isolateConfig(t)
	if err := runImport(nil, strings.NewReader(""), &bytes.Buffer{}, false); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("no link: err = %v", err)
	}
	for _, link := range []string{"magpie://delete?id=x", "magpie://import", "https://usemagpie.ai/import?preset=deepseek"} {
		if err := runImport([]string{"-y", link}, strings.NewReader(""), &bytes.Buffer{}, false); err == nil {
			t.Fatalf("%s accepted", link)
		}
	}
	if err := runImport([]string{"-y", "--bogus", testLink}, strings.NewReader(""), &bytes.Buffer{}, false); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if _, ok := savedKey(t, "test-relay"); ok {
		t.Fatal("a rejected link saved something")
	}
}

func TestIsImportLink(t *testing.T) {
	for _, s := range []string{"magpie://import?preset=deepseek", "magpie:import?preset=deepseek", "MAGPIE://import/?x=1", "Magpie:///import?x"} {
		if !isImportLink(s) {
			t.Errorf("%q not recognised", s)
		}
	}
	for _, s := range []string{"magpie", "claude", "https://magpie.example", "magpiex:import", "codex"} {
		if isImportLink(s) {
			t.Errorf("%q wrongly recognised", s)
		}
	}
}
