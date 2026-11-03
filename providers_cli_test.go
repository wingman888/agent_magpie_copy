package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestApplyPairsHeaders(t *testing.T) {
	var p provider.Provider
	err := applyPairs(&p, []string{"url=https://relay.example/v1", "key=sk", "header.X-Org-Id=acme", "HEADER.x-gateway=gw=1"})
	if err != nil {
		t.Fatal(err)
	}
	// Names keep the case typed; the prefix is case-insensitive; values may hold "=".
	if p.Headers["X-Org-Id"] != "acme" || p.Headers["x-gateway"] != "gw=1" || len(p.Headers) != 2 {
		t.Fatalf("headers = %#v", p.Headers)
	}
}

func TestApplyPairsRejectsBadHeaders(t *testing.T) {
	for _, kv := range []string{"header.=x", "header.Bad Name=x", "header.X-A=line\nbreak"} {
		var p provider.Provider
		if err := applyPairs(&p, []string{kv}); err == nil {
			t.Errorf("%q accepted", kv)
		}
	}
}

func TestApplyPairsIconNeedsCustomProvider(t *testing.T) {
	p := provider.Provider{ID: "openai", Preset: "openai"}
	err := applyPairs(&p, []string{"icon=openai"})
	if err == nil || !strings.Contains(err.Error(), "only custom providers") {
		t.Fatalf("preset icon error = %v", err)
	}
}

func TestIconArg(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if ref, err := iconArg("ollama"); err != nil || ref != "ollama" {
		t.Errorf("built-in: %q, %v", ref, err)
	}
	if ref, err := iconArg(""); err != nil || ref != "" {
		t.Errorf("empty: %q, %v", ref, err)
	}
	if _, err := iconArg("no-such-icon"); err == nil {
		t.Error("unknown name accepted")
	}

	dir := t.TempDir()
	png := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(png, append([]byte("\x89PNG\r\n\x1a\n"), 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := iconArg(png)
	if err != nil || !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") {
		t.Fatalf("picture file: %q, %v", ref, err)
	}

	txt := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(txt, []byte("just text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := iconArg(txt); err == nil {
		t.Error("plain text accepted as an icon")
	}

	big := filepath.Join(dir, "big.png")
	if err := os.WriteFile(big, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, provider.MaxIcon)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := iconArg(big); err == nil {
		t.Error("picture over 1 MB accepted")
	}
}
