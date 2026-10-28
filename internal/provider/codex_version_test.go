package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func codexVersionWith(t *testing.T, cache, cli string) string {
	t.Helper()
	home := t.TempDir()
	if cache != "" {
		os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
		os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"client_version":"`+cache+`"}`), 0o644)
	}
	oh, or := codexHome, codexRun
	t.Cleanup(func() {
		codexHome, codexRun = oh, or
		codexVer = ""
	})
	codexHome = func() string { return home }
	codexRun = func(context.Context) ([]byte, error) {
		if cli == "" {
			return nil, errors.New("no codex")
		}
		return []byte(cli + "\n"), nil
	}
	codexVer = ""
	return codexCacheVersion()
}

func TestCodexClientVersion(t *testing.T) {
	if v := codexVersionWith(t, "0.154.0", "codex-cli 0.155.1"); v != "0.155.1" {
		t.Errorf("cli newer: %s", v)
	}
	if v := codexVersionWith(t, "", ""); v != "0.154.0" {
		t.Errorf("no cli: %s", v)
	}
	if v := codexVersionWith(t, "0.160.2", "codex-cli 0.155.1"); v != "0.160.2" {
		t.Errorf("cache newer: %s", v)
	}
	if v := codexVersionWith(t, "0.9.0", "codex-cli 0.10.0"); v != "0.154.0" {
		t.Errorf("default newest: %s", v)
	}
}
