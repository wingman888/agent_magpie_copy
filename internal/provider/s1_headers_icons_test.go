package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// s1Env points magpie's config, cache and home at a fresh temp dir so the
// tests never read or write the real ones.
func s1Env(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

var s1PNG = append([]byte("\x89PNG\r\n\x1a\n"), []byte("s1-png-body")...)

func TestS1StoreIconNamesAndReuses(t *testing.T) {
	s1Env(t)
	ref, err := StoreIcon(s1PNG)
	if err != nil {
		t.Fatal(err)
	}
	name, ok := strings.CutPrefix(ref, "file:")
	if !ok || !regexp.MustCompile(`^[0-9a-f]{16}\.png$`).MatchString(name) {
		t.Fatalf("icon value %q is not file:<16 hex>.png", ref)
	}
	again, err := StoreIcon(s1PNG)
	if err != nil || again != ref {
		t.Fatalf("second store: %q, %v; want %q", again, err, ref)
	}
	entries, _ := os.ReadDir(IconDir())
	if len(entries) != 1 {
		t.Fatalf("same picture stored %d times", len(entries))
	}
	path := IconFile(name)
	if path == "" || filepath.Dir(path) != IconDir() {
		t.Fatalf("IconFile(%q) = %q", name, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "../" + name, "sub/" + name, "0123456789abcdef.exe",
		"0123456789ABCDEF.png", "short.png", "file:" + name, "0123456789abcdef.svgz"} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
}

func TestS1StoreIconRejects(t *testing.T) {
	s1Env(t)
	cases := map[string][]byte{
		"empty":   nil,
		"too big": append(append([]byte{}, s1PNG...), make([]byte, MaxIcon)...),
		"text":    []byte("just some words"),
		"html":    []byte("<html><body>hi</body></html>"),
	}
	for name, data := range cases {
		if ref, err := StoreIcon(data); err == nil {
			t.Errorf("%s: stored as %q", name, ref)
		}
	}
}

func TestS1StoreIconFormats(t *testing.T) {
	s1Env(t)
	cases := map[string][]byte{
		"png":  s1PNG,
		"jpg":  {0xFF, 0xD8, 0xFF, 0xE0, 1, 2},
		"gif":  []byte("GIF89a-s1"),
		"webp": []byte("RIFF\x10\x00\x00\x00WEBPVP8 "),
		"ico":  {0, 0, 1, 0, 1, 0},
		"svg":  []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`),
		"svg2": []byte(`<svg/>`),
	}
	for ext, data := range cases {
		want := ext
		if ext == "svg2" {
			want = "svg"
		}
		ref, err := StoreIcon(data)
		if err != nil || !strings.HasSuffix(ref, "."+want) {
			t.Errorf("%s: %q, %v", ext, ref, err)
		}
	}
}

func TestS1IconsPrunedOnSave(t *testing.T) {
	s1Env(t)
	kept, err := StoreIcon(s1PNG)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := StoreIcon([]byte("GIF89a-stale"))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := StoreIcon([]byte("GIF89a-fresh"))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, ref := range []string{kept, stale} {
		name := strings.TrimPrefix(ref, "file:")
		if err := os.Chtimes(IconFile(name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(Provider{ID: "s1-acme", Name: "S1 Acme", Chat: "https://acme.example/v1", Key: "k", Icon: kept}); err != nil {
		t.Fatal(err)
	}
	exists := func(ref string) bool {
		_, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:")))
		return err == nil
	}
	if !exists(kept) {
		t.Error("an old picture a provider still uses was deleted")
	}
	if exists(stale) {
		t.Error("an old unreferenced picture was kept")
	}
	if !exists(fresh) {
		t.Error("a picture within the grace hour was deleted")
	}
}

func TestS1SaveNormalizesHeadersAndCatalog(t *testing.T) {
	s1Env(t)
	err := Save(Provider{Name: "S1 Relay", Chat: "https://relay.example/v1", Key: "k",
		Headers: map[string]string{" X-Org-Id ": " acme ", "  ": "dropped"},
		Catalog: " OpenAI, deepseek,,"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find("s1-relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 1 || p.Headers["X-Org-Id"] != "acme" {
		t.Fatalf("headers: %#v", p.Headers)
	}
	if p.Catalog != "openai,deepseek" {
		t.Fatalf("catalog: %q", p.Catalog)
	}
	if err := Save(Provider{Name: "S1 Empty", Chat: "https://empty.example/v1", Key: "k",
		Headers: map[string]string{" ": "x"}}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("s1-empty")
	if err != nil {
		t.Fatal(err)
	}
	if q.Headers != nil {
		t.Fatalf("empty header map kept: %#v", q.Headers)
	}
	b, _ := os.ReadFile(Path())
	if n := strings.Count(string(b), `"headers"`); n != 1 {
		t.Fatalf("providers.json has %d headers fields, want 1", n)
	}
}

func TestS1HeadersSignedOnlyForCustom(t *testing.T) {
	s1Env(t)
	hdrs := map[string]string{"X-Org-Id": "acme", "authorization": "Bearer other"}
	ctx := context.Background()

	custom := Provider{ID: "s1c", Name: "S1 C", Chat: "https://c.example/v1", Key: "k", Headers: hdrs}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://c.example/v1/chat/completions", nil)
	if err := custom.Sign(ctx, req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header["X-Org-Id"]; len(got) != 1 || got[0] != "acme" {
		t.Fatalf("custom X-Org-Id: %#v", got)
	}
	if got := req.Header["authorization"]; len(got) != 1 || got[0] != "Bearer other" {
		t.Fatalf("custom authorization should override the key's: %#v", got)
	}
	if _, ok := req.Header["Authorization"]; ok {
		t.Fatal("the default Authorization header was not replaced")
	}

	preset, err := FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	preset.Key, preset.Headers = "k", hdrs
	req2, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.deepseek.com/v1/chat/completions", nil)
	if err := preset.Sign(ctx, req2, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := req2.Header["X-Org-Id"]; ok {
		t.Fatal("a preset sent custom headers")
	}

	acct := Provider{ID: "s1acct", Name: "S1 Acct", Account: &Account{}, Headers: hdrs}
	req3, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://acct.example/v1", nil)
	if err := acct.Sign(ctx, req3, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := req3.Header["X-Org-Id"]; ok {
		t.Fatal("a signed-in account sent custom headers")
	}
}

// s1Serve answers one HTTP/1.1 request on conn with a small model list and
// reports the request's header lines exactly as they arrived.
func s1Serve(conn net.Conn, seen chan<- []string) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		lines = append(lines, line)
	}
	n := 0
	for _, l := range lines {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			n, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	_, _ = io.CopyN(io.Discard, r, int64(n))
	seen <- lines
	body := `{"data":[{"id":"s1-model"}],"choices":[]}`
	fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

func TestS1HeaderCaseOnTheWire(t *testing.T) {
	s1Env(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback listener:", err)
	}
	defer ln.Close()
	seen := make(chan []string, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s1Serve(conn, seen)
		}
	}()

	p := Provider{ID: "s1wire", Name: "S1 Wire", Chat: "http://" + ln.Addr().String() + "/v1", Key: "k",
		Headers: map[string]string{"X-Org-Id": "acme", "x-gateway-Auth": "g"}}
	res := p.Test(context.Background())
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("probe: %+v", res)
	}
	// the model list fetch, then the probe
	for i := 0; i < 2; i++ {
		select {
		case lines := <-seen:
			has := func(want string) bool {
				for _, l := range lines {
					if l == want {
						return true
					}
				}
				return false
			}
			if !has("X-Org-Id: acme") || !has("x-gateway-Auth: g") {
				t.Fatalf("request %d headers: %q", i, lines)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no request reached the server")
		}
	}
}

func TestS1MultiCatalogNamesAndPrefix(t *testing.T) {
	s1Env(t)
	cache := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "magpie", "models.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{
	  "openai": {"id": "openai", "name": "OpenAI", "env": [], "models": {
	    "gpt-5.5": {"id": "gpt-5.5", "name": "GPT-5.5", "release_date": "2026-04-23",
	      "reasoning_options": [{"type": "effort", "values": ["low", "high"]}]}}},
	  "deepseek": {"id": "deepseek", "name": "DeepSeek", "env": [], "models": {
	    "deepseek-chat": {"id": "deepseek-chat", "name": "DeepSeek Chat", "release_date": "2025-01-01"}}}
	}`
	if err := os.WriteFile(cache, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()

	p := Provider{ID: "s1gw", Name: "S1 GW", Chat: "https://gw.example/v1", Key: "k",
		Catalog: "openai,deepseek", Models: []string{"deepseek/deepseek-chat", "gpt-5.5"}}
	got := p.Exposed()
	if len(got) != 2 {
		t.Fatalf("exposed: %+v", got)
	}
	if got[0].ID != "deepseek/deepseek-chat" || got[0].Name != "DeepSeek Chat" {
		t.Fatalf("prefixed model: %+v", got[0])
	}
	if got[1].ID != "gpt-5.5" || got[1].Name != "GPT-5.5" || len(got[1].Efforts) != 2 || got[1].Efforts[0] != "low" {
		t.Fatalf("gpt-5.5 from the second catalog: %+v", got[1])
	}
}
