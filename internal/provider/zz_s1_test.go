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

	"github.com/yetone/magpie/internal/catalog"
)

func zzCfgHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

var zzPngData = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1, 2, 3}, 20)...)

func TestZZStoreIcon(t *testing.T) {
	zzCfgHome(t)
	v, err := StoreIcon(zzPngData)
	if err != nil || !strings.HasPrefix(v, "file:") || !strings.HasSuffix(v, ".png") {
		t.Fatalf("%q %v", v, err)
	}
	name := strings.TrimPrefix(v, "file:")
	if len(name) != 16+4 {
		t.Fatalf("name %q", name)
	}
	v2, _ := StoreIcon(zzPngData)
	if v2 != v {
		t.Fatal("not deduplicated")
	}
	if ents, _ := os.ReadDir(IconDir()); len(ents) != 1 {
		t.Fatalf("%d files", len(ents))
	}
	if f := IconFile(name); f == "" {
		t.Fatal("no path")
	} else if _, err := os.Stat(f); err != nil {
		t.Fatal(err)
	}
	svg, err := StoreIcon([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`))
	if err != nil || !strings.HasSuffix(svg, ".svg") {
		t.Fatalf("%q %v", svg, err)
	}
	for _, bad := range [][]byte{nil, []byte("hello world"), bytes.Repeat([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, MaxIcon/8+2)} {
		if _, err := StoreIcon(bad); err == nil {
			t.Errorf("accepted %d bytes", len(bad))
		}
	}
	for _, n := range []string{"", "../x.png", "0123456789abcdef.txt", "0123456789abcdef", "0123456789ABCDEF.png", "a/0123456789abcdef.png", "..", "0123456789abcdef.png/.."} {
		if IconFile(n) != "" {
			t.Errorf("IconFile(%q) resolved", n)
		}
	}
}

func TestZZIconPruneOnSave(t *testing.T) {
	zzCfgHome(t)
	used, _ := StoreIcon(zzPngData)
	old, _ := StoreIcon([]byte("\x89PNG\r\n\x1a\nold"))
	fresh, _ := StoreIcon([]byte("\x89PNG\r\n\x1a\nfresh"))
	stale, _ := StoreIcon([]byte("\x89PNG\r\n\x1a\nstale"))
	past := time.Now().Add(-2 * time.Hour)
	for _, v := range []string{used, old, stale} {
		os.Chtimes(IconFile(strings.TrimPrefix(v, "file:")), past, past)
	}
	if err := Save(Provider{Name: "Mine", Chat: "http://x/v1", Key: "k", Icon: used}); err != nil {
		t.Fatal(err)
	}
	has := func(v string) bool {
		_, err := os.Stat(IconFile(strings.TrimPrefix(v, "file:")))
		return err == nil
	}
	if !has(used) || !has(fresh) || has(old) || has(stale) {
		t.Fatalf("used=%v fresh=%v old=%v stale=%v", has(used), has(fresh), has(old), has(stale))
	}
	if err := Save(Provider{Name: "Bad", Chat: "http://x/v1", Key: "k", Icon: "file:../../etc/passwd"}); err == nil {
		t.Fatal("traversal icon accepted")
	}
}

func TestZZSetIcon(t *testing.T) {
	zzCfgHome(t)
	Save(Provider{Name: "Mine", Chat: "http://x/v1", Key: "k"})
	pr, _ := FromPreset("deepseek")
	pr.Key = "k"
	Save(pr)
	if err := SetIcon("deepseek", "openai"); err == nil || !strings.Contains(err.Error(), "仅自定义供应商可设置") {
		t.Fatalf("%v", err)
	}
	if err := SetIcon("mine", "openai"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("mine"); p.Icon != "openai" {
		t.Fatal(p.Icon)
	}
	if err := SetIcon("mine", ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("mine"); p.Icon != "" {
		t.Fatal(p.Icon)
	}
}

func TestZZHeadersAndFallbackConfig(t *testing.T) {
	zzCfgHome(t)
	err := Save(Provider{Name: "Mine", Chat: "http://x/v1", Key: "k",
		Headers:  map[string]string{" X-Org-Id ": " acme ", "": "v", "  ": "w"},
		Fallback: []string{" a/b ", "", "a/b", "c/d"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := Find("mine")
	if len(p.Headers) != 1 || p.Headers["X-Org-Id"] != "acme" {
		t.Fatalf("%v", p.Headers)
	}
	if strings.Join(p.Fallback, "|") != "a/b|c/d" {
		t.Fatalf("%v", p.Fallback)
	}
	Save(Provider{Name: "Empty", Chat: "http://x/v1", Key: "k", Headers: map[string]string{"": "v"}})
	if b, _ := os.ReadFile(Path()); strings.Count(string(b), `"headers"`) != 1 {
		t.Fatalf("empty map written:\n%s", b)
	}
	pr, _ := FromPreset("deepseek")
	pr.Key = "k"
	pr.Headers = map[string]string{"X-A": "b"}
	Save(pr)
	if d, _ := Find("deepseek"); len(d.Headers) != 0 {
		t.Fatal("preset kept headers")
	}
}

func TestZZHeadersSent(t *testing.T) {
	zzCfgHome(t)
	var got []http.Header
	var raw [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Clone())
		raw = append(raw, r.Header.Values("X-Org-Id"))
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"data":[{"id":"m1"}]}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := normalize(Provider{ID: "mine", Name: "Mine", Chat: srv.URL + "/v1", Key: "k", Headers: map[string]string{"X-Org-Id": "acme", "authorization": "Bearer other"}})
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	res := p.Test(context.Background())
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("%+v", res)
	}
	if len(got) < 2 {
		t.Fatalf("%d requests", len(got))
	}
	for i, h := range got {
		if h.Get("X-Org-Id") != "acme" || len(raw[i]) != 1 {
			t.Errorf("request %d: %v", i, h)
		}
		if h.Get("Authorization") != "Bearer other" {
			t.Errorf("request %d auth %q", i, h.Get("Authorization"))
		}
	}
}

func TestZZApplyHeadersKeepsCase(t *testing.T) {
	h := http.Header{}
	ApplyHeaders(h, map[string]string{"x-org-ID": "a"})
	if v, ok := h["x-org-ID"]; !ok || v[0] != "a" {
		t.Fatalf("%v", h)
	}
}

func TestZZAccountKeepsFallback(t *testing.T) {
	signIn(t)
	if err := SetFallback("codex", []string{"deepseek/deepseek-chat", "", "deepseek/deepseek-chat"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Fallback) != 1 || p.Fallback[0] != "deepseek/deepseek-chat" {
		t.Fatalf("%v", p.Fallback)
	}
}

func TestZZMultiCatalog(t *testing.T) {
	home := zzCfgHome(t)
	writeFile(t, filepath.Join(home, ".cache", "magpie", "models.json"), map[string]any{
		"openai": map[string]any{"id": "openai", "models": map[string]any{
			"gpt-5.5": map[string]any{"id": "gpt-5.5", "name": "GPT-5.5", "reasoning_options": []any{map[string]any{"type": "effort", "values": []string{"low", "high"}}}},
		}},
		"deepseek": map[string]any{"id": "deepseek", "models": map[string]any{
			"deepseek-chat": map[string]any{"id": "deepseek-chat", "name": "DeepSeek Chat", "reasoning_options": []any{map[string]any{"type": "effort", "values": []string{"high", "max"}}}},
		}},
	})
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	p := normalize(Provider{ID: "gw", Name: "GW", Chat: "http://x/v1", Key: "k", Catalog: " openai , deepseek,openai", Models: []string{"deepseek/deepseek-chat", "gpt-5.5", "mystery/x"}})
	if p.Catalog != "openai,deepseek" {
		t.Fatal(p.Catalog)
	}
	ex := p.Exposed()
	if len(ex) != 3 {
		t.Fatalf("%+v", ex)
	}
	if ex[0].ID != "deepseek/deepseek-chat" || ex[0].Name != "DeepSeek Chat" || len(ex[0].Efforts) != 2 {
		t.Errorf("%+v", ex[0])
	}
	if ex[1].Name != "GPT-5.5" || len(ex[1].Efforts) != 2 {
		t.Errorf("%+v", ex[1])
	}
	if ex[2].Name != "mystery/x" || len(ex[2].Efforts) != 0 {
		t.Errorf("%+v", ex[2])
	}
	// live list from a gateway: decorated the same way
	catalog.SaveLive("gw", "http://x", []catalog.Model{{ID: "deepseek/deepseek-chat", Name: "deepseek/deepseek-chat"}, {ID: "gpt-5.5"}, {ID: "other/zzz"}})
	av := p.Available()
	if av[0].Name != "DeepSeek Chat" || len(av[0].Efforts) != 2 || av[1].Name != "GPT-5.5" || av[2].Name != "" || len(av[2].Efforts) != 0 {
		t.Errorf("%+v", av)
	}
}
