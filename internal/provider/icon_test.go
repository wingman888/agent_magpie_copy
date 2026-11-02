package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func tempConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

var pngData = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 40)...)

func TestStoreIcon(t *testing.T) {
	tempConfig(t)
	ref, err := StoreIcon(pngData)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") || len(ref) != len("file:")+16+4 {
		t.Fatalf("ref %q", ref)
	}
	again, err := StoreIcon(pngData)
	if err != nil || again != ref {
		t.Fatalf("again %q %v", again, err)
	}
	entries, _ := os.ReadDir(filepath.Join(filepath.Dir(Path()), "icons"))
	if len(entries) != 1 {
		t.Fatalf("%d files in icons", len(entries))
	}
	f := IconFile(strings.TrimPrefix(ref, "file:"))
	if b, err := os.ReadFile(f); err != nil || !bytes.Equal(b, pngData) {
		t.Fatalf("IconFile content: %v", err)
	}
	for _, bad := range []string{"../providers.json", "x.png", "0123456789abcdef.exe", "", "0123456789ABCDEF.png", "../0123456789abcdef.png"} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q", bad, got)
		}
	}

	types := map[string][]byte{
		".jpg":  {0xFF, 0xD8, 0xFF, 0xE0, 1, 2},
		".gif":  []byte("GIF89a...."),
		".webp": []byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
		".ico":  {0, 0, 1, 0, 1, 0},
		".svg":  []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`),
	}
	for ext, data := range types {
		ref, err := StoreIcon(data)
		if err != nil || !strings.HasSuffix(ref, ext) {
			t.Errorf("%s: %q %v", ext, ref, err)
		}
	}
	if _, err := StoreIcon([]byte("just some text")); err == nil {
		t.Error("text accepted")
	}
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty accepted")
	}
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxIcon)...)
	if _, err := StoreIcon(big); err == nil || !strings.Contains(err.Error(), "smaller") {
		t.Errorf("big: %v", err)
	}
	exact := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxIcon-8)...)
	if _, err := StoreIcon(exact); err != nil {
		t.Errorf("exactly 1 MB rejected: %v", err)
	}

	// an SVG in a .txt file is still an SVG
	txt := filepath.Join(t.TempDir(), "logo.txt")
	os.WriteFile(txt, types[".svg"], 0o644)
	if ref, err := StoreIconFile(txt); err != nil || !strings.HasSuffix(ref, ".svg") {
		t.Errorf("svg in txt: %q %v", ref, err)
	}
	bigFile := filepath.Join(t.TempDir(), "big.png")
	os.WriteFile(bigFile, big, 0o644)
	if _, err := StoreIconFile(bigFile); err == nil {
		t.Error("big file accepted")
	}
}

func TestIconSweep(t *testing.T) {
	tempConfig(t)
	keep, _ := StoreIcon(pngData)
	gone, _ := StoreIcon([]byte("<svg>old</svg>"))
	fresh, _ := StoreIcon([]byte("<svg>fresh</svg>"))
	old := time.Now().Add(-2 * time.Hour)
	for _, ref := range []string{keep, gone} {
		os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), old, old)
	}
	if err := Save(Provider{ID: "a", Name: "A", Key: "k", Chat: "https://a.example/v1", Icon: keep}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "b", Name: "B", Key: "k", Chat: "https://b.example/v1", Icon: "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	exists := func(ref string) bool {
		_, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:")))
		return err == nil
	}
	if !exists(keep) || exists(gone) || !exists(fresh) {
		t.Fatalf("after save: keep=%v gone=%v fresh=%v", exists(keep), exists(gone), exists(fresh))
	}
	// no longer used, and old: swept once its provider goes
	if err := Delete("a"); err != nil {
		t.Fatal(err)
	}
	if exists(keep) {
		t.Error("unused old icon kept after delete")
	}
	if !exists(fresh) {
		t.Error("fresh icon swept")
	}
}

func TestHeadersSaved(t *testing.T) {
	tempConfig(t)
	p := Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example/v1",
		Headers: map[string]string{" X-Org-Id ": "  acme ", "X-ORG-id": "x", "  ": "dropped"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-Org-Id": "acme", "X-ORG-id": "x"}
	if len(got.Headers) != 2 || got.Headers["X-Org-Id"] != "acme" || got.Headers["X-ORG-id"] != "x" {
		t.Fatalf("headers %v, want %v", got.Headers, want)
	}
	// a preset keeps none
	ds, _ := FromPreset("deepseek")
	ds.Key = "k"
	ds.Headers = map[string]string{"X-A": "b"}
	if err := Save(ds); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	var f file
	json.Unmarshal(b, &f)
	for _, p := range f.Providers {
		if p.Preset != "" && len(p.Headers) > 0 {
			t.Errorf("preset kept headers %v", p.Headers)
		}
	}
	if bytes.Count(b, []byte(`"headers"`)) != 1 {
		t.Errorf("headers written for a provider without any:\n%s", b)
	}
}

func TestSignKeepsHeaderCase(t *testing.T) {
	p := Provider{Key: "k", Chat: "https://relay.example/v1", Headers: map[string]string{"X-ORG-id": "acme", "authorization": "Token mine"}}
	req, _ := http.NewRequest("POST", "https://relay.example/v1/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if v := req.Header["X-ORG-id"]; len(v) != 1 || v[0] != "acme" {
		t.Errorf("X-ORG-id = %v; header %v", v, req.Header)
	}
	if _, ok := req.Header["X-Org-Id"]; ok {
		t.Error("name canonicalized")
	}
	if _, ok := req.Header["Authorization"]; ok {
		t.Error("key's Authorization kept alongside the user's")
	}
	if v := req.Header["authorization"]; len(v) != 1 || v[0] != "Token mine" {
		t.Errorf("authorization = %v", v)
	}
	acct := p
	acct.Account = &Account{}
	req2, _ := http.NewRequest("POST", "https://relay.example/v1/chat/completions", nil)
	acct.Sign(context.Background(), req2, Chat, nil)
	if _, ok := req2.Header["X-ORG-id"]; ok {
		t.Error("account sent custom header")
	}
}

// rawServer answers every request with 200 and a model list, recording the
// header lines exactly as they came over the wire.
func rawServer(t *testing.T) (string, func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var lines []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					req, err := http.ReadRequest(r)
					if err != nil {
						return
					}
					_ = req
					io.Copy(io.Discard, req.Body)
					body := `{"data":[{"id":"m1"}],"choices":[{"message":{"content":"hi"}}]}`
					io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "+itoa(len(body))+"\r\n\r\n"+body)
				}
			}(&recConn{Conn: c, add: func(s string) { mu.Lock(); lines = append(lines, s); mu.Unlock() }})
		}
	}()
	return "http://" + ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// recConn records the raw lines it reads.
type recConn struct {
	net.Conn
	add func(string)
	buf []byte
}

func (c *recConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.buf = append(c.buf, p[:n]...)
	for {
		i := bytes.Index(c.buf, []byte("\r\n"))
		if i < 0 {
			break
		}
		c.add(string(c.buf[:i]))
		c.buf = c.buf[i+2:]
	}
	return n, err
}

func TestHeadersOnWire(t *testing.T) {
	tempConfig(t)
	url, lines := rawServer(t)
	p := Provider{ID: "relay", Name: "Relay", Key: "k", Chat: url + "/v1", Models: []string{"m1"},
		Headers: map[string]string{"X-ORG-id": "acme"}}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	fetched := 0
	for _, l := range lines() {
		if l == "X-ORG-id: acme" {
			fetched++
		}
	}
	if fetched == 0 {
		t.Fatalf("model fetch did not send X-ORG-id as typed: %q", lines())
	}
	rs := p.Test(context.Background())
	if len(rs) != 1 || !rs[0].OK {
		t.Fatalf("test: %+v", rs)
	}
	n := 0
	for _, l := range lines() {
		if l == "X-ORG-id: acme" {
			n++
		}
		if strings.EqualFold(l, "X-ORG-id: acme") && l != "X-ORG-id: acme" {
			t.Errorf("canonicalized: %q", l)
		}
	}
	if n < fetched+2 { // Test fetches again, then probes
		t.Errorf("test did not send the header: %d lines of %q", n, lines())
	}
	_ = httptest.NewServer
}
