package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// isolate points every config and cache dir at a temp dir and returns it.
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir+"/config")
	t.Setenv("XDG_CACHE_HOME", dir+"/cache")
}

// a base on 127.0.0.1 with a closed port: the post-add model fetch fails fast.
const testLink = "magpie://import?name=Acme%20Relay&chat=http://127.0.0.1:1/v1&key=sk-SECRET-VALUE-123"

func TestImportNonInteractiveWithoutYesFailsAndWritesNothing(t *testing.T) {
	isolate(t)
	var out bytes.Buffer
	err := importLink([]string{testLink}, strings.NewReader("y\n"), &out, false)
	if err == nil {
		t.Fatal("expected an error without a terminal and without -y")
	}
	if len(provider.All()) != 0 {
		t.Fatal("provider was saved")
	}
	if _, statErr := os.Stat(provider.Path()); statErr == nil {
		t.Fatal("providers file was written")
	}
}

func TestImportDeclineDoesNotAdd(t *testing.T) {
	isolate(t)
	var out bytes.Buffer
	for _, answer := range []string{"\n", "n\n", "nope\n", ""} {
		if err := importLink([]string{testLink}, strings.NewReader(answer), &out, true); err != nil {
			t.Fatalf("answer %q: %v", answer, err)
		}
	}
	if len(provider.All()) != 0 {
		t.Fatal("a declined import was saved")
	}
	if !strings.Contains(out.String(), "Add it? [y/N]") {
		t.Fatalf("no confirmation prompt:\n%s", out.String())
	}
}

func TestImportConfirmAddsAndNeverShowsKey(t *testing.T) {
	isolate(t)
	var out bytes.Buffer
	if err := importLink([]string{testLink}, strings.NewReader("yes\n"), &out, true); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("acme-relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "sk-SECRET-VALUE-123" {
		t.Fatalf("key not saved: %q", p.Key)
	}
	if !strings.Contains(out.String(), "127.0.0.1") {
		t.Fatalf("host not shown:\n%s", out.String())
	}
	if strings.Contains(out.String(), "SECRET") {
		t.Fatalf("full key printed:\n%s", out.String())
	}
}

func TestImportYesSkipsPromptAndNeverShowsKey(t *testing.T) {
	isolate(t)
	var out bytes.Buffer
	if err := importLink([]string{"-y", testLink}, strings.NewReader(""), &out, false); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Find("acme-relay"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "SECRET") || strings.Contains(out.String(), "[y/N]") {
		t.Fatalf("unexpected output with -y:\n%s", out.String())
	}
}

func TestImportInvalidLinkFailsEvenWithYes(t *testing.T) {
	isolate(t)
	var out bytes.Buffer
	bad := "magpie://import?name=X&chat=http://api.example.com/v1&key=sk-LEAK"
	if err := importLink([]string{"-y", bad}, strings.NewReader(""), &out, false); err == nil {
		t.Fatal("invalid link accepted")
	}
	if len(provider.All()) != 0 {
		t.Fatal("invalid link saved something")
	}
	if strings.Contains(out.String(), "LEAK") {
		t.Fatal("key echoed")
	}
}

func TestImportReplaceNotice(t *testing.T) {
	isolate(t)
	var out bytes.Buffer
	if err := importLink([]string{"-y", testLink}, strings.NewReader(""), &out, false); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := importLink([]string{testLink}, strings.NewReader("n\n"), &out, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "replace") || !strings.Contains(out.String(), "acme-relay") {
		t.Fatalf("no replace notice:\n%s", out.String())
	}
}

func TestImportKeylessPointsAtProviderKey(t *testing.T) {
	isolate(t)
	var out bytes.Buffer
	link := "magpie://import?name=Keyless&chat=http://127.0.0.1:1/v1"
	if err := importLink([]string{"-y", link}, strings.NewReader(""), &out, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "magpie provider key keyless") {
		t.Fatalf("no key hint:\n%s", out.String())
	}
}

func TestIsMagpieLink(t *testing.T) {
	for _, a := range []string{"magpie://import?x", "MAGPIE:import?x", "magpie:///import"} {
		if !isMagpieLink(a) {
			t.Errorf("%s not recognised", a)
		}
	}
	for _, a := range []string{"import", "magpie", "codex"} {
		if isMagpieLink(a) {
			t.Errorf("%s recognised as a link", a)
		}
	}
}
