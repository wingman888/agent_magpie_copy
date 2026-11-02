package main

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestRunImport(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	link := "magpie://import?name=Box&chat=http://localhost:1/v1&key=sk-1&models=a,%20b"
	var saved []provider.Provider
	save := func(p provider.Provider) error { saved = append(saved, p); return provider.SaveImported(p) }

	if err := runImport(link, false, true, strings.NewReader("n\n"), save); err == nil || !strings.Contains(err.Error(), "nothing added") {
		t.Fatalf("n: %v", err)
	}
	if err := runImport(link, false, true, strings.NewReader("\n"), save); err == nil {
		t.Fatal("empty answer must not add")
	}
	if err := runImport(link, false, false, strings.NewReader("y\n"), save); err == nil {
		t.Fatal("non-terminal without -y must refuse")
	}
	if err := runImport("https://x.example/import?name=a", true, true, nil, save); err == nil {
		t.Fatal("bad link must fail")
	}
	if len(saved) != 0 {
		t.Fatalf("saved too early: %v", saved)
	}
	if err := runImport(link, false, true, strings.NewReader("yes\n"), save); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].ID != "box" || saved[0].Key != "sk-1" || len(saved[0].Models) != 2 {
		t.Fatalf("saved: %+v", saved)
	}
	// same id again: replaced with -y, no prompt
	if err := runImport(link, true, false, nil, save); err != nil || len(saved) != 2 {
		t.Fatalf("-y: %v %d", err, len(saved))
	}
	// keyless link saves too
	if err := runImport("magpie://import?name=Nokey&chat=https://nokey.example/v1", true, false, nil, save); err != nil {
		t.Fatal(err)
	}
	if p, err := provider.Find("nokey"); err != nil || p.Key != "" {
		t.Fatalf("%v %+v", err, p)
	}
}

func TestImportCmdArgs(t *testing.T) {
	if err := importCmd(nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("%v", err)
	}
	if err := importCmd([]string{"--bogus", "x"}); err == nil {
		t.Fatal("unknown flag")
	}
}
