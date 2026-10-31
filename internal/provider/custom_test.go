package provider

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func isolate(t *testing.T) {
	t.Helper()
	d := t.TempDir()
	t.Setenv("HOME", d)
	t.Setenv("XDG_CONFIG_HOME", d)
	t.Setenv("XDG_CACHE_HOME", d)
}

func rawRequest(t *testing.T, p Provider) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- ""
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		var sb strings.Builder
		for {
			line, err := r.ReadString('\n')
			sb.WriteString(line)
			if err != nil || line == "\r\n" {
				break
			}
		}
		c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}"))
		got <- sb.String()
	}()
	req, _ := http.NewRequest("POST", "http://"+ln.Addr().String()+"/x", bytes.NewReader(nil))
	if err := p.Sign(req.Context(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return <-got
}

func TestCustomHeadersKeepCaseAndOverrideAuth(t *testing.T) {
	isolate(t)
	p := Provider{ID: "r", Key: "sk-1", Chat: "http://x", Headers: map[string]string{"X-ORG-id": "acme", "Authorization": "Token mine"}}
	raw := rawRequest(t, p)
	if !strings.Contains(raw, "\r\nX-ORG-id: acme\r\n") {
		t.Errorf("header case lost:\n%s", raw)
	}
	if !strings.Contains(raw, "\r\nAuthorization: Token mine\r\n") || strings.Contains(raw, "Bearer") {
		t.Errorf("Authorization not overridden:\n%s", raw)
	}
}

func TestPresetAndAccountIgnoreHeaders(t *testing.T) {
	isolate(t)
	h := map[string]string{"X-Org-Id": "acme"}
	if raw := rawRequest(t, Provider{ID: "d", Preset: "deepseek", Key: "k", Chat: "http://x", Headers: h}); strings.Contains(raw, "X-Org-Id") {
		t.Errorf("preset sent custom header")
	}
	if raw := rawRequest(t, Provider{ID: "a", Key: "k", Chat: "http://x", Headers: h, Account: &Account{}}); strings.Contains(raw, "X-Org-Id") {
		t.Errorf("account sent custom header")
	}
	if got := normalize(Provider{Preset: "deepseek", Headers: h}).Headers; got != nil {
		t.Errorf("preset kept headers: %v", got)
	}
}

func TestHeadersSavedTrimmed(t *testing.T) {
	isolate(t)
	err := Save(Provider{Name: "Relay", Chat: "http://relay.test/v1", Key: "k",
		Headers: map[string]string{" X-A ": "  v  ", "  ": "drop", "": "drop"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 1 || p.Headers["X-A"] != "v" {
		t.Errorf("headers = %v", p.Headers)
	}
	if err := Save(Provider{Name: "Plain", Chat: "http://p.test/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Count(string(b), `"headers"`) != 1 {
		t.Errorf("headers should be written only when set:\n%s", b)
	}
}

var png = []byte("\x89PNG\r\n\x1a\nrest")

func TestIconFileRejectsTraversal(t *testing.T) {
	isolate(t)
	for _, n := range []string{"../providers.json", "x.png", "0123456789abcdef.exe", "", "../icons/0123456789abcdef.png", "0123456789ABCDEF.png"} {
		if got := IconFile(n); got != "" {
			t.Errorf("IconFile(%q) = %q", n, got)
		}
	}
	ref, err := StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") {
		t.Fatalf("ref = %q", ref)
	}
	b, err := os.ReadFile(IconFile(strings.TrimPrefix(ref, "file:")))
	if err != nil || !bytes.Equal(b, png) {
		t.Errorf("read back: %v %q", err, b)
	}
	if again, _ := StoreIcon(png); again != ref {
		t.Errorf("same bytes gave %q and %q", ref, again)
	}
	ents, _ := os.ReadDir(filepath.Dir(IconFile(strings.TrimPrefix(ref, "file:"))))
	if len(ents) != 1 {
		t.Errorf("want one file, got %d", len(ents))
	}
}

func TestStoreIconValidation(t *testing.T) {
	isolate(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty accepted")
	}
	if _, err := StoreIcon([]byte("hello world")); err == nil {
		t.Error("text accepted")
	}
	if _, err := StoreIcon(append(append([]byte{}, png...), make([]byte, MaxIcon)...)); err == nil || !strings.Contains(err.Error(), "smaller") {
		t.Errorf("big accepted: %v", err)
	}
	for _, s := range []string{
		"<svg xmlns='http://www.w3.org/2000/svg'/>",
		"\xef\xbb\xbf<?xml version='1.0'?>\n<!-- c -->\n<!DOCTYPE svg>\n<svg width='1'></svg>",
	} {
		ref, err := StoreIcon([]byte(s))
		if err != nil || !strings.HasSuffix(ref, ".svg") {
			t.Errorf("svg %q: %q %v", s, ref, err)
		}
	}
	for _, c := range [][]byte{[]byte("\xff\xd8\xff\xe0x"), []byte("GIF89a.."), []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), {0, 0, 1, 0, 1, 0, 0}} {
		if _, err := StoreIcon(c); err != nil {
			t.Errorf("%q: %v", c, err)
		}
	}
	f := filepath.Join(t.TempDir(), "a.png")
	os.WriteFile(f, []byte("plain text"), 0o644)
	if _, err := StoreIconFile(f); err == nil {
		t.Error("text file accepted")
	}
}

func TestSetIcon(t *testing.T) {
	isolate(t)
	if err := Save(Provider{Name: "Relay", Chat: "http://r.test/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{Name: "DS", Preset: "deepseek", Chat: "http://d.test/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "logo.png")
	os.WriteFile(f, png, 0o644)
	ref, err := SetIcon("relay", f)
	if err != nil || !strings.HasPrefix(ref, "file:") {
		t.Fatalf("%q %v", ref, err)
	}
	if ref2, _ := SetIcon("relay", f); ref2 != ref {
		t.Error("not stable")
	}
	bad := filepath.Join(t.TempDir(), "bad.txt")
	os.WriteFile(bad, []byte("nope"), 0o644)
	if _, err := SetIcon("relay", bad); err == nil {
		t.Error("text accepted")
	}
	if p, _ := Find("relay"); p.Icon != ref {
		t.Errorf("icon changed on failure: %q", p.Icon)
	}
	if got, err := SetIcon("relay", "deepseek-color"); err != nil || got != "deepseek-color" {
		t.Errorf("%q %v", got, err)
	}
	if got, err := SetIcon("relay", ""); err != nil || got != "" {
		t.Errorf("%q %v", got, err)
	}
	if _, err := SetIcon("ds", "x"); err == nil || !strings.Contains(err.Error(), "custom") {
		t.Errorf("preset: %v", err)
	}
}

func TestIconSweep(t *testing.T) {
	isolate(t)
	if err := Save(Provider{Name: "Relay", Chat: "http://r.test/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "logo.png")
	os.WriteFile(f, png, 0o644)
	ref, _ := SetIcon("relay", f)
	path := IconFile(strings.TrimPrefix(ref, "file:"))
	exists := func() bool { _, err := os.Stat(path); return err == nil }

	// change the icon: the old image is under an hour old, so it stays
	SetIcon("relay", "other")
	if !exists() {
		t.Fatal("fresh image swept")
	}
	// two hours on, the next save sweeps it
	real := now
	defer func() { now = real }()
	now = func() time.Time { return real().Add(2 * time.Hour) }
	if err := Save(Provider{Name: "Relay", Chat: "http://r.test/v1", Key: "k", Icon: "other"}); err != nil {
		t.Fatal(err)
	}
	if exists() {
		t.Error("old unreferenced image kept")
	}
	// a referenced image is never swept
	ref, _ = SetIcon("relay", f)
	path = IconFile(strings.TrimPrefix(ref, "file:"))
	now = func() time.Time { return real().Add(5 * time.Hour) }
	Save(Provider{Name: "Other", Chat: "http://o.test/v1", Key: "k"})
	if !exists() {
		t.Error("referenced image swept")
	}
	// deleting the provider frees it
	Delete("relay")
	if exists() {
		t.Error("image of deleted provider kept")
	}
}
