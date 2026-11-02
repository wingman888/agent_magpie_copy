package provider

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// cleanHome points magpie's config at an empty temp home.
func cleanHome(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

var pngData = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDRfake")

func TestIconFileRejects(t *testing.T) {
	cleanHome(t)
	for _, n := range []string{"../providers.json", "x.png", "0123456789abcdef.exe", "", "0123456789ABCDEF.png",
		"0123456789abcdef0.png", "../0123456789abcdef.png", "a/0123456789abcdef.png", "0123456789abcdef.png/"} {
		if got := IconFile(n); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", n, got)
		}
	}
	if got := IconFile("0123456789abcdef.svg"); got != filepath.Join(filepath.Dir(Path()), "icons", "0123456789abcdef.svg") {
		t.Errorf("IconFile(valid) = %q", got)
	}
}

func TestStoreIconDedupes(t *testing.T) {
	cleanHome(t)
	a, err := StoreIcon(pngData)
	if err != nil {
		t.Fatal(err)
	}
	b, err := StoreIcon(append([]byte(nil), pngData...))
	if err != nil {
		t.Fatal(err)
	}
	if a != b || !strings.HasPrefix(a, "file:") || !strings.HasSuffix(a, ".png") || len(a) != len("file:")+16+4 {
		t.Fatalf("refs %q %q", a, b)
	}
	entries, _ := os.ReadDir(filepath.Join(filepath.Dir(Path()), "icons"))
	if len(entries) != 1 {
		t.Fatalf("%d files in icons/", len(entries))
	}
	got, err := os.ReadFile(IconFile(strings.TrimPrefix(a, "file:")))
	if err != nil || !bytes.Equal(got, pngData) {
		t.Fatalf("stored %q %v", got, err)
	}
}

