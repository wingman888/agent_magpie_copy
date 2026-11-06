package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// isolateMagpie points config and cache at temp dirs.
func isolateMagpie(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func providersFileExists() bool {
	_, err := os.Stat(provider.Path())
	return err == nil
}

const importLocal = "magpie://import?name=Local%20Box&chat=http://127.0.0.1:1/v1&key=sk-abcdefghijklmnop"

func TestImportNonInteractiveWithoutYesFailsAndWritesNothing(t *testing.T) {
	isolateMagpie(t)
	var out bytes.Buffer
	// the input says y, but it is not a terminal: it must still refuse
	err := importLink(importLocal, false, strings.NewReader("y\n"), &out, false)
	if err == nil {
		t.Fatal("non-interactive import without -y succeeded")
	}
	if providersFileExists() {
		t.Fatal("a providers file was written")
	}
}

func TestImportDeclinedAddsNothing(t *testing.T) {
	isolateMagpie(t)
	var out bytes.Buffer
	for _, answer := range []string{"n\n", "\n", "maybe\n", ""} {
		out.Reset()
		if err := importLink(importLocal, false, strings.NewReader(answer), &out, true); err != nil {
			t.Fatalf("answer %q: %v", answer, err)
		}
		if !strings.Contains(out.String(), "not added") {
			t.Fatalf("answer %q: %s", answer, out.String())
		}
	}
	if providersFileExists() {
		t.Fatal("a provider was added without a yes")
	}
}

func TestImportShowsBeforeAskingAndMasksKey(t *testing.T) {
	isolateMagpie(t)
	var out bytes.Buffer
	if err := importLink(importLocal, false, strings.NewReader("n\n"), &out, true); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if strings.Contains(s, "sk-abcdefghijklmnop") {
		t.Fatalf("full key shown: %s", s)
	}
	if !strings.Contains(s, "sk-a…") {
		t.Fatalf("no masked key: %s", s)
	}
	if !strings.Contains(s, "127.0.0.1") {
		t.Fatalf("host not shown: %s", s)
	}
	if !strings.Contains(s, "Add it? [y/N]") {
		t.Fatalf("no question: %s", s)
	}
	if providersFileExists() {
		t.Fatal("written before the answer")
	}
}

func TestImportYesAddsWithoutAsking(t *testing.T) {
	isolateMagpie(t)
	var out bytes.Buffer
	if err := importLink(importLocal, true, strings.NewReader(""), &out, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Add it?") {
		t.Fatalf("asked despite -y: %s", out.String())
	}
	p, err := provider.Find("local-box")
	if err != nil {
		t.Fatalf("not added: %v", err)
	}
	if p.Key != "sk-abcdefghijklmnop" || p.Chat != "http://127.0.0.1:1/v1" {
		t.Fatalf("%+v", p)
	}
}

func TestImportYesAnswerAdds(t *testing.T) {
	isolateMagpie(t)
	var out bytes.Buffer
	for _, answer := range []string{"y\n", "yes\n", "Y\n"} {
		out.Reset()
		_ = provider.Delete("local-box") // start each round without it; nothing to delete at first
		if err := importLink(importLocal, false, strings.NewReader(answer), &out, true); err != nil {
			t.Fatalf("answer %q: %v", answer, err)
		}
		if _, err := provider.Find("local-box"); err != nil {
			t.Fatalf("answer %q did not add: %v", answer, err)
		}
	}
}

func TestImportExistingIDSaysReplace(t *testing.T) {
	isolateMagpie(t)
	if err := provider.Save(provider.Provider{ID: "local-box", Name: "Old", Chat: "http://127.0.0.1:1/v1", Key: "old"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := importLink(importLocal, true, strings.NewReader(""), &out, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "replace the existing provider local-box") {
		t.Fatalf("no replace notice: %s", out.String())
	}
	p, _ := provider.Find("local-box")
	if p.Name != "Local Box" {
		t.Fatalf("not replaced: %+v", p)
	}
}

func TestImportInvalidLinkWithYesWritesNothing(t *testing.T) {
	isolateMagpie(t)
	var out bytes.Buffer
	for _, link := range []string{
		"magpie://import?name=X&chat=http://api.example.com/v1&key=sk-z",
		"magpie://delete?name=X&chat=https://a.example/v1",
		"magpie://import?name=magpie&chat=https://a.example/v1",
		"magpie://import?name=X&chat=https://a.example/v1&key=sk-z%0A",
	} {
		if err := importLink(link, true, strings.NewReader(""), &out, false); err == nil {
			t.Fatalf("accepted %q", link)
		}
	}
	if providersFileExists() {
		t.Fatal("an invalid link wrote a providers file")
	}
}

func TestImportCmdArguments(t *testing.T) {
	isolateMagpie(t)
	if err := importCmd(nil); err == nil {
		t.Fatal("no link accepted")
	}
	if err := importCmd([]string{"--bogus", importLocal}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if err := importCmd([]string{importLocal, importLocal}); err == nil {
		t.Fatal("two links accepted")
	}
}
