package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func importEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func TestImportNeedsConsentOrYes(t *testing.T) {
	importEnv(t)
	link := "magpie://import?name=Local&chat=http://127.0.0.1:9/v1&key=sk-secretsecret"

	// stdin is a pipe holding "y": still not a terminal, so nothing is added
	r, w, _ := os.Pipe()
	w.WriteString("y\n")
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
	if err := importCmd([]string{link}); err == nil {
		t.Fatal("import without -y on a non-terminal succeeded")
	}
	if provider.Exists("local") {
		t.Fatal("saved without consent")
	}

	if err := importCmd([]string{"-y", "magpie://delete?id=x"}); err == nil {
		t.Fatal("invalid link accepted")
	}
	if err := importCmd([]string{"-y", "magpie://import?name=X&chat=http://example.com/v1"}); err == nil || provider.Exists("x") {
		t.Fatal("public http accepted")
	}

	if err := importCmd([]string{"-y", link}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("local")
	if err != nil || p.Key != "sk-secretsecret" || p.Chat != "http://127.0.0.1:9/v1" {
		t.Fatalf("%+v %v", p, err)
	}
}
