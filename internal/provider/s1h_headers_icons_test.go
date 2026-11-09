package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// s1hSandbox points HOME, the config and the cache at a fresh temp dir, so
// nothing here reads or writes the machine's own magpie or agent files.
func s1hSandbox(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// s1hRecorder keeps the request heads a raw server saw, byte for byte.
type s1hRecorder struct {
	mu    sync.Mutex
	heads []string
}

func (r *s1hRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.heads...)
}

// s1hRawServer answers every request with a one-model list. Unlike httptest
// it does not canonicalise header names, so the casing on the wire shows.
func s1hRawServer(t *testing.T) (string, *s1hRecorder) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	rec := &s1hRecorder{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s1hAnswer(c, rec)
		}
	}()
	return "http://" + ln.Addr().String(), rec
}

func s1hAnswer(c net.Conn, rec *s1hRecorder) {
	defer c.Close()
	br := bufio.NewReader(c)
	var head strings.Builder
	n := 0
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
			n, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	io.CopyN(io.Discard, br, int64(n))
	rec.mu.Lock()
	rec.heads = append(rec.heads, head.String())
	rec.mu.Unlock()
	body := `{"data":[{"id":"m1"}]}`
	fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

func TestS1HOwnHeadersGoOnEveryRequestAsTyped(t *testing.T) {
	s1hSandbox(t)
	base, rec := s1hRawServer(t)
	ctx := context.Background()

	custom := Provider{ID: "s1h-gw", Name: "S1H Gateway", Chat: base + "/v1", Key: "sk-test", Models: []string{"m1"},
		Headers: map[string]string{"X-Org-Id": "acme", "user-agent": "s1h-agent"}}
	if _, err := custom.Fetch(ctx); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, r := range custom.Test(ctx) {
		if !r.OK {
			t.Fatalf("test: %+v", r)
		}
	}
	heads := rec.all()
	if len(heads) != 3 {
		t.Fatalf("requests seen: %d, want 3 (fetch, test's fetch, test's probe)", len(heads))
	}
	for _, h := range heads {
		if !strings.Contains(h, "\r\nX-Org-Id: acme\r\n") {
			t.Errorf("X-Org-Id not sent as typed:\n%s", h)
		}
		if !strings.Contains(h, "\r\nUser-Agent: s1h-agent\r\n") || strings.Count(h, "User-Agent:") != 1 {
			t.Errorf("User-Agent override not applied once:\n%s", h)
		}
	}

	before := len(rec.all())
	preset := Provider{ID: "s1h-pre", Name: "S1H Preset", Preset: "deepseek", Chat: base + "/v1", Key: "sk-test",
		Models: []string{"m1"}, Headers: map[string]string{"X-Org-Id": "acme"}}
	for _, r := range preset.Test(ctx) {
		if !r.OK {
			t.Fatalf("preset test: %+v", r)
		}
	}
	for _, h := range rec.all()[before:] {
		if strings.Contains(h, "X-Org-Id") {
			t.Errorf("a preset sent a custom header:\n%s", h)
		}
	}
}

func TestS1HSaveCleansHeadersAndRefusesBadOnes(t *testing.T) {
	s1hSandbox(t)
	const relay = "http://127.0.0.1:1/v1"
	if err := Save(Provider{Name: "S1H Clean", Chat: relay, Key: "k",
		Headers: map[string]string{" X-Org-Id ": " acme ", "  ": "dropped"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("s1h-clean")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 1 || p.Headers["X-Org-Id"] != "acme" {
		t.Fatalf("headers after save: %v", p.Headers)
	}
	if err := Save(Provider{Name: "S1H Empty", Chat: relay, Key: "k", Headers: map[string]string{" ": "x"}}); err != nil {
		t.Fatal(err)
	}
	if e, _ := Find("s1h-empty"); e.Headers != nil {
		t.Fatalf("an empty header map was kept: %v", e.Headers)
	}
	if err := Save(Provider{Name: "S1H Bad Name", Chat: relay, Key: "k", Headers: map[string]string{"Bad Name": "x"}}); err == nil {
		t.Fatal("a header name with a space was accepted")
	}
	if err := Save(Provider{Name: "S1H Bad Value", Chat: relay, Key: "k", Headers: map[string]string{"X-Ok": "a\r\nX-Evil: 1"}}); err == nil {
		t.Fatal("a header value with a line break was accepted")
	}
}

// s1hPNG is a PNG signature followed by n zero bytes: enough for the sniffer.
func s1hPNG(n int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, n)...)
}

func TestS1HStoreIconKeepsOnePictureAndRefusesTheRest(t *testing.T) {
	s1hSandbox(t)
	png := s1hPNG(40)
	ref, err := StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(ref, "file:")
	if !strings.HasPrefix(ref, "file:") || len(name) != 16+len(".png") || !strings.HasSuffix(name, ".png") {
		t.Fatalf("ref %q", ref)
	}
	if _, err := os.Stat(IconFile(name)); err != nil {
		t.Fatalf("stored file: %v", err)
	}
	again, err := StoreIcon(png)
	if err != nil || again != ref {
		t.Fatalf("second store: %q %v", again, err)
	}
	if ents, _ := os.ReadDir(IconDir()); len(ents) != 1 {
		t.Fatalf("the same picture was stored %d times", len(ents))
	}

	formats := []struct{ ext, body string }{
		{".jpg", "\xFF\xD8\xFF\xE0\x00\x10JFIF"},
		{".gif", "GIF89a\x01\x00\x01\x00"},
		{".webp", "RIFF\x00\x00\x00\x00WEBPVP8 "},
		{".ico", "\x00\x00\x01\x00\x01\x00"},
		{".svg", `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`},
	}
	for _, f := range formats {
		ref, err := StoreIcon([]byte(f.body))
		if err != nil || !strings.HasSuffix(ref, f.ext) {
			t.Errorf("%s: %q %v", f.ext, ref, err)
		}
	}

	refused := map[string][]byte{
		"empty":   nil,
		"text":    []byte("just words"),
		"html":    []byte("<html><body/></html>"),
		"too big": s1hPNG(MaxIcon),
	}
	for what, data := range refused {
		if _, err := StoreIcon(data); err == nil {
			t.Errorf("%s was accepted", what)
		}
	}
	if _, err := StoreIcon(s1hPNG(MaxIcon - 8)); err != nil {
		t.Errorf("an image of exactly MaxIcon bytes was refused: %v", err)
	}
}

func TestS1HIconFileAcceptsOnlyStoredNames(t *testing.T) {
	s1hSandbox(t)
	good := "0123456789abcdef.png"
	if p := IconFile(good); p == "" || filepath.Dir(p) != IconDir() {
		t.Fatalf("IconFile(%q) = %q", good, IconFile(good))
	}
	for _, bad := range []string{
		"", "../0123456789abcdef.png", "sub/0123456789abcdef.png", `..\0123456789abcdef.png`,
		"0123456789abcdef.exe", "0123456789ABCDEF.png", "0123456789abcde.png",
		"file:0123456789abcdef.png", "0123456789abcdef.png/", "passwd",
	} {
		if p := IconFile(bad); p != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, p)
		}
	}
}

