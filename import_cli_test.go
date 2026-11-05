package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// isolatedConfig points HOME and the config dir at a temp directory.
func isolatedConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

func providersFileExists(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(provider.Path())
	return err == nil
}

// captureStdout runs f and returns what it printed.
func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	ferr := f()
	w.Close()
	os.Stdout = old
	return <-done, ferr
}

func TestImportWithoutTerminalNeedsYes(t *testing.T) {
	isolatedConfig(t)
	// a pipe carrying "y" is not a terminal, and must not count as an answer
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString("y\n")
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; r.Close() }()

	link := "magpie://import?name=Acme&chat=https://api.acme.example/v1&key=sk-abcdefghijkl"
	if _, err := captureStdout(t, func() error { return importCmd([]string{link}) }); err == nil {
		t.Fatal("non-interactive import without -y succeeded")
	}
	if providersFileExists(t) {
		t.Fatal("something was written before confirmation")
	}
}

func TestImportInvalidLinkWritesNothingEvenWithYes(t *testing.T) {
	isolatedConfig(t)
	for _, link := range []string{
		"magpie://import?name=X&chat=http://api.example.com/v1",
		"magpie://delete?name=X&chat=https://a.example/v1",
		"magpie://import?name=X&chat=https://a.example/v1&key=sk-1%0Aevil",
		"magpie://import?preset=nosuch&key=k",
	} {
		if _, err := captureStdout(t, func() error { return importCmd([]string{"-y", link}) }); err == nil {
			t.Errorf("accepted %s", link)
		}
	}
	if providersFileExists(t) {
		t.Fatal("an invalid link wrote the providers file")
	}
}

func TestImportYesSavesAndNeverPrintsTheKey(t *testing.T) {
	isolatedConfig(t)
	const key = "sk-secret-abcdefgh-1234"
	// 127.0.0.1 with nothing listening: the model fetch fails fast and is fine
	link := "magpie://import?name=Local%20Box&chat=http://127.0.0.1:1/v1&key=" + key
	out, err := captureStdout(t, func() error { return importCmd([]string{"--yes", link}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, key) || strings.Contains(out, "abcdefgh-1234") {
		t.Fatalf("full key printed:\n%s", out)
	}
	if !strings.Contains(out, "127.0.0.1") {
		t.Fatalf("destination host not shown:\n%s", out)
	}
	p, err := provider.Find("local-box")
	if err != nil || p.Key != key || p.Chat != "http://127.0.0.1:1/v1" {
		t.Fatalf("saved: %+v %v", p, err)
	}
}

func TestImportReplaceNoticeMentionsReplace(t *testing.T) {
	isolatedConfig(t)
	link := "magpie://import?name=Local%20Box&chat=http://127.0.0.1:1/v1&key=k-one"
	if _, err := captureStdout(t, func() error { return importCmd([]string{"-y", link}) }); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return importCmd([]string{"-y", link}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "replace") || !strings.Contains(out, "local-box") {
		t.Fatalf("no replace notice:\n%s", out)
	}
}

func TestIsMagpieLink(t *testing.T) {
	for s, want := range map[string]bool{
		"magpie://import?x": true, "MAGPIE:import": true, "magpie": false, "https://magpie": false, "import": false,
	} {
		if isMagpieLink(s) != want {
			t.Errorf("isMagpieLink(%q) != %v", s, want)
		}
	}
}

func TestImportReplaceKeepsKeyOnlyForSameAddresses(t *testing.T) {
	isolatedConfig(t)
	const key = "sk-keep-abcdefgh-9999"
	first := "magpie://import?name=Local%20Box&chat=http://127.0.0.1:1/v1&key=" + key
	if _, err := captureStdout(t, func() error { return importCmd([]string{"-y", first}) }); err != nil {
		t.Fatal(err)
	}

	// same addresses, no key in the link: the saved key is carried over
	same := "magpie://import?name=Local%20Box&chat=http://127.0.0.1:1/v1"
	if _, err := captureStdout(t, func() error { return importCmd([]string{"-y", same}) }); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Exact("local-box")
	if err != nil || p.Key != key {
		t.Fatalf("same addresses: key not kept: %+v %v", p, err)
	}

	// different addresses, no key in the link: the saved key must not move
	other := "magpie://import?name=Local%20Box&chat=http://127.0.0.1:2/v1"
	out, err := captureStdout(t, func() error { return importCmd([]string{"-y", other}) })
	if err != nil {
		t.Fatal(err)
	}
	p, err = provider.Exact("local-box")
	if err != nil || p.Key != "" || p.Chat != "http://127.0.0.1:2/v1" {
		t.Fatalf("different addresses: key was reused: %+v %v", p, err)
	}
	if !strings.Contains(out, "previous key not reused") {
		t.Fatalf("no note about the key not being reused:\n%s", out)
	}
	if strings.Contains(out, key) || strings.Contains(out, "abcdefgh-9999") {
		t.Fatalf("full key printed:\n%s", out)
	}
}

func TestImportReplaceMatchesIDNotName(t *testing.T) {
	isolatedConfig(t)
	// an existing provider whose display name is "relay" but whose id differs
	if err := provider.Save(provider.Provider{ID: "relay-old", Name: "relay", Chat: "http://127.0.0.1:1/v1", Key: "sk-old-zzzzzzzz"}); err != nil {
		t.Fatal(err)
	}
	// same addresses and no key: a name match must not donate its key
	link := "magpie://import?name=relay&chat=http://127.0.0.1:1/v1"
	if _, err := captureStdout(t, func() error { return importCmd([]string{"-y", link}) }); err != nil {
		t.Fatal(err)
	}
	old, err := provider.Exact("relay-old")
	if err != nil || old.Key != "sk-old-zzzzzzzz" {
		t.Fatalf("existing provider changed: %+v %v", old, err)
	}
	created, err := provider.Exact("relay")
	if err != nil || created.Key != "" {
		t.Fatalf("name match donated a key: %+v %v", created, err)
	}
}
