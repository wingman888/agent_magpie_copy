package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func cliHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

func capture(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	err := f()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b), err
}

func TestApplyPairsHeaders(t *testing.T) {
	cliHome(t)
	p := provider.Provider{Name: "R"}
	err := applyPairs(&p, []string{"header.X-Org-Id=acme", "HEADER.X-ORG-id =  padded  ", "header.=dropped", "Header.  =x", " url = https://r.example/v1 "})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 2 || p.Headers["X-Org-Id"] != "acme" || p.Headers["X-ORG-id"] != "padded" || p.Chat != "https://r.example/v1" {
		t.Fatalf("%#v", p)
	}
}

func TestShowProviderHeadersSorted(t *testing.T) {
	cliHome(t)
	p := provider.Provider{ID: "r", Name: "R", Chat: "https://r.example/v1", Key: "k",
		Headers: map[string]string{"X-Org-Id": "a", "X-ORG-id": "b", "Authorization": "c"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	saved, _ := provider.Find("r")
	out, err := capture(t, func() error { return showProvider(*saved) })
	if err != nil {
		t.Fatal(err)
	}
	a := strings.Index(out, "header  Authorization: c")
	b := strings.Index(out, "header  X-ORG-id: b")
	c := strings.Index(out, "header  X-Org-Id: a")
	if a < 0 || b < a || c < b {
		t.Fatalf("order %d %d %d in\n%s", a, b, c, out)
	}
}

func TestProviderIconCmd(t *testing.T) {
	home := cliHome(t)
	if err := provider.Save(provider.Provider{ID: "r", Name: "R", Chat: "https://r.example/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	icon := func() string { p, _ := provider.Find("r"); return p.Icon }
	logo := filepath.Join(home, "logo.png")
	os.WriteFile(logo, []byte("\x89PNG\r\n\x1a\nxx"), 0o644)
	if _, err := capture(t, func() error { return providerCmd([]string{"provider", "icon", "r", logo}) }); err != nil {
		t.Fatal(err)
	}
	ref := icon()
	if !strings.HasPrefix(ref, "file:") {
		t.Fatalf("icon %q", ref)
	}
	capture(t, func() error { return providerCmd([]string{"provider", "icon", "r", logo}) })
	if icon() != ref {
		t.Fatalf("second ref %q", icon())
	}
	entries, _ := os.ReadDir(filepath.Join(home, ".config", "magpie", "icons"))
	if len(entries) != 1 {
		t.Fatalf("%d icons", len(entries))
	}
	// refused images leave the icon alone
	txt := filepath.Join(home, "notes.txt")
	os.WriteFile(txt, []byte("hello"), 0o644)
	empty := filepath.Join(home, "empty.png")
	os.WriteFile(empty, nil, 0o644)
	big := filepath.Join(home, "big.png")
	os.WriteFile(big, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, provider.MaxIcon)...), 0o644)
	for _, bad := range []string{txt, empty, big, filepath.Join(home, "missing.png"), "./nope/logo.png"} {
		if _, err := capture(t, func() error { return providerCmd([]string{"provider", "icon", "r", bad}) }); err == nil {
			t.Errorf("%s accepted", bad)
		}
		if icon() != ref {
			t.Fatalf("%s changed icon to %q", bad, icon())
		}
	}
	if _, err := capture(t, func() error { return providerCmd([]string{"provider", "icon", "r", "deepseek-color"}) }); err != nil || icon() != "deepseek-color" {
		t.Fatalf("builtin: %v %q", err, icon())
	}
	if _, err := capture(t, func() error { return providerCmd([]string{"provider", "icon", "r", ""}) }); err != nil || icon() != "" {
		t.Fatalf("reset: %v %q", err, icon())
	}
	b, _ := os.ReadFile(provider.Path())
	if strings.Contains(string(b), `"icon"`) {
		t.Fatalf("icon written after reset: %s", b)
	}
}

func TestProviderIconOnlyCustom(t *testing.T) {
	cliHome(t)
	pr, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Skip("no deepseek preset")
	}
	pr.Key = "k"
	if err := provider.Save(pr); err != nil {
		t.Fatal(err)
	}
	before, _ := provider.Find(pr.ID)
	_, err = capture(t, func() error { return providerCmd([]string{"provider", "icon", pr.ID, "generic"}) })
	if err == nil || !strings.Contains(err.Error(), "custom") {
		t.Fatalf("err %v", err)
	}
	after, _ := provider.Find(pr.ID)
	if after.Icon != before.Icon {
		t.Fatalf("icon %q → %q", before.Icon, after.Icon)
	}
}

func TestUsageMentionsHeadersAndIcon(t *testing.T) {
	for _, s := range []string{"header.X-Foo", "icon", "provider icon"} {
		if !strings.Contains(providerUsage, s) {
			t.Errorf("usage lacks %q", s)
		}
	}
}
