package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const testKey = "sk-SECRET-ABCDEF123456"

// A base on 127.0.0.1 port 1 refuses connections at once, so the model fetch
// that follows an import fails fast and offline.
const testLink = "magpie://import?name=Acme%20Relay&chat=http://127.0.0.1:1/v1&key=" + testKey

func isolateImport(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func notWritten(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(provider.Path()); !os.IsNotExist(err) {
		t.Fatalf("providers file written (stat err %v); nothing should be saved", err)
	}
}

func TestImportYesAddsWithoutLeakingKey(t *testing.T) {
	isolateImport(t)
	var out bytes.Buffer
	if err := importLink([]string{"-y", testLink}, nil, false, &out); err != nil {
		t.Fatalf("import -y: %v\n%s", err, out.String())
	}
	p, err := provider.Find("acme-relay")
	if err != nil {
		t.Fatalf("not saved: %v", err)
	}
	if p.Key != testKey {
		t.Errorf("saved key = %q", p.Key)
	}
	text := out.String()
	if strings.Contains(text, "SECRET-ABCDEF") || strings.Contains(text, testKey) {
		t.Errorf("output shows the full key:\n%s", text)
	}
	if !strings.Contains(text, "127.0.0.1") {
		t.Errorf("output does not name the host requests go to:\n%s", text)
	}
	if strings.Contains(text, "Add it?") {
		t.Errorf("-y still asked:\n%s", text)
	}
}

func TestImportNonInteractiveWithoutYesFailsEvenWithYInInput(t *testing.T) {
	isolateImport(t)
	var out bytes.Buffer
	err := importLink([]string{testLink}, strings.NewReader("y\n"), false, &out)
	if err == nil {
		t.Fatalf("non-interactive import without -y succeeded:\n%s", out.String())
	}
	notWritten(t)
	if strings.Contains(out.String(), "SECRET-ABCDEF") {
		t.Errorf("output shows the full key")
	}
}

func TestImportDeclinedDoesNotAdd(t *testing.T) {
	for _, answer := range []string{"n\n", "\n", "nope\n", ""} {
		isolateImport(t)
		var out bytes.Buffer
		if err := importLink([]string{testLink}, strings.NewReader(answer), true, &out); err != nil {
			t.Fatalf("answer %q: %v", answer, err)
		}
		if !strings.Contains(out.String(), "Add it? [y/N]") {
			t.Errorf("answer %q: no prompt:\n%s", answer, out.String())
		}
		notWritten(t)
	}
}

func TestImportConfirmedAdds(t *testing.T) {
	for _, answer := range []string{"y\n", "yes\n", "  Y  \n"} {
		isolateImport(t)
		var out bytes.Buffer
		if err := importLink([]string{testLink}, strings.NewReader(answer), true, &out); err != nil {
			t.Fatalf("answer %q: %v", answer, err)
		}
		if _, err := provider.Find("acme-relay"); err != nil {
			t.Errorf("answer %q: not saved: %v", answer, err)
		}
	}
}

func TestImportReplaceNotice(t *testing.T) {
	isolateImport(t)
	if err := importLink([]string{"-y", testLink}, nil, false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	second := "magpie://import?name=Acme%20Relay&chat=http://127.0.0.1:1/v1&key=sk-NEW-KEY-99999999"
	if err := importLink([]string{"-y", second}, nil, false, &out); err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.Contains(l, "replace") {
			line = l
		}
	}
	if !strings.Contains(line, "will replace the existing provider acme-relay") {
		t.Errorf("no replace notice, got %q\n%s", line, out.String())
	}
	p, err := provider.Find("acme-relay")
	if err != nil || p.Key != "sk-NEW-KEY-99999999" {
		t.Errorf("replacement not saved: key=%q err=%v", p.Key, err)
	}
}

func TestImportNoReplaceNoticeForNewProvider(t *testing.T) {
	isolateImport(t)
	var out bytes.Buffer
	if err := importLink([]string{"-y", testLink}, nil, false, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "replace") {
		t.Errorf("replace notice for a new provider:\n%s", out.String())
	}
}

func TestImportInvalidLinkRejectedEvenWithYes(t *testing.T) {
	bad := []string{
		"magpie://import?name=Acme&chat=http://public.example/v1&key=" + testKey,
		"magpie://delete?name=Acme&chat=https://a.example/v1&key=" + testKey,
		"magpie://import?name=Acme&chat=https://a.example/v1&key=sk-SECRET%0A",
	}
	for _, link := range bad {
		isolateImport(t)
		var out bytes.Buffer
		err := importLink([]string{"-y", link}, nil, false, &out)
		if err == nil {
			t.Errorf("%q: invalid link accepted", link)
		}
		if err != nil && strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%q: error shows the key: %v", link, err)
		}
		notWritten(t)
	}
}

func TestImportNoKeyHintWhenNotReady(t *testing.T) {
	isolateImport(t)
	// a remote host with no key is not usable yet; .invalid never resolves, so the fetch fails fast
	var out bytes.Buffer
	if err := importLink([]string{"-y", "magpie://import?name=Remote&chat=https://remote.invalid/v1"}, nil, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "magpie provider key remote <key>") {
		t.Errorf("no key hint:\n%s", out.String())
	}
}

func TestImportArgumentForms(t *testing.T) {
	isolateImport(t)
	// the magpie: link can come first, with the flag after it
	if err := importLink([]string{testLink, "--yes"}, nil, false, &bytes.Buffer{}); err != nil {
		t.Fatalf("link then --yes: %v", err)
	}
	if err := importLink([]string{"import", "-y", "magpie://import?preset=ollama"}, nil, false, &bytes.Buffer{}); err != nil {
		t.Fatalf("import -y preset: %v", err)
	}
	for _, args := range [][]string{
		{"import"},
		{"import", "-y"},
		{"import", "--bogus", testLink},
		{"import", testLink, testLink},
	} {
		if err := importLink(args, nil, false, &bytes.Buffer{}); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestMagpieLinkDetection(t *testing.T) {
	for _, s := range []string{"magpie://import?x", "MAGPIE:import?x", "Magpie:///import"} {
		if !isMagpieLink(s) {
			t.Errorf("%q not recognised as a magpie link", s)
		}
	}
	for _, s := range []string{"magpie", "claude", "https://magpie:import", "xmagpie:"} {
		if isMagpieLink(s) {
			t.Errorf("%q recognised as a magpie link", s)
		}
	}
}

func TestKeyStatusMasks(t *testing.T) {
	got := keyStatus("sk-abcdefghijklmnop")
	if strings.Contains(got, "ijklmnop") || !strings.Contains(got, "sk-a…") {
		t.Errorf("long key shown as %q", got)
	}
	if got := keyStatus("short"); !strings.Contains(got, "provided") || strings.Contains(got, "short") {
		t.Errorf("short key shown as %q", got)
	}
	if got := keyStatus(""); strings.Contains(got, "provided") {
		t.Errorf("no key shown as %q", got)
	}
}
