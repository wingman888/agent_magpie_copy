package provider

import (
	"bufio"
	"bytes"
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

	"github.com/yetone/magpie/internal/extrahdr"
)

// hdrtIsolate points magpie's config, cache and home at temp dirs.
func hdrtIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// hdrtRawServer answers every request with a model list and sends each
// request's header lines, exactly as they arrived, on got.
func hdrtRawServer(t *testing.T, got chan<- []string) string {
	t.Helper()
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
			go hdrtServeRaw(c, got)
		}
	}()
	return "http://" + ln.Addr().String()
}

func hdrtServeRaw(c net.Conn, got chan<- []string) {
	defer c.Close()
	r := bufio.NewReader(c)
	var lines []string
	clen := 0
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
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "content-length") {
			clen, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	_, _ = io.CopyN(io.Discard, r, int64(clen))
	got <- lines
	body := `{"data":[{"id":"hdrt-model"}]}`
	fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

// hdrtFindLines returns the raw lines whose name matches, case-insensitively.
func hdrtFindLines(lines []string, name string) []string {
	var out []string
	for _, l := range lines {
		if k, _, ok := strings.Cut(l, ":"); ok && strings.EqualFold(strings.TrimSpace(k), name) {
			out = append(out, l)
		}
	}
	return out
}

func TestHdrtCustomHeadersOnTestAndModelList(t *testing.T) {
	hdrtIsolate(t)
	got := make(chan []string, 10)
	base := hdrtRawServer(t, got)
	if err := Save(Provider{Name: "Hdrt Relay", Chat: base + "/v1", Key: "sk-hdrt",
		Headers: map[string]string{" X-Org-Id ": " acme ", "x-trace": "t1", "User-Agent": "acme-cli", "  ": "dropped"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("hdrt-relay")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, r := range p.Test(ctx) {
		if !r.OK {
			t.Fatalf("probe %s failed: %+v", r.Protocol, r)
		}
	}
	close(got)
	n := 0
	for lines := range got {
		n++
		if !hdrtHasLine(lines, "X-Org-Id: acme") {
			t.Errorf("request %d lacks typed-case X-Org-Id: %q", n, lines)
		}
		if !hdrtHasLine(lines, "x-trace: t1") {
			t.Errorf("request %d lacks lower-case x-trace: %q", n, lines)
		}
		if ua := hdrtFindLines(lines, "User-Agent"); len(ua) != 1 || ua[0] != "User-Agent: acme-cli" {
			t.Errorf("request %d user-agent = %q, want one acme-cli line", n, ua)
		}
		if !hdrtHasLine(lines, "Authorization: Bearer sk-hdrt") {
			t.Errorf("request %d lost its auth header: %q", n, lines)
		}
	}
	if n != 2 {
		t.Errorf("expected a model list request and a probe, got %d requests", n)
	}
}

func hdrtHasLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func TestHdrtPresetAndAccountSendNoCustomHeaders(t *testing.T) {
	hdrtIsolate(t)
	hdrs := map[string]string{"X-Org-Id": "acme"}
	cases := map[string]Provider{
		"preset":  {ID: "deepseek", Preset: "deepseek", Chat: "https://api.deepseek.com/v1", Key: "k", Headers: hdrs},
		"account": {ID: "codex", Account: &Account{Agent: "codex", User: "a@example.com"}, Responses: CodexBase, Headers: hdrs},
	}
	for name, p := range cases {
		if h := p.CustomHeaders(); h != nil {
			t.Errorf("%s: CustomHeaders = %v, want nil", name, h)
		}
		req, _ := http.NewRequest(http.MethodPost, "https://example.test/v1", nil)
		if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
			t.Fatal(err)
		}
		if v := req.Header.Get("X-Org-Id"); v != "" {
			t.Errorf("%s: sent X-Org-Id=%q", name, v)
		}
	}
}

func TestHdrtSaveDropsEmptyHeaderMap(t *testing.T) {
	hdrtIsolate(t)
	if err := Save(Provider{Name: "Hdrt Plain", Chat: "https://relay.example.test/v1", Key: "k",
		Headers: map[string]string{"  ": "x", "": "y"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"headers"`) {
		t.Errorf("empty header map was written: %s", b)
	}
	p, _ := Find("hdrt-plain")
	if p.Headers != nil {
		t.Errorf("headers = %v, want none", p.Headers)
	}
}

// ---- icons ------------------------------------------------------------------

var hdrtNameRe = regexp.MustCompile(`^file:[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

func hdrtPNG(extra int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, extra)...)
}

func hdrtIconCount(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir(IconsDir())
	if err != nil {
		return 0
	}
	return len(ents)
}

func TestHdrtStoreIconFormatsAndNames(t *testing.T) {
	hdrtIsolate(t)
	webp := append([]byte("RIFF\x10\x00\x00\x00WEBP"), bytes.Repeat([]byte{1}, 8)...)
	cases := map[string]struct {
		data []byte
		ext  string
	}{
		"png":  {hdrtPNG(20), ".png"},
		"jpeg": {append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{2}, 20)...), ".jpg"},
		"gif":  {append([]byte("GIF89a"), bytes.Repeat([]byte{3}, 20)...), ".gif"},
		"webp": {webp, ".webp"},
		"ico":  {append([]byte{0, 0, 1, 0}, bytes.Repeat([]byte{4}, 20)...), ".ico"},
		"svg":  {[]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`), ".svg"},
	}
	for name, c := range cases {
		v, err := StoreIcon(c.data)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !hdrtNameRe.MatchString(v) {
			t.Errorf("%s: value %q does not match file:<16 hex>%s", name, v, c.ext)
			continue
		}
		if !strings.HasSuffix(v, c.ext) {
			t.Errorf("%s: value %q, want extension %s", name, v, c.ext)
		}
		path := IconFile(strings.TrimPrefix(v, "file:"))
		if path == "" {
			t.Fatalf("%s: IconFile refused its own name %q", name, v)
		}
		if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, c.data) {
			t.Errorf("%s: stored file wrong: %v", name, err)
		}
	}
}

func TestHdrtStoreIconDeduplicates(t *testing.T) {
	hdrtIsolate(t)
	data := hdrtPNG(64)
	a, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("same picture gave %q and %q", a, b)
	}
	if n := hdrtIconCount(t); n != 1 {
		t.Errorf("icons folder holds %d files, want 1", n)
	}
}

func TestHdrtStoreIconRejects(t *testing.T) {
	hdrtIsolate(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty content accepted")
	}
	if _, err := StoreIcon([]byte{}); err == nil {
		t.Error("zero-length content accepted")
	}
	if _, err := StoreIcon(hdrtPNG(MaxIcon)); err == nil {
		t.Errorf("content over %d bytes accepted", MaxIcon)
	}
	if _, err := StoreIcon(hdrtPNG(MaxIcon - 8)); err != nil {
		t.Errorf("content of exactly MaxIcon bytes refused: %v", err)
	}
	if _, err := StoreIcon([]byte("just some plain text, not a picture")); err == nil {
		t.Error("plain text accepted")
	}
}

func TestHdrtIconFileOnlyAcceptsStoredNames(t *testing.T) {
	hdrtIsolate(t)
	good := "0123456789abcdef.png"
	if p := IconFile(good); p != filepath.Join(IconsDir(), good) {
		t.Errorf("IconFile(%q) = %q", good, p)
	}
	for _, bad := range []string{
		"", "../providers.json", "../../etc/passwd", "sub/0123456789abcdef.png",
		`..\0123456789abcdef.png`, "0123456789abcdef.exe", "0123456789ABCDEF.png",
		"0123456789abcde.png", "0123456789abcdef0.png", "0123456789abcdef.png/..",
		"0123456789abcdef.png\x00", "file:0123456789abcdef.png", "..",
	} {
		if p := IconFile(bad); p != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, p)
		}
	}
}

func TestHdrtSaveRejectsIconOutsideFolder(t *testing.T) {
	hdrtIsolate(t)
	err := Save(Provider{Name: "Hdrt Evil", Chat: "https://relay.example.test/v1", Key: "k", Icon: "file:../../providers.json"})
	if err == nil {
		t.Error("icon pointing outside icons/ was saved")
	}
}

func TestHdrtSavePrunesUnusedIcons(t *testing.T) {
	hdrtIsolate(t)
	old := time.Now().Add(-3 * time.Hour)
	stale := func(v string) string {
		path := IconFile(strings.TrimPrefix(v, "file:"))
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		return path
	}
	kept, err := StoreIcon(hdrtPNG(30))
	if err != nil {
		t.Fatal(err)
	}
	keptPath := stale(kept)
	orphan, err := StoreIcon(append([]byte("GIF89a"), bytes.Repeat([]byte{9}, 30)...))
	if err != nil {
		t.Fatal(err)
	}
	orphanPath := stale(orphan)
	other := filepath.Join(IconsDir(), "notes.txt")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{Name: "Hdrt Iconed", Chat: "https://relay.example.test/v1", Key: "k", Icon: kept}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Errorf("unreferenced old icon survived: %v", err)
	}
	if _, err := os.Stat(keptPath); err != nil {
		t.Errorf("referenced icon removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("a file that is not an icon was removed: %v", err)
	}
}

func TestHdrtGraceKeepsFreshUnusedIcon(t *testing.T) {
	hdrtIsolate(t)
	fresh, err := StoreIcon(hdrtPNG(40))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := Save(Provider{Name: "Hdrt Other", Chat: "https://relay.example.test/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(fresh, "file:"))); err != nil {
		t.Errorf("icon chosen moments ago was swept away: %v", err)
	}
	// once it is old and still unused, the next save clears it
	if err := os.Chtimes(IconFile(strings.TrimPrefix(fresh, "file:")), old, old); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{Name: "Hdrt Other", Chat: "https://relay.example.test/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(fresh, "file:"))); !os.IsNotExist(err) {
		t.Errorf("old unused icon was kept: %v", err)
	}
}

// extrahdr is covered here because its package is not in the check list.
func TestHdrtExtraHeadersOverrideAndKeepCase(t *testing.T) {
	h := http.Header{}
	h.Set("User-Agent", "magpie")
	h.Set("Authorization", "Bearer k")
	extrahdr.Set(h, map[string]string{"user-agent": "acme-cli", "X-Org-Id": "acme"})
	if _, ok := h["User-Agent"]; ok {
		t.Errorf("default User-Agent should be replaced: %v", h)
	}
	if got := h["user-agent"]; len(got) != 1 || got[0] != "acme-cli" {
		t.Errorf("user-agent = %v, want [acme-cli] under the typed case", got)
	}
	if got := h["X-Org-Id"]; len(got) != 1 || got[0] != "acme" {
		t.Errorf("X-Org-Id = %v", got)
	}
	if h.Get("Authorization") != "Bearer k" {
		t.Errorf("auth header touched: %v", h)
	}
}
