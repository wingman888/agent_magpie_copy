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
	if err := applyPairs(&p, []string{"header.X-Org-Id=acme", "Header.x-gateway-token=s3cr=t", "name=Relay"}); err != nil {
		t.Fatal(err)
	}
	if p.Headers["X-Org-Id"] != "acme" {
		t.Errorf("X-Org-Id = %q; headers %v", p.Headers["X-Org-Id"], p.Headers)
	}
	if p.Headers["x-gateway-token"] != "s3cr=t" {
		t.Errorf("x-gateway-token = %q; the name's case and the value's '=' must survive", p.Headers["x-gateway-token"])
	}
	if p.Name != "Relay" {
		t.Errorf("name = %q", p.Name)
	}
	if err := applyPairs(&p, []string{"header.Bad Name=x"}); err == nil {
		t.Error("a header name with a space was accepted")
	}
}

func TestSetIconPresetRefused(t *testing.T) {
	p := provider.Provider{Name: "DeepSeek", Preset: "deepseek"}
	err := setIcon(&p, "deepseek")
	if err == nil || !strings.Contains(err.Error(), "仅自定义供应商可设置") {
		t.Fatalf("err = %v, want the custom-only message", err)
	}
	acct := provider.Provider{Name: "Claude Code", Account: &provider.Account{Agent: "claude", User: "me@example.com"}}
	if err := setIcon(&acct, "openai"); err == nil {
		t.Error("an account's icon was set")
	}
}

func TestSetIconFileAndName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	png := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(png, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 40)...), 0o644); err != nil {
		t.Fatal(err)
	}
	p := provider.Provider{Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk"}
	if err := setIcon(&p, png); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Icon, "file:") || provider.IconPath(p.Icon) == "" {
		t.Fatalf("icon = %q, want a stored file reference", p.Icon)
	}
	if _, err := os.Stat(provider.IconPath(p.Icon)); err != nil {
		t.Fatalf("stored icon missing: %v", err)
	}

	if err := setIcon(&p, "openai"); err != nil || p.Icon != "openai" {
		t.Fatalf("built-in name: icon = %q, err = %v", p.Icon, err)
	}
	if err := setIcon(&p, ""); err != nil || p.Icon != "" {
		t.Fatalf("empty value should reset the icon, got %q, %v", p.Icon, err)
	}

	text := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(text, []byte("plain text, not a picture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setIcon(&p, text); err == nil {
		t.Error("a text file was accepted as an icon")
	}
	big := filepath.Join(dir, "big.png")
	if err := os.WriteFile(big, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, provider.MaxIcon)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setIcon(&p, big); err == nil {
		t.Error("an image over 1 MB was accepted")
	}
}
