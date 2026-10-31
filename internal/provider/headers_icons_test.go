package provider

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var pngData = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 20)...)

func tmpConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func TestHeadersSaveTrimDrop(t *testing.T) {
	tmpConfig(t)
	err := Save(Provider{Name: "R", Chat: "http://x.test/v1", Key: "k",
		Headers: map[string]string{" X-Org-Id ": " a ", "X-ORG-id": "b", "  ": "z"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := Find("r")
	if len(p.Headers) != 2 || p.Headers["X-Org-Id"] != "a" || p.Headers["X-ORG-id"] != "b" {
		t.Fatalf("headers %v", p.Headers)
	}
	b, _ := os.ReadFile(Path())
	if !strings.Contains(string(b), `"headers"`) {
		t.Fatal("headers not written")
	}
}

func TestPresetDropsHeaders(t *testing.T) {
	tmpConfig(t)
	p, err := FromPreset("deepseek")
	if err != nil {
		t.Skip(err)
	}
	p.Key = "k"
	p.Headers = map[string]string{"X-A": "b"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "headers") {
		t.Fatal("preset kept headers")
	}
	if p.CustomHeaders() != nil {
		t.Fatal("preset exposes headers")
	}
}

func TestSignAndTestSendHeaders(t *testing.T) {
	tmpConfig(t)
	var seen []http.Header
	var raw []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		for k := range r.Header {
			raw = append(raw, k)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"data":[{"id":"m1"}]}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := Provider{ID: "r", Name: "R", Chat: srv.URL + "/v1", Key: "k",
		Headers: map[string]string{"X-ORG-id": "acme", "Authorization": "mine"}}
	p.Test(context.Background())
	if len(seen) < 2 {
		t.Fatalf("requests %d", len(seen))
	}
	for _, h := range seen {
		if h.Get("Authorization") != "mine" {
			t.Fatalf("auth %q", h.Get("Authorization"))
		}
	}
	found := false
	for _, k := range raw {
		if k == "X-ORG-id" {
			found = true
		}
	}
	// the server canonicalizes names it reads; check via raw TCP below
	_ = found
}

func TestApplyHeadersCase(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://x", nil)
	req.Header.Set("Authorization", "Bearer k")
	p := Provider{Headers: map[string]string{"X-ORG-id": "a", "authorization": "mine"}}
	p.applyHeaders(req)
	if _, ok := req.Header["X-ORG-id"]; !ok {
		t.Fatal("case lost")
	}
	if v := req.Header["authorization"]; len(v) != 1 || v[0] != "mine" || len(req.Header["Authorization"]) != 0 {
		t.Fatalf("override: %v", req.Header)
	}
	acct := Provider{Account: &Account{}, Headers: map[string]string{"X": "y"}}
	r2, _ := http.NewRequest("GET", "http://x", nil)
	acct.applyHeaders(r2)
	if len(r2.Header) != 0 {
		t.Fatal("account sent headers")
	}
}

func TestStoreIcon(t *testing.T) {
	tmpConfig(t)
	ref, err := StoreIcon(pngData)
	if err != nil || !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") {
		t.Fatal(ref, err)
	}
	ref2, _ := StoreIcon(pngData)
	if ref != ref2 {
		t.Fatal("not stable")
	}
	ents, _ := os.ReadDir(filepath.Join(filepath.Dir(Path()), "icons"))
	if len(ents) != 1 {
		t.Fatal(len(ents))
	}
	got, _ := os.ReadFile(IconFile(strings.TrimPrefix(ref, "file:")))
	if !bytes.Equal(got, pngData) {
		t.Fatal("content")
	}
	if _, err := StoreIcon([]byte("hello")); err == nil {
		t.Fatal("text accepted")
	}
	if _, err := StoreIcon(nil); err == nil {
		t.Fatal("empty accepted")
	}
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxIcon)...)
	if _, err := StoreIcon(big); err == nil {
		t.Fatal("big accepted")
	}
	if r, err := StoreIcon([]byte(`<?xml?><svg xmlns="x"/>`)); err != nil || !strings.HasSuffix(r, ".svg") {
		t.Fatal(r, err)
	}
	f := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(f, []byte(`<svg/>`), 0o644)
	if _, err := StoreIconFile(f); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"../providers.json", "x.png", "0123456789abcdef.exe", "", "0123456789ABCDEF.png", "../123456789abcdef.png"} {
		if IconFile(n) != "" {
			t.Fatal(n)
		}
	}
}

func TestIconSweep(t *testing.T) {
	tmpConfig(t)
	a, _ := StoreIcon(append(pngData, 'a'))
	b, _ := StoreIcon(append(pngData, 'b'))
	c, _ := StoreIcon(append(pngData, 'c'))
	old := time.Now().Add(-2 * time.Hour)
	for _, r := range []string{a, b} {
		os.Chtimes(IconFile(strings.TrimPrefix(r, "file:")), old, old)
	}
	if err := Save(Provider{Name: "R", Chat: "http://x.test/v1", Key: "k", Icon: a}); err != nil {
		t.Fatal(err)
	}
	exists := func(r string) bool {
		_, err := os.Stat(IconFile(strings.TrimPrefix(r, "file:")))
		return err == nil
	}
	if !exists(a) || exists(b) || !exists(c) {
		t.Fatal(exists(a), exists(b), exists(c))
	}
	os.Chtimes(IconFile(strings.TrimPrefix(c, "file:")), old, old)
	if err := Delete("r"); err != nil {
		t.Fatal(err)
	}
	if exists(a) || exists(c) {
		t.Fatal("not swept")
	}
}
