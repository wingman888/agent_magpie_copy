package main

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const testLink = "magpie://import?name=Box&chat=http://127.0.0.1:1/v1&key=k"

func withIO(t *testing.T, input string, term bool) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	old := importIO
	importIO.in = strings.NewReader(input)
	importIO.isTerminal = func() bool { return term }
	t.Cleanup(func() { importIO = old })
}

func TestImportNotTerminal(t *testing.T) {
	withIO(t, "y\n", false)
	err := importCmd([]string{testLink})
	if err == nil || err.Error() != "not a terminal: add -y to import without asking" {
		t.Fatal(err)
	}
	if _, err := provider.Find("box"); err == nil {
		t.Fatal("saved")
	}
}

func TestImportYes(t *testing.T) {
	withIO(t, "", false)
	if err := importCmd([]string{"--yes", testLink}); err != nil {
		t.Fatal(err)
	}
	if p, err := provider.Find("box"); err != nil || p.Key != "k" {
		t.Fatal(err)
	}
	// importing again replaces it
	if err := importCmd([]string{testLink, "-y"}); err != nil {
		t.Fatal(err)
	}
}

func TestImportConfirm(t *testing.T) {
	for in, ok := range map[string]bool{"y\n": true, "YES\n": true, "n\n": false, "\n": false, "": false} {
		withIO(t, in, true)
		err := importCmd([]string{testLink})
		if ok != (err == nil) {
			t.Errorf("%q: %v", in, err)
		}
		if !ok && (err == nil || err.Error() != "nothing added") {
			t.Errorf("%q: %v", in, err)
		}
		_, found := provider.Find("box")
		if ok != (found == nil) {
			t.Errorf("%q: saved=%v", in, found == nil)
		}
	}
}

func TestImportErrors(t *testing.T) {
	withIO(t, "", false)
	if err := importCmd(nil); err == nil || !strings.Contains(err.Error(), "magpie import [-y] 'magpie://import?…'") {
		t.Fatal(err)
	}
	if err := importCmd([]string{"-y", "magpie://delete"}); err == nil {
		t.Fatal("bad link accepted")
	}
}

func TestRunLink(t *testing.T) {
	withIO(t, "", false)
	if err := run([]string{"MAGPIE://import?name=Box&chat=http://127.0.0.1:1/v1&key=k"}); err == nil {
		t.Fatal("expected non-terminal error")
	}
}
