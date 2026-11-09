package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// hdrS1Stdout captures what f prints to stdout.
func hdrS1Stdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	ferr := f()
	os.Stdout = old
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b), ferr
}

func TestHdrS1ApplyPairsHeaders(t *testing.T) {
	var p provider.Provider
	pairs := []string{
		"url=https://relay.example.com/v1",
		"key=sk-1",
		"HEADER.X-ORG-id =  padded  ",
		"header.X-Org-Id=acme",
		"header.X-Org-Id=acme2",
		"header.   =dropped",
		"header.=dropped too",
	}
	if err := applyPairs(&p, pairs); err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://relay.example.com/v1" || p.Key != "sk-1" {
		t.Fatalf("url/key not applied: %+v", p)
	}
	want := map[string]string{"X-ORG-id": "padded", "X-Org-Id": "acme2"}
	if len(p.Headers) != len(want) {
		t.Fatalf("headers = %v, want %v", p.Headers, want)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("header %q = %q, want %q", k, p.Headers[k], v)
		}
	}
}

func TestHdrS1ShowProviderHeaderLines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)
	p := provider.Provider{ID: "hdrs1-show", Name: "Show", Chat: "https://relay.example.com/v1", Key: "sk",
		Headers: map[string]string{"X-Org-Id": "acme", "X-ORG-id": "padded", "Authorization": "Bearer u"}}
	out, err := hdrS1Stdout(t, func() error { return showProvider(p) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "header  X-Org-Id: acme") {
		t.Fatalf("missing header line:\n%s", out)
	}
	order := []string{"header  Authorization: Bearer u", "header  X-ORG-id: padded", "header  X-Org-Id: acme"}
	last := -1
	for _, line := range order {
		i := strings.Index(out, line)
		if i < 0 {
			t.Fatalf("missing %q:\n%s", line, out)
		}
		if i < last {
			t.Fatalf("header lines out of byte order:\n%s", out)
		}
		last = i
	}
}