func TestS1HSaveKeepsReferencedIconsAndPrunesOldUnusedOnes(t *testing.T) {
	s1hSandbox(t)
	const relay = "http://127.0.0.1:1/v1"
	used, err := StoreIcon(s1hPNG(10))
	if err != nil {
		t.Fatal(err)
	}
	usedPath := IconFile(strings.TrimPrefix(used, "file:"))
	old, err := StoreIcon(s1hPNG(20))
	if err != nil {
		t.Fatal(err)
	}
	oldPath := IconFile(strings.TrimPrefix(old, "file:"))
	fresh, err := StoreIcon(s1hPNG(30))
	if err != nil {
		t.Fatal(err)
	}
	freshPath := IconFile(strings.TrimPrefix(fresh, "file:"))
	past := time.Now().Add(-2 * time.Hour)
	for _, p := range []string{usedPath, oldPath} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}

	if err := Save(Provider{Name: "S1H Icon", Chat: relay, Key: "k", Icon: used}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("an old unreferenced icon survived a save: %v", err)
	}
	if _, err := os.Stat(usedPath); err != nil {
		t.Errorf("a referenced icon was deleted: %v", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Errorf("an icon from the last hour was deleted: %v", err)
	}
	if p, _ := Find("s1h-icon"); p == nil || p.Icon != used {
		t.Errorf("icon not kept on the provider: %+v", p)
	}
	if err := Save(Provider{Name: "S1H Bad Icon", Chat: relay, Key: "k", Icon: "file:../../etc/passwd"}); err == nil {
		t.Error("an icon reference outside the icons folder was accepted")
	}
}

