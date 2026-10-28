package main_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

type cli struct {
	bin  string
	home string
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "magpie")
	if out, err := exec.Command("go", "build", "-tags", "nogui", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return &cli{bin: bin, home: home}
}

func (c *cli) run(args ...string) (string, error) {
	cmd := exec.Command(c.bin, args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAcceptCLIHeaders(t *testing.T) {
	c := newCLI(t)
	var mu sync.Mutex
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()

	out, err := c.run("provider", "add", "My Relay", "url="+srv.URL+"/v1", "key=sk-1", "header.X-Foo=bar", "header.x-id=1", "Header.X-A=2")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	p, err := provider.Find("my-relay")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-Foo": "bar", "x-id": "1", "X-A": "2"}
	if len(p.Headers) != 3 {
		t.Fatalf("headers = %#v", p.Headers)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("headers[%q] = %q", k, p.Headers[k])
		}
	}
	mu.Lock()
	ok := false
	for _, h := range seen {
		if len(h["x-id"]) == 1 && h["x-id"][0] == "1" {
			ok = true
		}
	}
	mu.Unlock()
	if !ok {
		t.Errorf("model fetch during add lacked custom header: %v", seen)
	}

	out, err = c.run("provider", "my-relay")
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	a, b, d := strings.Index(out, "X-A: 2"), strings.Index(out, "X-Foo: bar"), strings.Index(out, "x-id: 1")
	if a < 0 || b < 0 || d < 0 || !(a < b && b < d) {
		t.Errorf("header lines missing or unsorted (%d %d %d):\n%s", a, b, d, out)
	}

	out, err = c.run("provider", "add", "Bad", "url=https://bad.example.com/v1", "key=sk-1", "header.=x")
	if err == nil || !strings.Contains(out, "unknown field") {
		t.Errorf("bare header. : err=%v out=%s", err, out)
	}

	out, _ = c.run("provider")
	if !strings.Contains(out, "header.X-Foo") || !strings.Contains(out, "magpie provider icon <id>") {
		t.Errorf("usage lacks header/icon docs:\n%s", out)
	}
}

func TestAcceptCLIIcon(t *testing.T) {
	c := newCLI(t)
	pngPath := filepath.Join(t.TempDir(), "logo.png")
	os.WriteFile(pngPath, pngBytes, 0o644)
	want, err := provider.StoreIcon(pngBytes)
	if err != nil {
		t.Fatal(err)
	}

	if out, err := c.run("provider", "add", "Relay", "url=https://relay.example.com/v1", "key=sk-1", "icon="+pngPath); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	p, _ := provider.Find("relay")
	if p.Icon != want {
		t.Fatalf("icon = %q, want %q", p.Icon, want)
	}
	if !regexp.MustCompile(`^file:[0-9a-f]{16}\.png$`).MatchString(p.Icon) {
		t.Errorf("icon = %q", p.Icon)
	}
	if _, err := os.Stat(provider.IconFile(strings.TrimPrefix(p.Icon, "file:"))); err != nil {
		t.Error(err)
	}

	out, err := c.run("provider", "icon", "relay", "deepseek-color")
	if err != nil || !strings.Contains(out, "✓") || !strings.Contains(out, "Relay icon deepseek-color") {
		t.Errorf("icon builtin: %v %q", err, out)
	}
	if p, _ = provider.Find("relay"); p.Icon != "deepseek-color" {
		t.Errorf("icon = %q", p.Icon)
	}

	out, err = c.run("provider", "icon", "relay", pngPath)
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if p, _ = provider.Find("relay"); p.Icon != want {
		t.Errorf("icon = %q", p.Icon)
	}

	if out, err = c.run("provider", "icon", "relay", ""); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if p, _ = provider.Find("relay"); p.Icon != "generic" {
		t.Errorf("icon = %q", p.Icon)
	}

	out, err = c.run("provider", "icon", "relay")
	if err == nil || !strings.Contains(out, `magpie provider icon <id> <picture file | built-in name | "">`) {
		t.Errorf("usage: %v %s", err, out)
	}

	if out, err = c.run("provider", "add", "deepseek", "sk-1"); err != nil {
		t.Skipf("preset add failed: %v %s", err, out)
	}
	out, err = c.run("provider", "icon", "deepseek", "generic")
	if err == nil || !strings.Contains(out, "has its own icon; only a custom provider takes one") {
		t.Errorf("preset icon: %v %s", err, out)
	}
}
