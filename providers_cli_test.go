package main

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestApplyPairsHeaders(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{
		"url=https://relay.example.com/v1",
		"header.X-Org-Id=acme",
		"HEADER.x-gateway-auth=tok=with=equals",
	}); err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://relay.example.com/v1" {
		t.Errorf("Chat = %q", p.Chat)
	}
	want := map[string]string{"X-Org-Id": "acme", "x-gateway-auth": "tok=with=equals"}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("Headers[%q] = %q, want %q (all: %v)", k, p.Headers[k], v, p.Headers)
		}
	}
}

func TestApplyPairsIcon(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	var custom provider.Provider
	if err := applyPairs(&custom, []string{"url=https://relay.example.com/v1", "icon=openai"}); err != nil {
		t.Fatal(err)
	}
	if custom.Icon != "openai" {
		t.Errorf("Icon = %q, want openai", custom.Icon)
	}
	if err := applyPairs(&custom, []string{"icon=not-an-icon-or-file"}); err == nil {
		t.Error("unknown icon accepted")
	}

	preset, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	err = applyPairs(&preset, []string{"icon=openai"})
	if err == nil || !strings.Contains(err.Error(), "仅自定义供应商可设置") {
		t.Errorf("preset icon error = %v", err)
	}
}
