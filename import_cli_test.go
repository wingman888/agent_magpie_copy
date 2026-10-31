package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func testEnv(in string, tty bool, existing bool, saved *[]provider.Provider) (importEnv, *bytes.Buffer) {
	var out bytes.Buffer
	return importEnv{
		in: strings.NewReader(in), out: &out, isTerminal: func() bool { return tty },
		existing: func(string) bool { return existing },
		finish:   func(p provider.Provider) error { *saved = append(*saved, p); return nil },
	}, &out
}

const testLink = "magpie://import?preset=deepseek&key=sk-abc&models=a,%20b"

func TestRunImport(t *testing.T) {
	var saved []provider.Provider
	env, out := testEnv("y\n", true, false, &saved)
	if err := runImport([]string{testLink}, env); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].ID != "deepseek" || saved[0].Key != "sk-abc" {
		t.Fatalf("%+v", saved)
	}
	if !strings.Contains(out.String(), "api.deepseek.com") || !strings.Contains(out.String(), "Add it? [y/N]") {
		t.Fatal(out.String())
	}
	for _, in := range []string{"n\n", "\n", ""} {
		saved = nil
		env, _ := testEnv(in, true, false, &saved)
		err := runImport([]string{testLink}, env)
		if err == nil || !strings.Contains(err.Error(), "nothing added") || len(saved) != 0 {
			t.Fatalf("%q: %v %v", in, err, saved)
		}
	}
}

func TestRunImportNonInteractive(t *testing.T) {
	var saved []provider.Provider
	env, _ := testEnv("y\n", false, false, &saved)
	if err := runImport([]string{testLink}, env); err == nil || len(saved) != 0 {
		t.Fatalf("%v %v", err, saved)
	}
	env, out := testEnv("", false, true, &saved)
	if err := runImport([]string{"-y", testLink}, env); err != nil || len(saved) != 1 {
		t.Fatalf("%v %v", err, saved)
	}
	if !strings.Contains(strings.ToLower(out.String()), "replace") {
		t.Fatal(out.String())
	}
}

func TestRunImportErrors(t *testing.T) {
	var saved []provider.Provider
	env, _ := testEnv("y\n", true, false, &saved)
	if err := runImport(nil, env); err == nil {
		t.Fatal("no link accepted")
	}
	if err := runImport([]string{"-y", "magpie://delete?id=x"}, env); err == nil || len(saved) != 0 {
		t.Fatal("bad link accepted")
	}
}