func TestS1HFallbackIsCleanedAndKeptOnAccounts(t *testing.T) {
	s1hSandbox(t)
	if err := Save(Provider{Name: "S1H FB", Chat: "http://127.0.0.1:1/v1", Key: "k",
		Fallback: []string{" deepseek/deepseek-chat ", "", "deepseek/deepseek-chat", "openai/gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("s1h-fb")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Fallback, "|") != "deepseek/deepseek-chat|openai/gpt-5.5" {
		t.Fatalf("fallback: %q", p.Fallback)
	}

	signIn(t)
	if err := Save(Provider{ID: "codex", Fallback: []string{"x/y", "x/y", " "}}); err != nil {
		t.Fatal(err)
	}
	acct, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if acct.Account == nil || strings.Join(acct.Fallback, "|") != "x/y" {
		t.Fatalf("account fallback: account=%v fallback=%q", acct.Account != nil, acct.Fallback)
	}
}

func s1hWriteCatalog(t *testing.T, body string) {
	t.Helper()
	p := catalog.CachePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
}

func TestS1HCatalogsSearchedInOrderWithPrefixedIDs(t *testing.T) {
	s1hSandbox(t)
	s1hWriteCatalog(t, `{
	  "openai": {"id":"openai","name":"OpenAI","models":{
	    "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","reasoning_options":[{"type":"effort","values":["low","high"]}]}}},
	  "deepseek": {"id":"deepseek","name":"DeepSeek","models":{
	    "deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat"}}}
	}`)
	if err := Save(Provider{Name: "S1H Multi", Chat: "http://127.0.0.1:1/v1", Key: "k", Catalog: " openai, ,deepseek,openai"}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("s1h-multi")
	if err != nil {
		t.Fatal(err)
	}
	if q.Catalog != "openai,deepseek" || strings.Join(q.Catalogs(), ",") != "openai,deepseek" {
		t.Fatalf("catalog: %q", q.Catalog)
	}
	byID := map[string]catalog.Model{}
	for _, m := range q.Available() {
		byID[m.ID] = m
	}
	if _, ok := byID["gpt-5.5"]; !ok {
		t.Fatalf("openai's model missing: %v", byID)
	}
	if _, ok := byID["deepseek-chat"]; !ok {
		t.Fatalf("deepseek's model missing: %v", byID)
	}

	if err := catalog.SaveLive(q.ID, q.Chat, []catalog.Model{
		{ID: "deepseek/deepseek-chat"}, {ID: "gpt-5.5"}, {ID: "vendor/unknown"},
	}); err != nil {
		t.Fatal(err)
	}
	got := map[string]catalog.Model{}
	for _, m := range q.Available() {
		got[m.ID] = m
	}
	if d := got["deepseek/deepseek-chat"]; d.Name != "DeepSeek Chat" {
		t.Errorf("prefixed model name: %+v", d)
	}
	if g := got["gpt-5.5"]; g.Name != "GPT-5.5" || len(g.Efforts) != 2 {
		t.Errorf("gpt-5.5: %+v", g)
	}
	if u := got["vendor/unknown"]; u.Name != "" || len(u.Efforts) != 0 {
		t.Errorf("unmatched model should carry nothing from the catalog: %+v", u)
	}
}
