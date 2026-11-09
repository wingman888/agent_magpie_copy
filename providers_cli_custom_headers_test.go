package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestCustomHdrsApplyPairs(t *testing.T) {
	var p provider.Provider
	err := applyPairs(&p, []string{
		"url=https://relay.example.com/v1",
		"HEADER.X-ORG-id =  padded  ",
		"header.X-Org-Id=acme",
		"header.=dropped",
		"header.   =dropped too",
		"Header.Authorization=Token abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-ORG-id": "padded", "X-Org-Id": "acme", "Authorization": "Token abc"}
	if len(p.Headers) != len(want) {
		t.Fatalf("headers = %v, want %v", p.Headers, want)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("header %q = %q, want %q", k, p.Headers[k], v)
		}
	}
	if p.Chat != "https://relay.example.com/v1" {
		t.Errorf("url = %q", p.Chat)
	}
}

func TestCustomHdrsShowProviderLines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)

	p := provider.Provider{ID: "acme", Name: "Acme", Chat: "https://relay.example.com/v1", Key: "sk-test",
		Headers: map[string]string{"X-Org-Id": "acme", "X-ORG-id": "other", "Authorization": "Token abc"}}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	showErr := showProvider(p)
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)
	r.Close()
	if showErr != nil {
		t.Fatal(showErr)
	}

	text := string(out)
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "header  ") {
			lines = append(lines, l)
		}
	}
	want := []string{"header  Authorization: Token abc", "header  X-ORG-id: other", "header  X-Org-Id: acme"}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("header lines = %q\nfull output:\n%s", lines, text)
	}
}
