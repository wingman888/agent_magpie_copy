package main

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestApplyPairsHeaders(t *testing.T) {
	var p provider.Provider
	err := applyPairs(&p, []string{
		"HEADER.X-ORG-id =  padded  ",
		"header.X-Org-Id=acme",
		"Header. =dropped",
		"header.Authorization=Bearer user",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"X-ORG-id":      "padded",
		"X-Org-Id":      "acme",
		"Authorization": "Bearer user",
	}
	if len(p.Headers) != len(want) {
		t.Fatalf("headers = %v, want %v", p.Headers, want)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("header %q = %q, want %q", k, p.Headers[k], v)
		}
	}
}

func TestApplyPairsEmptyIcon(t *testing.T) {
	p := provider.Provider{Icon: "deepseek-color"}
	if err := applyPairs(&p, []string{"icon=  "}); err != nil {
		t.Fatal(err)
	}
	if p.Icon != "" {
		t.Errorf("icon = %q, want empty (default)", p.Icon)
	}
}

func TestApplyPairsMissingIconPathFails(t *testing.T) {
	var p provider.Provider
	err := applyPairs(&p, []string{"icon=./no-such-dir/logo.png"})
	if err == nil {
		t.Fatal("expected an error for a missing icon file")
	}
}

func TestSetIconRejectsPresetAndAccount(t *testing.T) {
	cases := map[string]provider.Provider{
		"preset":  {ID: "deepseek", Preset: "deepseek", Icon: "deepseek-color"},
		"account": {ID: "claude", Account: &provider.Account{User: "someone"}},
	}
	for name, p := range cases {
		err := setIcon(&p, "generic")
		if err == nil {
			t.Fatalf("%s: expected error", name)
		}
		if !strings.Contains(err.Error(), "custom") {
			t.Errorf("%s: error %q should mention custom", name, err)
		}
	}
}

func TestSetIconBuiltinAndDefault(t *testing.T) {
	p := provider.Provider{ID: "relay", Name: "Relay"}
	if err := setIcon(&p, "deepseek-color"); err != nil {
		t.Fatal(err)
	}
	if p.Icon != "deepseek-color" {
		t.Errorf("icon = %q, want deepseek-color", p.Icon)
	}
	if err := setIcon(&p, ""); err != nil {
		t.Fatal(err)
	}
	if p.Icon != "" {
		t.Errorf("icon = %q, want empty after reset", p.Icon)
	}
}

func TestApplyPairsMissingImageNameFails(t *testing.T) {
	p := provider.Provider{ID: "relay", Icon: "deepseek-color"}
	err := applyPairs(&p, []string{"icon=missing.png"})
	if err == nil {
		t.Fatal("expected an error for a missing image file name")
	}
	if !strings.Contains(err.Error(), "icon file not found") {
		t.Errorf("error %q should say icon file not found", err)
	}
	if p.Icon != "deepseek-color" {
		t.Errorf("icon = %q, want unchanged deepseek-color", p.Icon)
	}
}

func TestSetIconMissingImageNameKeepsIcon(t *testing.T) {
	for _, name := range []string{"missing.PNG", "logo.svg", "pic.jpeg"} {
		p := provider.Provider{ID: "relay", Icon: "deepseek-color"}
		if err := setIcon(&p, name); err == nil {
			t.Fatalf("%s: expected error", name)
		}
		if p.Icon != "deepseek-color" {
			t.Errorf("%s: icon = %q, want unchanged", name, p.Icon)
		}
	}
}

func TestSetIconDirectoryFails(t *testing.T) {
	dir := t.TempDir()
	p := provider.Provider{ID: "relay", Icon: "deepseek-color"}
	if err := setIcon(&p, dir); err == nil {
		t.Fatal("expected an error for a directory")
	}
	if p.Icon != "deepseek-color" {
		t.Errorf("icon = %q, want unchanged", p.Icon)
	}
}

func TestSetIconBuiltinNamesNotAffected(t *testing.T) {
	for _, name := range []string{"deepseek-color", "generic"} {
		p := provider.Provider{ID: "relay"}
		if err := setIcon(&p, name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p.Icon != name {
			t.Errorf("icon = %q, want %q", p.Icon, name)
		}
	}
}
