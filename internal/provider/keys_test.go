package provider

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func custom(t *testing.T, key string) {
	t.Helper()
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: key, Chat: "https://r.example/v1"}); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T) Provider {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

func TestKeyLifecycle(t *testing.T) {
	isolate(t)
	custom(t, "k1")
	if err := AddKey("relay", "team", "k2", Anthropic); err != nil {
		t.Fatal(err)
	}
	if AddKey("relay", "", "k2", "") == nil || AddKey("relay", "", "k3", "bogus") == nil {
		t.Fatal("duplicate / bad protocol accepted")
	}
	if err := SetKeyProtocol("relay", KeyRef("k1"), Chat); err != nil {
		t.Fatal(err)
	}
	if SetKeyProtocol("relay", KeyRef("k1"), "gemini") == nil {
		t.Fatal("gemini accepted")
	}
	if err := UseKey("relay", KeyRef("k2")); err != nil {
		t.Fatal(err)
	}
	p := get(t)
	if p.Key != "k2" || p.KeyName != "team" || p.KeyProtocol != Anthropic || len(p.Keys) != 1 || p.Keys[0].Key != "k1" || p.Keys[0].Protocol != Chat || p.Keys[0].Off {
		t.Fatalf("after use: %+v", p)
	}
	if err := SetKeyOn("relay", KeyRef("k2"), false); err != nil {
		t.Fatal(err)
	}
	p = get(t)
	if p.Key != "k1" || !p.Keys[0].Off || p.Keys[0].Key != "k2" || p.Keys[0].Name != "team" {
		t.Fatalf("after off: %+v", p)
	}
	if SetKeyOn("relay", KeyRef("k1"), false) == nil || RemoveKey("relay", KeyRef("k1")) == nil {
		t.Fatal("last enabled key was removable")
	}
	if err := RenameKey("relay", KeyRef("k1"), " me "); err != nil || get(t).KeyName != "me" {
		t.Fatal("rename", err)
	}
	if err := RemoveKey("relay", KeyRef("k2")); err != nil || len(get(t).Keys) != 0 {
		t.Fatal("remove extra", err)
	}
	if err := AddKey("relay", "", "k3", ""); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", KeyRef("k1")); err != nil || get(t).Key != "k3" {
		t.Fatalf("remove first: %+v", get(t))
	}
}

func TestSaveNormalizes(t *testing.T) {
	isolate(t)
	err := Save(Provider{ID: "relay", Key: "k", Chat: "r.example/v1", KeyProtocol: "nope"})
	if err == nil {
		t.Fatal("bad keyProtocol saved")
	}
	err = Save(Provider{ID: "relay", Key: "k", Chat: "r.example/v1", Catalog: " openai, deepseek ,",
		Headers: map[string]string{" X-Org-Id ": " acme ", "  ": "x"}, Fallback: []string{" a/b ", "", "a/b", "c/d"}})
	if err != nil {
		t.Fatal(err)
	}
	p := get(t)
	if p.Headers["X-Org-Id"] != "acme" || len(p.Headers) != 1 || len(p.Fallback) != 2 || p.Catalog != "openai,deepseek" {
		t.Fatalf("%+v", p)
	}
	Save(Provider{ID: "relay", Key: "k", Chat: "r.example/v1", Headers: map[string]string{"": "x"}})
	b, _ := os.ReadFile(Path())
	if bytes.Contains(b, []byte("headers")) {
		t.Fatalf("empty headers written: %s", b)
	}
}

func TestHeadersOnWire(t *testing.T) {
	isolate(t)
	var got http.Header
	var raw []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header
		for k := range r.Header {
			raw = append(raw, k)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	p := Provider{ID: "relay", Key: "k", Chat: srv.URL, Headers: map[string]string{"X-Org-Id": "acme", "authorization": "Bearer other"}}
	req, _ := http.NewRequest("POST", srv.URL, nil)
	p.Sign(context.Background(), req, Chat, nil)
	http.DefaultClient.Do(req)
	if got.Get("X-Org-Id") != "acme" || got.Get("Authorization") != "Bearer other" {
		t.Fatalf("%v", got)
	}
	p.Preset = "x"
	req, _ = http.NewRequest("POST", srv.URL, nil)
	p.Sign(context.Background(), req, Chat, nil)
	http.DefaultClient.Do(req)
	if got.Get("X-Org-Id") != "" {
		t.Fatal("preset sent headers")
	}
}

var png = []byte("\x89PNG\r\n\x1a\n0000")

func TestIcons(t *testing.T) {
	isolate(t)
	a, err := StoreIcon(png)
	if err != nil || len(a) != len("file:")+16+4 {
		t.Fatal(a, err)
	}
	if b, _ := StoreIcon(png); b != a {
		t.Fatal("not deduped")
	}
	if _, err := StoreIcon(nil); err == nil {
		t.Fatal("empty")
	}
	if _, err := StoreIcon(make([]byte, MaxIcon+1)); err == nil {
		t.Fatal("big")
	}
	if _, err := StoreIcon([]byte("hello")); err == nil {
		t.Fatal("text")
	}
	if s, err := StoreIcon([]byte(`<?xml version="1.0"?><svg xmlns=""/>`)); err != nil || filepath.Ext(s) != ".svg" {
		t.Fatal(s, err)
	}
	name := a[len("file:"):]
	if IconFile(name) == "" || IconFile("../"+name) != "" || IconFile("abcd.png") != "" || IconFile(name[:16]+".txt") != "" || IconFile("") != "" || IconFile(name[:15]+"G.png") != "" {
		t.Fatal("IconFile validation")
	}
	// cleanup: old unreferenced goes, fresh and referenced stay
	old, _ := StoreIcon([]byte("\x89PNG\r\n\x1a\nold"))
	used, _ := StoreIcon([]byte("\x89PNG\r\n\x1a\nused"))
	fresh, _ := StoreIcon([]byte("\x89PNG\r\n\x1a\nfresh"))
	past := time.Now().Add(-2 * time.Hour)
	for _, n := range []string{old, used} {
		os.Chtimes(IconFile(n[5:]), past, past)
	}
	if err := Save(Provider{ID: "relay", Key: "k", Chat: "https://r.example/v1", Icon: used}); err != nil {
		t.Fatal(err)
	}
	exists := func(n string) bool { _, err := os.Stat(IconFile(n[5:])); return err == nil }
	if exists(old) || !exists(used) || !exists(fresh) {
		t.Fatal("cleanup", exists(old), exists(used), exists(fresh))
	}
}
