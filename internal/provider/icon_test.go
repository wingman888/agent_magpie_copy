package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var pngHead = []byte("\x89PNG\r\n\x1a\n")

func isolate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestStoreIcon(t *testing.T) {
	isolate(t)
	data := append(append([]byte{}, pngHead...), "body"...)
	ref, err := StoreIcon(data)
	if err != nil || !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") || len(ref) != len("file:")+16+4 {
		t.Fatalf("ref %q err %v", ref, err)
	}
	if again, _ := StoreIcon(data); again != ref {
		t.Fatalf("second ref %q", again)
	}
	got, err := os.ReadFile(IconFile(strings.TrimPrefix(ref, "file:")))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read back: %v", err)
	}
	for body, ext := range map[string]string{
		"<?xml version=\"1.0\"?>\n<SVG/>": ".svg",
		"\xff\xd8\xffxx":                  ".jpg",
		"GIF89a..":                        ".gif",
		"RIFF\x00\x00\x00\x00WEBPVP8 ":    ".webp",
		"\x00\x00\x01\x00ico":             ".ico",
	} {
		if r, err := StoreIcon([]byte(body)); err != nil || !strings.HasSuffix(r, ext) {
			t.Errorf("%q → %q %v", body, r, err)
		}
	}
}

func TestStoreIconErrors(t *testing.T) {
	isolate(t)
	if _, err := StoreIcon([]byte("hello")); err == nil || !strings.HasPrefix(err.Error(), "not a picture") {
		t.Errorf("hello: %v", err)
	}
	if _, err := StoreIcon(nil); err == nil || err.Error() != "the picture is empty" {
		t.Errorf("empty: %v", err)
	}
	big := append(append([]byte{}, pngHead...), make([]byte, MaxIcon)...)
	if _, err := StoreIcon(big); err == nil || !strings.Contains(err.Error(), "over 1 MB") {
		t.Errorf("big: %v", err)
	}
	exact := big[:MaxIcon]
	if _, err := StoreIcon(exact); err != nil {
		t.Errorf("exact: %v", err)
	}
	f := filepath.Join(t.TempDir(), "big.png")
	os.WriteFile(f, big, 0o644)
	if _, err := StoreIconFile(f); err == nil || !strings.Contains(err.Error(), "over 1 MB") {
		t.Errorf("file: %v", err)
	}
}

func TestIconFile(t *testing.T) {
	isolate(t)
	for _, n := range []string{"../providers.json", "x.png", "0123456789abcdef.exe", ""} {
		if IconFile(n) != "" {
			t.Errorf("%q accepted", n)
		}
	}
	if IconFile("0123456789abcdef.svg") != filepath.Join(IconDir(), "0123456789abcdef.svg") {
		t.Error("good name rejected")
	}
}

func TestSweepIcons(t *testing.T) {
	isolate(t)
	used, _ := StoreIcon(append(append([]byte{}, pngHead...), "a"...))
	old, _ := StoreIcon(append(append([]byte{}, pngHead...), "b"...))
	fresh, _ := StoreIcon(append(append([]byte{}, pngHead...), "c"...))
	oldUsed, _ := StoreIcon(append(append([]byte{}, pngHead...), "d"...))
	stray := filepath.Join(IconDir(), "notes.txt")
	os.WriteFile(stray, []byte("x"), 0o644)
	past := time.Now().Add(-2 * time.Hour)
	for _, ref := range []string{old, oldUsed} {
		os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), past, past)
	}
	os.Chtimes(stray, past, past)
	if err := Save(Provider{Name: "A", Chat: "https://a.example.com/v1", Key: "k", Icon: oldUsed}); err != nil {
		t.Fatal(err)
	}
	_ = used
	exists := func(ref string) bool {
		_, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:")))
		return err == nil
	}
	if exists(old) || !exists(oldUsed) || !exists(fresh) {
		t.Errorf("old %v oldUsed %v fresh %v", exists(old), exists(oldUsed), exists(fresh))
	}
	if _, err := os.Stat(stray); err != nil {
		t.Error("stray file removed")
	}
}

func TestHeadersSaved(t *testing.T) {
	isolate(t)
	err := Save(Provider{Name: "R", Chat: "https://r.example.com/v1", Key: "k", Headers: map[string]string{" X-Org ": " acme ", "  ": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := Find("r")
	if len(p.Headers) != 1 || p.Headers["X-Org"] != "acme" {
		t.Fatalf("%v", p.Headers)
	}
	Save(Provider{Name: "R", Chat: "https://r.example.com/v1", Key: "k", Headers: map[string]string{" ": "x"}})
	p, _ = Find("r")
	b, _ := json.Marshal(p)
	if p.Headers != nil || strings.Contains(string(b), "headers") {
		t.Errorf("headers kept: %s", b)
	}
	Save(Provider{Name: "Deep", Preset: "deepseek", Chat: "https://api.deepseek.com/v1", Key: "k", Headers: map[string]string{"A": "b"}})
	if p, _ := Find("deep"); p == nil || p.Headers != nil {
		t.Errorf("preset kept headers")
	}
}

func TestSignAndFetchHeaders(t *testing.T) {
	isolate(t)
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()
	p := Provider{ID: "r", Name: "R", Chat: srv.URL + "/v1", Key: "k", Headers: map[string]string{"x-id": "1", "Authorization": "Token z"}}
	req, _ := http.NewRequest("POST", srv.URL, nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if v := req.Header["x-id"]; len(v) != 1 || v[0] != "1" {
		t.Errorf("x-id: %v", req.Header)
	}
	if req.Header.Get("Authorization") != "Token z" {
		t.Errorf("auth: %q", req.Header.Get("Authorization"))
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Get("X-Id") != "1" || got.Get("Authorization") != "Token z" {
		t.Errorf("fetch headers: %v", got)
	}
	acct := p
	acct.Account = &Account{}
	req, _ = http.NewRequest("POST", srv.URL, nil)
	acct.Sign(context.Background(), req, Chat, nil)
	if _, ok := req.Header["x-id"]; ok {
		t.Error("account got custom headers")
	}
}
