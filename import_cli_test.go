package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// impcliSetup points every config and cache path at a temp dir and forces
// the terminal answer, so tests never touch the user's providers.
func impcliSetup(t *testing.T, terminal bool, answer string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	oldTerm, oldIn := importTerminal, importIn
	t.Cleanup(func() { importTerminal, importIn = oldTerm, oldIn })
	importTerminal = func() bool { return terminal }
	importIn = strings.NewReader(answer)
}

// impcliCapture returns what fn writes to standard output.
func impcliCapture(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	ferr := fn()
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out), ferr
}

// impcliStored reports whether a provider with this id is saved.
func impcliStored(id string) (provider.Provider, bool) {
	for _, p := range provider.All() {
		if p.ID == id {
			return p, true
		}
	}
	return provider.Provider{}, false
}

// Port 9 on loopback refuses connections at once, so the model fetch after
// a save fails fast and offline.
const impcliLink = "magpie://import?name=Impcli%20Relay&chat=http://127.0.0.1:9/v1/&key=sk-impcli-1234567890&models=a,%20b"

func TestImpcliRefusesWithoutTerminal(t *testing.T) {
	impcliSetup(t, false, "y\n")
	_, err := impcliCapture(t, func() error { return importCmd([]string{impcliLink}) })
	if err == nil || !strings.Contains(err.Error(), "nothing added") {
		t.Fatalf("want refusal with nothing added, got %v", err)
	}
	if _, ok := impcliStored("impcli-relay"); ok {
		t.Fatal("provider saved although confirmation was impossible")
	}
}

func TestImpcliYesFlagSkipsQuestion(t *testing.T) {
	impcliSetup(t, false, "")
	if _, err := impcliCapture(t, func() error { return importCmd([]string{"-y", impcliLink}) }); err != nil {
		t.Fatal(err)
	}
	p, ok := impcliStored("impcli-relay")
	if !ok {
		t.Fatal("provider not saved with -y")
	}
	if p.Key != "sk-impcli-1234567890" || p.Chat != "http://127.0.0.1:9/v1" {
		t.Fatalf("saved %+v", p)
	}
}

func TestImpcliConfirmYes(t *testing.T) {
	impcliSetup(t, true, "  YES \n")
	out, err := impcliCapture(t, func() error { return importCmd([]string{impcliLink}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "127.0.0.1") || !strings.Contains(out, "Add it? [y/N]") {
		t.Fatalf("summary missing host or question:\n%s", out)
	}
	if _, ok := impcliStored("impcli-relay"); !ok {
		t.Fatal("provider not saved after yes")
	}
}

func TestImpcliConfirmNoOrEmpty(t *testing.T) {
	for _, answer := range []string{"n\n", "\n", ""} {
		impcliSetup(t, true, answer)
		_, err := impcliCapture(t, func() error { return importCmd([]string{impcliLink}) })
		if err == nil || !strings.Contains(err.Error(), "nothing added") {
			t.Fatalf("answer %q: want nothing added, got %v", answer, err)
		}
		if _, ok := impcliStored("impcli-relay"); ok {
			t.Fatalf("answer %q saved a provider", answer)
		}
	}
}

func TestImpcliReplaceWarning(t *testing.T) {
	impcliSetup(t, true, "n\n")
	if err := provider.Save(provider.Provider{ID: "impcli-relay", Name: "Old", Chat: "http://127.0.0.1:9/v1", Key: "sk-old-key-value"}); err != nil {
		t.Fatal(err)
	}
	out, err := impcliCapture(t, func() error { return importCmd([]string{impcliLink}) })
	if err == nil {
		t.Fatal("declined import should fail")
	}
	if !strings.Contains(strings.ToLower(out), "this will replace the existing provider impcli-relay") {
		t.Fatalf("no replace warning before the question:\n%s", out)
	}
	if p, _ := impcliStored("impcli-relay"); p.Name != "Old" {
		t.Fatal("existing provider changed although the import was declined")
	}
}

func TestImpcliKeylessHint(t *testing.T) {
	impcliSetup(t, false, "")
	link := "magpie://import?name=Impcli%20Keyless&chat=http://[::1]:9/v1"
	out, err := impcliCapture(t, func() error { return importCmd([]string{"-y", link}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "magpie provider key impcli-keyless <key>") {
		t.Fatalf("no key hint:\n%s", out)
	}
	if p, ok := impcliStored("impcli-keyless"); !ok || p.Ready() {
		t.Fatalf("keyless provider should be saved and not ready, got %+v ok=%v", p, ok)
	}
}

func TestImpcliUsageAndArguments(t *testing.T) {
	impcliSetup(t, true, "")
	for _, args := range [][]string{nil, {"-y"}, {impcliLink, impcliLink}, {"--bogus", impcliLink}} {
		if _, err := impcliCapture(t, func() error { return importCmd(args) }); err == nil {
			t.Fatalf("args %q: want an error", args)
		}
	}
}

func TestImpcliProtocolLinkDetection(t *testing.T) {
	for _, s := range []string{"magpie://import?preset=x", "MAGPIE:import?preset=x", "Magpie:///import"} {
		if !isImportLink(s) {
			t.Errorf("%q should be an import link", s)
		}
	}
	for _, s := range []string{"https://usemagpie.ai/import", "claude", "magpi:import", ""} {
		if isImportLink(s) {
			t.Errorf("%q should not be an import link", s)
		}
	}
}
