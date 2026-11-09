package main

import (
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestImpCLIKeyIsMasked(t *testing.T) {
	key := "sk-abcdefghijklmnopqrstuvwxyz"
	shown := importKey(key)
	if strings.Contains(shown, key) || strings.Contains(shown, "qrstuvwxyz") {
		t.Errorf("importKey leaked the key: %q", shown)
	}
	if !strings.Contains(shown, "sk-a…") {
		t.Errorf("importKey(%q) = %q, want leading characters and …", key, shown)
	}
	if got := importKey("short"); strings.Contains(got, "short") {
		t.Errorf("importKey(short) leaked: %q", got)
	}
	if got := importKey(""); !strings.Contains(got, "not given") {
		t.Errorf("importKey(empty) = %q", got)
	}
}

func TestImpCLIAnswers(t *testing.T) {
	for _, s := range []string{"y", "Y", "yes", " YES \n"} {
		if !isYes(s) {
			t.Errorf("isYes(%q) = false", s)
		}
	}
	for _, s := range []string{"", "n", "no", "yess", "1"} {
		if isYes(s) {
			t.Errorf("isYes(%q) = true", s)
		}
	}
}

func TestImpCLIInvalidLinkWritesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	for _, l := range []string{
		"magpie://delete?name=A&chat=https://a.example/v1",
		"magpie://import?name=A&chat=http://a.example/v1&key=sk-x",
		"magpie://import?name=A&chat=https://a.example/v1&key=sk-a%0Ab",
	} {
		if err := importCmd([]string{"-y", l}); err == nil {
			t.Errorf("importCmd(-y, %q) succeeded, want error", l)
		}
	}
	if _, err := os.Stat(provider.Path()); !os.IsNotExist(err) {
		t.Errorf("an invalid link wrote %s", provider.Path())
	}
}

func TestImpCLIArgumentErrors(t *testing.T) {
	if err := importCmd(nil); err == nil {
		t.Error("importCmd() with no link succeeded")
	}
	if err := importCmd([]string{"-x", "magpie://import"}); err == nil {
		t.Error("unknown flag accepted")
	}
	if err := importCmd([]string{"a", "b"}); err == nil {
		t.Error("two links accepted")
	}
}
