package provider

import (
	"bufio"
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

// hdrxIsolate points every magpie and agent path at a fresh temp dir.
func hdrxIsolate(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	return home
}

// hdrxRawServer answers every request on every connection with a model
// list, and records each request's header block exactly as it arrived on
// the wire. net/http would canonicalise the names, so this reads raw bytes.
func hdrxRawServer(t *testing.T) (addr string, seen func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var blocks []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go hdrxServe(c, &mu, &blocks)
		}
	}()
	return ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), blocks...)
	}
}

func hdrxServe(c net.Conn, mu *sync.Mutex, blocks *[]string) {
	defer c.Close()
	br := bufio.NewReader(c)
	const body = `{"data":[{"id":"m1"}]}`
	for {
		var head strings.Builder
		length := 0
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			head.WriteString(line)
			if line == "\r\n" {
				break
			}
			if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
				length, _ = strconv.Atoi(strings.TrimSpace(v))
			}
		}
		if _, err := io.CopyN(io.Discard, br, int64(length)); err != nil {
			return
		}
		mu.Lock()
		*blocks = append(*blocks, head.String())
		mu.Unlock()
		resp := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: " +
			strconv.Itoa(len(body)) + "\r\n\r\n" + body
		if _, err := io.WriteString(c, resp); err != nil {
			return
		}
	}
}

func TestHdrxHeadersReachEveryRequest(t *testing.T) {
	hdrxIsolate(t)
	addr, seen := hdrxRawServer(t)
	p := Provider{
		ID: "hdrx-relay", Name: "Relay", Key: "sk-real",
		Chat:    "http://" + addr + "/v1",
		Models:  []string{"m1"},
		Headers: map[string]string{"X-Org-Id": "acme", "x-trace": "on"},
	}
	// Test fetches the model list first, then probes the chat endpoint.
	res := p.Test(context.Background())
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("probe failed: %+v", res)
	}
	blocks := seen()
	if len(blocks) < 2 {
		t.Fatalf("want a model-list request and a probe, saw %d", len(blocks))
	}
	for _, b := range blocks {
		if !strings.Contains(b, "\r\nX-Org-Id: acme\r\n") {
			t.Errorf("request missing X-Org-Id with its casing:\n%s", b)
		}
		if !strings.Contains(b, "\r\nx-trace: on\r\n") {
			t.Errorf("request missing x-trace with its casing:\n%s", b)
		}
		if !strings.Contains(b, "Authorization: Bearer sk-real") {
			t.Errorf("auth header lost:\n%s", b)
		}
	}
}

func TestHdrxHeadersOverrideDefaults(t *testing.T) {
	hdrxIsolate(t)
	addr, seen := hdrxRawServer(t)
	p := Provider{
		ID: "hdrx-override", Name: "Override", Key: "sk-real",
		Chat:    "http://" + addr + "/v1",
		Models:  []string{"m1"},
		Headers: map[string]string{"authorization": "Bearer custom"},
	}
	p.Test(context.Background())
	blocks := seen()
	if len(blocks) == 0 {
		t.Fatal("no request reached the server")
	}
	for _, b := range blocks {
		if !strings.Contains(b, "\r\nauthorization: Bearer custom\r\n") {
			t.Errorf("custom authorization not sent as typed:\n%s", b)
		}
		if strings.Contains(b, "sk-real") {
			t.Errorf("default Authorization still sent:\n%s", b)
		}
	}
}

func TestHdrxHeadersNotForPresetsOrAccounts(t *testing.T) {
	hdrxIsolate(t)
	h := map[string]string{"X-Org-Id": "acme"}
	preset := Provider{ID: "deepseek", Preset: "deepseek", Key: "sk", Chat: "https://api.deepseek.com/v1", Headers: h}
	acct := Provider{ID: "claude", Account: &Account{Agent: "claude"}, Headers: h}
	for _, p := range []Provider{preset, acct} {
		if got := p.ExtraHeaders(); got != nil {
			t.Errorf("%s: ExtraHeaders = %v, want none", p.ID, got)
		}
		req, _ := http.NewRequest(http.MethodPost, "https://example.com/v1/chat", nil)
		if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
			t.Fatal(err)
		}
		if v := req.Header.Values("X-Org-Id"); len(v) != 0 {
			t.Errorf("%s: header leaked onto request: %v", p.ID, v)
		}
	}
}