func TestStoreIconTypes(t *testing.T) {
	cleanHome(t)
	cases := map[string][]byte{
		".png":  pngData,
		".jpg":  []byte("\xFF\xD8\xFF\xE0jfif"),
		".gif":  []byte("GIF89a...."),
		".webp": []byte("RIFF\x10\x00\x00\x00WEBPVP8 "),
		".ico":  []byte("\x00\x00\x01\x00\x01\x00"),
		".svg":  []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`),
	}
	for ext, data := range cases {
		ref, err := StoreIcon(data)
		if err != nil || !strings.HasSuffix(ref, ext) {
			t.Errorf("%s: %q %v", ext, ref, err)
		}
	}
	// SVG is told by content, not by name
	dir := t.TempDir()
	txt := filepath.Join(dir, "logo.txt")
	os.WriteFile(txt, cases[".svg"], 0o644)
	if ref, err := StoreIconFile(txt); err != nil || !strings.HasSuffix(ref, ".svg") {
		t.Errorf("svg in .txt: %q %v", ref, err)
	}
}

func TestStoreIconRejects(t *testing.T) {
	cleanHome(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty accepted")
	}
	if _, err := StoreIcon([]byte("just some text")); err == nil {
		t.Error("text accepted")
	}
	big := append(append([]byte(nil), pngData...), make([]byte, MaxIcon)...)
	if _, err := StoreIcon(big); err == nil || !strings.Contains(err.Error(), "smaller") {
		t.Errorf("oversized: %v", err)
	}
	if _, err := StoreIcon(big[:MaxIcon]); err != nil {
		t.Errorf("exactly MaxIcon refused: %v", err)
	}
	dir := t.TempDir()
	for name, data := range map[string][]byte{"empty.png": nil, "notes.png": []byte("hello"), "big.png": big} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o644)
		if _, err := StoreIconFile(p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := StoreIconFile(filepath.Join(dir, "missing.png")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestIconsCleanedUp(t *testing.T) {
	cleanHome(t)
	old := time.Now().Add(-2 * time.Hour)
	store := func(data []byte, age time.Time) string {
		ref, err := StoreIcon(data)
		if err != nil {
			t.Fatal(err)
		}
		os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), age, age)
		return ref
	}
	used := store([]byte("GIF89a used"), old)
	dropped := store([]byte("GIF89a dropped"), old)
	fresh := store([]byte("GIF89a fresh"), time.Now())
	exists := func(ref string) bool {
		_, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:")))
		return err == nil
	}
	if err := Save(Provider{Name: "A", Chat: "https://a.example/v1", Key: "k", Icon: used}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{Name: "B", Chat: "https://b.example/v1", Key: "k", Icon: "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	if !exists(used) || exists(dropped) || !exists(fresh) {
		t.Fatalf("after save: used %v dropped %v fresh %v", exists(used), exists(dropped), exists(fresh))
	}
	if err := Delete("a"); err != nil {
		t.Fatal(err)
	}
	if exists(used) || !exists(fresh) {
		t.Fatalf("after delete: used %v fresh %v", exists(used), exists(fresh))
	}
}

func TestPresetKeepsItsIconAndNoHeaders(t *testing.T) {
	cleanHome(t)
	pr, err := FromPreset("deepseek")
	if err != nil {
		t.Skip("no deepseek preset")
	}
	pr.Key = "k"
	pr.Headers = map[string]string{"X-Org-Id": "acme"}
	ref, _ := StoreIcon(pngData)
	pr.Icon = ref
	if err := Save(pr); err != nil {
		t.Fatal(err)
	}
	p, err := Find(pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 0 || strings.HasPrefix(p.Icon, "file:") {
		t.Fatalf("preset kept %v / %q", p.Headers, p.Icon)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "headers") {
		t.Fatalf("headers written for a preset: %s", b)
	}
}

func TestHeadersCleaned(t *testing.T) {
	cleanHome(t)
	err := Save(Provider{Name: "Relay", Chat: "https://r.example/v1", Key: "k",
		Headers: map[string]string{" X-Org-Id ": "  acme ", "   ": "x", "X-ORG-id": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if len(p.Headers) != 2 || p.Headers["X-Org-Id"] != "acme" || p.Headers["X-ORG-id"] != "b" {
		t.Fatalf("headers %#v", p.Headers)
	}
	if err := Save(Provider{Name: "Plain", Chat: "https://p.example/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Count(string(b), `"headers"`) != 1 {
		t.Fatalf("headers field: %s", b)
	}
}

// rawServer records the header lines exactly as they came over the wire
// (net/http's server would canonicalize the names).
type rawServer struct {
	mu    sync.Mutex
	lines []string
	reply string
}

func (s *rawServer) start(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				var head []string
				length := 0
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					l = strings.TrimRight(l, "\r\n")
					if l == "" {
						break
					}
					head = append(head, l)
					if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(k, "Content-Length") {
						length, _ = strconv.Atoi(strings.TrimSpace(v))
					}
				}
				body := make([]byte, length)
				if _, err := io.ReadFull(r, body); err != nil {
					return
				}
				s.mu.Lock()
				s.lines = append(s.lines, head...)
				s.mu.Unlock()
				c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nConnection: close\r\nContent-Length: " +
					strconv.Itoa(len(s.reply)) + "\r\n\r\n" + s.reply))
			}(c)
		}
	}()
	return "http://" + ln.Addr().String()
}

func (s *rawServer) has(line string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.lines {
		if l == line {
			return true
		}
	}
	return false
}

func (s *rawServer) count(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, l := range s.lines {
		if strings.HasPrefix(strings.ToLower(l), strings.ToLower(prefix)) {
			n++
		}
	}
	return n
}

func TestCustomHeadersOnTheWire(t *testing.T) {
	cleanHome(t)
	srv := &rawServer{reply: `{"data":[{"id":"m1"}],"choices":[]}`}
	base := srv.start(t)
	err := Save(Provider{Name: "My Relay", Chat: base + "/v1", Key: "sk-real",
		Headers: map[string]string{"X-ORG-id": "acme", "Authorization": "Token mine", "x-api-token": "t"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find("my-relay")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := p.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	if !srv.has("X-ORG-id: acme") || !srv.has("Authorization: Token mine") || !srv.has("x-api-token: t") {
		t.Fatalf("fetch sent %q", srv.lines)
	}
	srv.lines = nil
	res := p.Test(ctx)
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("test %+v", res)
	}
	if !srv.has("X-ORG-id: acme") || !srv.has("Authorization: Token mine") || srv.count("Authorization:") != srv.count("X-ORG-id:") {
		t.Fatalf("test sent %q", srv.lines)
	}
	if srv.has("X-Org-Id: acme") {
		t.Fatalf("name canonicalized: %q", srv.lines)
	}
}

func TestSignHeaders(t *testing.T) {
	p := Provider{Key: "k", Chat: "https://x.example/v1", Headers: map[string]string{"authorization": "Mine", "X-ORG-id": "1", "X-Org-Id": "2"}}
	req, _ := http.NewRequest("POST", "https://x.example/v1/chat/completions", nil)
	req.Header.Set("X-Org-Id", "stale")
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header["authorization"]; len(got) != 1 || got[0] != "Mine" {
		t.Fatalf("authorization %v in %v", got, req.Header)
	}
	if _, ok := req.Header["Authorization"]; ok {
		t.Fatalf("key header left: %v", req.Header)
	}
	if req.Header["X-ORG-id"][0] != "1" || req.Header["X-Org-Id"][0] != "2" {
		t.Fatalf("headers %v", req.Header)
	}
	// a preset or an account sends none
	for _, q := range []Provider{{Preset: "deepseek", Key: "k", Headers: p.Headers}, {Key: "k", Headers: p.Headers, Account: &Account{}}} {
		req, _ := http.NewRequest("POST", "https://x.example/", nil)
		q.Sign(context.Background(), req, Chat, nil)
		if req.Header.Get("Authorization") != "Bearer k" || len(req.Header) != 1 {
			t.Fatalf("%+v sent %v", q, req.Header)
		}
	}
}
