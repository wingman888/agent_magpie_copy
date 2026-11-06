package main

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// chHeadersIsolate keeps config, cache and home in a temp dir.
func chHeadersIsolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// chHeadersCapture returns what fn writes to stdout.
func chHeadersCapture(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	w.Close()
	os.Stdout = old
	return chAnsi.ReplaceAllString(<-done, "")
}

var chAnsi = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestChHeaderPairsParse(t *testing.T) {
	var p provider.Provider
	err := applyPairs(&p, []string{
		"url=https://relay.example/v1",
		"key=sk-1",
		"header.X-Org-Id=acme",
		"HEADER.X-ORG-id =  padded  ",
		"header.X-Org-Id=second",
		"Header.=dropped",
		"header.   =dropped too",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-Org-Id": "second", "X-ORG-id": "padded"}
	if len(p.Headers) != len(want) {
		t.Fatalf("headers = %v, want %v", p.Headers, want)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("header %q = %q, want %q", k, p.Headers[k], v)
		}
	}
	if p.Chat != "https://relay.example/v1" || p.Key != "sk-1" {
		t.Errorf("other keys disturbed: chat=%q key=%q", p.Chat, p.Key)
	}
}

func TestChHeaderPairsUnknownKeyStillFails(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{"headr.X-Foo=1"}); err == nil {
		t.Fatal("misspelled key accepted")
	}
}

func TestChHeaderShowFormatAndOrder(t *testing.T) {
	chHeadersIsolate(t)
	p := provider.Provider{
		ID:      "chshow",
		Name:    "Show",
		Chat:    "https://relay.example/v1",
		Key:     "sk-1",
		Headers: map[string]string{"X-Org-Id": "acme", "Authorization": "Bearer x", "X-ORG-id": "padded"},
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	saved, err := provider.Find("chshow")
	if err != nil {
		t.Fatal(err)
	}
	out := chHeadersCapture(t, func() { showProvider(*saved) })
	lines := []string{"header  Authorization: Bearer x", "header  X-ORG-id: padded", "header  X-Org-Id: acme"}
	last := -1
	for _, l := range lines {
		i := strings.Index(out, l)
		if i < 0 {
			t.Fatalf("missing %q in:\n%s", l, out)
		}
		if i < last {
			t.Errorf("%q out of order in:\n%s", l, out)
		}
		last = i
	}
}