func TestHdrxHeadersSavedCleaned(t *testing.T) {
	hdrxIsolate(t)
	err := Save(Provider{
		ID: "hdrx-clean", Name: "Clean", Key: "sk", Chat: "https://relay.example.com/v1",
		Headers: map[string]string{" X-A ": " 1 ", "   ": "dropped"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Find("hdrx-clean")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Headers) != 1 || got.Headers["X-A"] != "1" {
		t.Fatalf("headers = %v, want map[X-A:1]", got.Headers)
	}

	// only blank entries: nothing is written
	if err := Save(Provider{ID: "hdrx-clean", Name: "Clean", Key: "sk", Chat: "https://relay.example.com/v1",
		Headers: map[string]string{" ": "x"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"headers"`) {
		t.Fatalf("empty header map was written:\n%s", b)
	}
}

func TestHdrxRejectsBadHeaderName(t *testing.T) {
	hdrxIsolate(t)
	err := Save(Provider{ID: "hdrx-bad", Name: "Bad", Key: "sk", Chat: "https://relay.example.com/v1",
		Headers: map[string]string{"X Bad": "v"}})
	if err == nil {
		t.Fatal("a header name with a space was accepted")
	}
}

// icnxPNG is a PNG signature followed by filler; enough for content sniffing.
func icnxPNG(n int) []byte {
	b := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, n)...)
	return b
}

func icnxFiles(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(IconDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func TestIcnxStoreIconDedupes(t *testing.T) {
	hdrxIsolate(t)
	a, err := StoreIcon(icnxPNG(32))
	if err != nil {
		t.Fatal(err)
	}
	b, err := StoreIcon(icnxPNG(32))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("same image, different refs: %q %q", a, b)
	}
	if !strings.HasPrefix(a, "file:") || !strings.HasSuffix(a, ".png") || len(a) != len("file:")+16+len(".png") {
		t.Fatalf("ref %q is not file:<16 hex>.png", a)
	}
	if files := icnxFiles(t); len(files) != 1 {
		t.Fatalf("icons dir holds %v, want one file", files)
	}
}

func TestIcnxStoreIconRejects(t *testing.T) {
	hdrxIsolate(t)
	cases := map[string][]byte{
		"empty":     nil,
		"plain":     []byte("hello, this is just text"),
		"too large": icnxPNG(MaxIcon),
		"html":      []byte("<html><body>x</body></html>"),
	}
	for name, data := range cases {
		if _, err := StoreIcon(data); err == nil {
			t.Errorf("%s: StoreIcon accepted it", name)
		}
	}
	if files := icnxFiles(t); len(files) != 0 {
		t.Fatalf("rejected images were stored: %v", files)
	}
}

func TestIcnxStoreIconFormats(t *testing.T) {
	hdrxIsolate(t)
	want := map[string][]byte{
		".jpg":  {0xff, 0xd8, 0xff, 0xe0, 0, 0},
		".gif":  []byte("GIF89a....."),
		".webp": append([]byte("RIFF\x00\x00\x00\x00WEBP"), 0, 0, 0, 0),
		".ico":  {0, 0, 1, 0, 1, 0},
		".svg":  []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`),
	}
	for ext, data := range want {
		ref, err := StoreIcon(data)
		if err != nil {
			t.Errorf("%s: %v", ext, err)
			continue
		}
		if !strings.HasSuffix(ref, ext) {
			t.Errorf("%s: ref %q has the wrong extension", ext, ref)
		}
	}
}

func TestIcnxIconFileResolvesOnlyStoredNames(t *testing.T) {
	hdrxIsolate(t)
	ref, err := StoreIcon(icnxPNG(8))
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(ref, "file:")
	if path := IconFile(name); path == "" {
		t.Fatalf("IconFile(%q) = \"\"", name)
	} else if _, err := os.Stat(path); err != nil {
		t.Fatalf("IconFile path does not exist: %v", err)
	}
	bad := []string{
		"", "../" + name, "a/" + name, `a\` + name, "..",
		"0123456789abcdef.exe", "0123456789ABCDEF.png", "0123456789abcde.png",
		"0123456789abcdef0.png", "0123456789abcdef.PNG", "../../providers.json",
		"0123456789abcdef.png/..", "file:" + name,
	}
	for _, b := range bad {
		if got := IconFile(b); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", b, got)
		}
	}
}

func TestIcnxSaveRejectsUnstoredIconRef(t *testing.T) {
	hdrxIsolate(t)
	err := Save(Provider{ID: "icnx-ref", Name: "Ref", Key: "sk", Chat: "https://relay.example.com/v1",
		Icon: "file:../../secret.png"})
	if err == nil {
		t.Fatal("a traversal icon reference was saved")
	}
}

func TestIcnxPruneKeepsReferencedAndRecent(t *testing.T) {
	hdrxIsolate(t)
	old := time.Now().Add(-2 * time.Hour)

	used, err := StoreIcon(icnxPNG(16))
	if err != nil {
		t.Fatal(err)
	}
	stale, err := StoreIcon(icnxPNG(17))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := StoreIcon(icnxPNG(18))
	if err != nil {
		t.Fatal(err)
	}
	// the referenced image is also old; only the reference should save it
	for _, ref := range []string{used, stale} {
		if err := os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), old, old); err != nil {
			t.Fatal(err)
		}
	}

	// saving any provider runs the sweep
	if err := Save(Provider{ID: "icnx-user", Name: "User", Key: "sk", Chat: "https://relay.example.com/v1",
		Icon: used}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(IconFile(strings.TrimPrefix(used, "file:"))); err != nil {
		t.Errorf("referenced icon was removed: %v", err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(fresh, "file:"))); err != nil {
		t.Errorf("recent unreferenced icon was removed: %v", err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(stale, "file:"))); !os.IsNotExist(err) {
		t.Errorf("old unreferenced icon survived the sweep (err=%v)", err)
	}
}

func TestIcnxPruneIgnoresForeignFiles(t *testing.T) {
	hdrxIsolate(t)
	if _, err := StoreIcon(icnxPNG(4)); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(IconDir(), "notes.txt")
	if err := os.WriteFile(foreign, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	os.Chtimes(foreign, old, old)
	pruneIcons(nil)
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("a file that is not an icon was deleted: %v", err)
	}
}
