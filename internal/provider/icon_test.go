package provider

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"context"
)

var png1 = append([]byte("\x89PNG\r\n\x1a\n"), 1, 2, 3)

func TestStoreIcon(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a, err := StoreIcon(png1)
	if err != nil || !strings.HasPrefix(a, "file:") || !strings.HasSuffix(a, ".png") {
		t.Fatal(a, err)
	}
	b, _ := StoreIcon(png1)
	if a != b {
		t.Fatal("not deduplicated")
	}
	ents, _ := os.ReadDir(IconsDir())
	if len(ents) != 1 {
		t.Fatal(len(ents))
	}
	got, _ := os.ReadFile(IconFile(strings.TrimPrefix(a, "file:")))
	if !bytes.Equal(got, png1) {
		t.Fatal("content differs")
	}
	if _, err := StoreIcon([]byte("<SVG xmlns=''></svg>")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, []byte("hello"), append(png1, make([]byte, MaxIcon)...)} {
		if _, err := StoreIcon(bad); err == nil {
			t.Fatal("accepted bad icon")
		}
	}
	for _, n := range []string{"../providers.json", "x.png", "0123456789abcdef.exe", "", "0123456789abcdef.png/../a.png"} {
		if IconFile(n) != "" {
			t.Fatal("IconFile accepted", n)
		}
	}
}

func TestIconSweep(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old, _ := StoreIcon(png1)
	fresh, _ := StoreIcon(append(png1, 9))
	past := time.Now().Add(-2 * time.Hour)
	os.Chtimes(IconFile(strings.TrimPrefix(old, "file:")), past, past)
	if err := Save(Provider{Name: "A", Chat: "http://localhost/v1", Icon: old}); err != nil {
		t.Fatal(err)
	}
	exists := func(ref string) bool {
		_, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:")))
		return err == nil
	}
	if !exists(old) || !exists(fresh) {
		t.Fatal("referenced or fresh icon removed")
	}
	if err := Save(Provider{Name: "A", Chat: "http://localhost/v1", Icon: "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	if exists(old) || !exists(fresh) {
		t.Fatal("sweep wrong")
	}
	_ = filepath.Join
}

func TestHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()
	p := Provider{Name: "R", Chat: srv.URL, Key: "k",
		Headers: map[string]string{" X-Org-Id ": " acme ", "": "x", "Authorization": "mine", "X-ORG-id": "b"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	got, _ := Find("r")
	if len(got.Headers) != 3 || got.Headers["X-Org-Id"] != "acme" {
		t.Fatal(got.Headers)
	}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	got.Sign(context.Background(), req, Chat, nil)
	if _, ok := req.Header["X-ORG-id"]; !ok || req.Header["X-Org-Id"][0] != "acme" || req.Header["Authorization"][0] != "mine" || len(req.Header["Authorization"]) != 1 {
		t.Fatal(req.Header)
	}
	got.Fetch(context.Background())
	if len(seen) == 0 || !strings.Contains(strings.Join(seen[0].Values("X-Org-Id"), ","), "acme") || seen[0].Get("Authorization") != "mine" {
		t.Fatal(seen)
	}
	pr := Provider{Preset: "x", Headers: map[string]string{"A": "b"}}
	if normalize(pr).Headers != nil {
		t.Fatal("preset kept headers")
	}
}
