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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// s1Isolate points HOME, the config and the cache at fresh directories so the
// test never reads the machine's agents or writes its real providers.json.
func s1Isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// s1PNG is an image-shaped byte slice with a PNG signature and a body that
// makes it unique to the test.
func s1PNG(body string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte(body)...)
}

// s1RawServer answers every request with a model list and records the raw
// request head, so header names can be checked byte for byte (net/http's
// server would canonicalize them on the way in).
func s1RawServer(t *testing.T) (string, func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var heads []string
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				var head strings.Builder
				length := 0
				for {
					line, err := r.ReadString('\n')
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
				io.CopyN(io.Discard, r, int64(length))
				mu.Lock()
				heads = append(heads, head.String())
				mu.Unlock()
				body := `{"data":[{"id":"s1-model"}]}`
				io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "+
					strconv.Itoa(len(body))+"\r\nConnection: close\r\n\r\n"+body)
			}(conn)
		}
	}()
	return "http://" + ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), heads...)
	}
}

func TestS1HeadersSentRawOnTestAndModelList(t *testing.T) {
	s1Isolate(t)
	base, heads := s1RawServer(t)
	p := Provider{ID: "s1-relay", Name: "S1 Relay", Key: "sk-s1", Chat: base + "/v1",
		Headers: map[string]string{" X-Org-Id ": " acme ", "": "dropped"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	saved, err := Find("s1-relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Headers) != 1 || saved.Headers["X-Org-Id"] != "acme" {
		t.Fatalf("headers after save: %#v", saved.Headers)
	}
	res := saved.Test(context.Background())
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("probe: %+v", res)
	}
	all := heads()
	if len(all) < 2 {
		t.Fatalf("want a model-list request and a probe, got %d", len(all))
	}
	for _, h := range all {
		if !strings.Contains(h, "\r\nX-Org-Id: acme\r\n") {
			t.Errorf("request lacks the raw header:\n%s", h)
		}
	}
}

func TestS1PresetIgnoresHeaders(t *testing.T) {
	s1Isolate(t)
	if err := Save(Provider{ID: "deepseek", Preset: "deepseek", Key: "sk-s1",
		Headers: map[string]string{"X-Org-Id": "acme"}}); err != nil {
		t.Fatal(err)
	}
	saved, err := Find("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Headers != nil {
		t.Fatalf("a preset kept headers: %#v", saved.Headers)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.deepseek.com/v1/chat/completions", nil)
	if err := saved.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Header["X-Org-Id"]; ok {
		t.Fatal("a preset sent a custom header")
	}
}

func TestS1EmptyHeaderMapIsNotWritten(t *testing.T) {
	s1Isolate(t)
	if err := Save(Provider{ID: "s1-plain", Key: "sk-s1", Chat: "https://relay.example/v1",
		Headers: map[string]string{"  ": "x"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("headers")) {
		t.Fatalf("an empty header map was written:\n%s", b)
	}
}

func TestS1HeaderOverridesDefaultCaseSensitively(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://relay.example/v1", nil)
	p := Provider{ID: "s1-auth", Key: "sk-s1", Chat: "https://relay.example/v1",
		Headers: map[string]string{"authorization": "Token abc", "X-Org-Id": "acme"}}
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header["authorization"]; len(got) != 1 || got[0] != "Token abc" {
		t.Fatalf("custom authorization: %v", got)
	}
	if _, ok := req.Header["Authorization"]; ok {
		t.Fatal("the default Authorization header was sent alongside the custom one")
	}
	if got := req.Header["X-Org-Id"]; len(got) != 1 || got[0] != "acme" {
		t.Fatalf("X-Org-Id: %v", got)
	}
}

func TestS1StoreIconRules(t *testing.T) {
	s1Isolate(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("an empty icon was accepted")
	}
	if _, err := StoreIcon([]byte("just some text, not an image")); err == nil {
		t.Error("plain text was accepted as an icon")
	}
	if _, err := StoreIcon(append(s1PNG("x"), make([]byte, MaxIcon)...)); err == nil {
		t.Error("an icon over 1 MB was accepted")
	}
	exact := append(s1PNG("edge"), make([]byte, MaxIcon-len(s1PNG("edge")))...)
	if _, err := StoreIcon(exact); err != nil {
		t.Errorf("an icon of exactly 1 MB was refused: %v", err)
	}

	png := s1PNG("s1-logo")
	ref, err := StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(ref, "file:")
	if !regexp.MustCompile(`^file:[0-9a-f]{16}\.png$`).MatchString(ref) {
		t.Fatalf("icon value %q", ref)
	}
	again, err := StoreIcon(png)
	if err != nil || again != ref {
		t.Fatalf("second store: %q, %v", again, err)
	}
	entries, _ := os.ReadDir(iconDir())
	if len(entries) != 2 { // the exact-size image and this one
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("stored files: %v", names)
	}
	b, err := os.ReadFile(IconFile(name))
	if err != nil || !bytes.Equal(b, png) {
		t.Fatalf("stored content differs: %v", err)
	}

	svg, err := StoreIcon([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`))
	if err != nil || !strings.HasSuffix(svg, ".svg") {
		t.Fatalf("svg: %q, %v", svg, err)
	}
}

func TestS1IconFileOnlyAcceptsStoredNames(t *testing.T) {
	s1Isolate(t)
	good := "0123456789abcdef.png"
	if IconFile(good) == "" {
		t.Fatalf("%s was refused", good)
	}
	for _, bad := range []string{
		"", "../0123456789abcdef.png", "sub/0123456789abcdef.png", `..\0123456789abcdef.png`,
		"0123456789abcdef.exe", "0123456789ABCDEF.png", "0123456789abcdef.svgx",
		"123456789abcdef.png", "0123456789abcdef.png\n", "file:0123456789abcdef.png",
		"..", "/etc/passwd",
	} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
}

func TestS1SetIconOnlyForCustomProviders(t *testing.T) {
	s1Isolate(t)
	if err := Save(Provider{ID: "deepseek", Preset: "deepseek", Key: "sk-s1"}); err != nil {
		t.Fatal(err)
	}
	if err := SetIcon("deepseek", "claude-color"); err == nil {
		t.Fatal("a preset accepted an icon")
	}

	if err := Save(Provider{ID: "s1-relay", Name: "S1 Relay", Key: "sk-s1", Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	img := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(img, s1PNG("s1-picked"), 0o644); err != nil {
		t.Fatal(err)
	}
	icon, err := IconArg(img)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetIcon("s1-relay", icon); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("s1-relay"); p.Icon != icon {
		t.Fatalf("icon %q, want %q", p.Icon, icon)
	}
	if err := SetIcon("s1-relay", ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("s1-relay"); p.Icon != "" {
		t.Fatalf("icon not cleared: %q", p.Icon)
	}

	text := filepath.Join(dir, "notes.txt")
	os.WriteFile(text, []byte("not an icon"), 0o644)
	if _, err := IconArg(text); err == nil {
		t.Error("a text file was accepted as an icon")
	}
	if got, err := IconArg("claude-color"); err != nil || got != "claude-color" {
		t.Errorf("built-in name: %q, %v", got, err)
	}
	if err := Save(Provider{ID: "s1-bad", Key: "sk-s1", Chat: "https://relay.example/v1",
		Icon: "file:../../etc/passwd"}); err == nil {
		t.Error("a reference outside icons/ was saved")
	}
}

func TestS1PruneIconsKeepsReferencedAndFresh(t *testing.T) {
	s1Isolate(t)
	used, _ := StoreIcon(s1PNG("s1-used"))
	orphan, _ := StoreIcon(s1PNG("s1-orphan"))
	fresh, _ := StoreIcon(s1PNG("s1-fresh"))
	old := time.Now().Add(-2 * time.Hour)
	for _, ref := range []string{used, orphan} {
		if err := os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(Provider{ID: "s1-relay", Key: "sk-s1", Chat: "https://relay.example/v1", Icon: used}); err != nil {
		t.Fatal(err)
	}
	exists := func(ref string) bool {
		_, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:")))
		return err == nil
	}
	if !exists(used) {
		t.Error("a referenced icon was removed")
	}
	if exists(orphan) {
		t.Error("an old unreferenced icon was kept")
	}
	if !exists(fresh) {
		t.Error("a fresh unreferenced icon was removed inside the grace period")
	}
}

func TestS1HeadersAndFallbackNormalize(t *testing.T) {
	s1Isolate(t)
	if err := Save(Provider{ID: "s1-relay", Key: "sk-s1", Chat: "https://relay.example/v1",
		Fallback: []string{" deepseek/deepseek-chat ", "", "deepseek/deepseek-chat", "openai/gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("s1-relay")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Fallback, "|") != "deepseek/deepseek-chat|openai/gpt-5.5" {
		t.Fatalf("fallback: %q", p.Fallback)
	}
}

func TestS1MultiCatalogAndPrefixedModels(t *testing.T) {
	s1Isolate(t)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	const cat = `{
	  "openai": {"id":"openai","name":"OpenAI","models":{
	    "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","reasoning_options":[{"type":"effort","values":["low","high"]}]}}},
	  "deepseek": {"id":"deepseek","name":"DeepSeek","models":{
	    "deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat"}}}
	}`
	if err := os.WriteFile(catalog.CachePath(), []byte(cat), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()

	if err := Save(Provider{ID: "s1-gw", Key: "sk-s1", Chat: "https://gw.example/v1", Catalog: " openai, deepseek ,"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("s1-gw")
	if err != nil {
		t.Fatal(err)
	}
	if p.Catalog != "openai,deepseek" {
		t.Fatalf("catalog: %q", p.Catalog)
	}
	byID := map[string]catalog.Model{}
	for _, m := range p.Available() {
		byID[m.ID] = m
	}
	if _, ok := byID["deepseek-chat"]; !ok {
		t.Fatalf("second catalog not searched: %v", byID)
	}

	if err := catalog.SaveLive(p.ID, "https://gw.example/v1", []catalog.Model{
		{ID: "deepseek/deepseek-chat"}, {ID: "gpt-5.5"}, {ID: "unknown/model"},
	}); err != nil {
		t.Fatal(err)
	}
	byID = map[string]catalog.Model{}
	for _, m := range p.Available() {
		byID[m.ID] = m
	}
	if m := byID["deepseek/deepseek-chat"]; m.Name != "DeepSeek Chat" {
		t.Errorf("prefixed model name: %+v", m)
	}
	if m := byID["gpt-5.5"]; m.Name != "GPT-5.5" || len(m.Efforts) != 2 {
		t.Errorf("gpt-5.5: %+v", m)
	}
	if m, ok := byID["unknown/model"]; !ok || m.Name != "" || m.Efforts != nil {
		t.Errorf("unmatched model should pass through bare: %+v", m)
	}
}
