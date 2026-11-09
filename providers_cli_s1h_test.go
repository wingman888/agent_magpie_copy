package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestS1HAddPairsHeadersAndIcons(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	var p provider.Provider
	if err := applyPairs(&p, []string{"url=https://relay.example.com/v1", "key=sk", "header.X-Org-Id=acme", "HEADER.x-gw=a=b", "icon=openai"}); err != nil {
		t.Fatal(err)
	}
	if p.Headers["X-Org-Id"] != "acme" || p.Headers["x-gw"] != "a=b" || len(p.Headers) != 2 {
		t.Errorf("headers: %v", p.Headers)
	}
	if p.Icon != "openai" {
		t.Errorf("built-in icon: %q", p.Icon)
	}

	png := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(png, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...), 0o644); err != nil {
		t.Fatal(err)
	}
	var q provider.Provider
	if err := applyPairs(&q, []string{"icon=" + png}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(q.Icon, "file:") || !strings.HasSuffix(q.Icon, ".png") {
		t.Errorf("picture icon: %q", q.Icon)
	}

	pre := provider.Provider{Name: "DeepSeek", Preset: "deepseek"}
	if err := applyPairs(&pre, []string{"icon=openai"}); err == nil || !strings.Contains(err.Error(), "仅自定义供应商可设置图标") {
		t.Errorf("preset icon: %v", err)
	}

	if err := applyPairs(&provider.Provider{}, []string{"header.=x"}); err == nil {
		t.Error("a header with no name was accepted")
	}

	txt := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(txt, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := rsiIconArg(txt); err == nil {
		t.Error("a text file was accepted as an icon")
	}
	if _, err := rsiIconArg("file:0123456789abcdef.png"); err == nil {
		t.Error("a file: reference was accepted as a built-in name")
	}
	if v, err := rsiIconArg(""); err != nil || v != "" {
		t.Errorf("empty icon: %q %v", v, err)
	}
}
