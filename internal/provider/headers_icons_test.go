package provider

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// useConfig points magpie's config and cache at a fresh temp home.
func useConfig(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

func TestCustomHeadersKeepCaseAndOverrideAuth(t *testing.T) {
	useConfig(t)
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-1",
		Headers: map[string]string{"X-Org-Id": "acme", "authorization": "Bearer custom"}}
	req, _ := http.NewRequest(http.MethodPost, p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header["X-Org-Id"]; !reflect.DeepEqual(got, []string{"acme"}) {
		t.Fatalf("X-Org-Id = %q (header map %v)", got, req.Header)
	}
	if got := req.Header["authorization"]; !reflect.DeepEqual(got, []string{"Bearer custom"}) {
		t.Fatalf("authorization = %q", got)
	}
	if _, ok := req.Header["Authorization"]; ok {
		t.Fatalf("default Authorization survived the override: %v", req.Header)
	}
}

func TestPresetsIgnoreHeaders(t *testing.T) {
	useConfig(t)
	p, err := FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "sk-1"
	p.Headers = map[string]string{"X-Org-Id": "acme"}
	req, _ := http.NewRequest(http.MethodPost, p.Chat, nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Header["X-Org-Id"]; ok || p.CustomHeaders() != nil {
		t.Fatalf("preset sent custom headers: %v", req.Header)
	}
}

func TestHeadersReachModelListFetch(t *testing.T) {
	useConfig(t)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Org-Id")
		w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()
	ms, err := catalog.FetchWith(context.Background(), srv.URL+"/v1", "sk", false, map[string]string{"X-Org-Id": "acme"})
	if err != nil || len(ms) != 1 {
		t.Fatalf("fetch: %v %v", ms, err)
	}
	if got != "acme" {
		t.Fatalf("model list request sent X-Org-Id %q", got)
	}
}

func TestHeadersSaveTrimAndDrop(t *testing.T) {
	useConfig(t)
	if err := Save(Provider{Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-1",
		Headers: map[string]string{"  X-Org-Id ": "  acme  ", "   ": "dropped", "X-Empty": "   "}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-Org-Id": "acme", "X-Empty": ""}
	if !reflect.DeepEqual(p.Headers, want) {
		t.Fatalf("headers = %v, want %v", p.Headers, want)
	}
	if err := Save(Provider{Name: "Plain", Chat: "https://plain.example/v1", Key: "sk-2", Headers: map[string]string{" ": "x"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), `"headers"`) && strings.Count(string(b), `"headers"`) != 1 {
		t.Fatalf("empty header map was written: %s", b)
	}
}

func TestStoreIconFormatsAndDedupe(t *testing.T) {
	useConfig(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	ref, err := StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") || len(ref) != len("file:")+16+len(".png") {
		t.Fatalf("icon ref %q", ref)
	}
	again, err := StoreIcon(png)
	if err != nil || again != ref {
		t.Fatalf("second store = %q, %v", again, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(IconFile(strings.TrimPrefix(ref, "file:"))))
	if len(entries) != 1 {
		t.Fatalf("same picture stored %d times", len(entries))
	}
	svg, err := StoreIcon([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`))
	if err != nil || !strings.HasSuffix(svg, ".svg") {
		t.Fatalf("svg = %q, %v", svg, err)
	}
	for _, bad := range [][]byte{nil, []byte("hello, plain text"), []byte("<html><body>x</body></html>"), bytes.Repeat([]byte("a"), MaxIcon+1)} {
		if _, err := StoreIcon(bad); err == nil {
			t.Errorf("accepted %d bytes of %q", len(bad), bad[:min(len(bad), 20)])
		}
	}
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxIcon)...)
	if _, err := StoreIcon(big); err == nil {
		t.Errorf("accepted an icon over 1 MB")
	}
}

func TestIconFileRejectsOtherNames(t *testing.T) {
	useConfig(t)
	ok := "0123456789abcdef.png"
	if got := IconFile(ok); got == "" || filepath.Base(got) != ok {
		t.Fatalf("IconFile(%q) = %q", ok, got)
	}
	for _, bad := range []string{"", "../0123456789abcdef.png", "0123456789abcdef.png/../x", "a/0123456789abcdef.svg",
		"0123456789abcdef.jpeg", "0123456789ABCDEF.png", "0123456789abcde.png", "..", "../../providers.json"} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
}

func TestSaveRemovesOnlyStaleUnusedIcons(t *testing.T) {
	useConfig(t)
	stale, _ := StoreIcon([]byte("\x89PNG\r\n\x1a\nstale"))
	kept, _ := StoreIcon([]byte("\x89PNG\r\n\x1a\nused"))
	fresh, _ := StoreIcon([]byte("\x89PNG\r\n\x1a\nfresh"))
	old := time.Now().Add(-2 * time.Hour)
	for _, ref := range []string{stale, kept} {
		if err := os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(Provider{Name: "Mine", Chat: "https://mine.example/v1", Key: "k", Icon: kept}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(stale, "file:"))); !os.IsNotExist(err) {
		t.Errorf("stale unused icon was kept")
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(kept, "file:"))); err != nil {
		t.Errorf("referenced icon was removed: %v", err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(fresh, "file:"))); err != nil {
		t.Errorf("fresh unused icon was removed: %v", err)
	}
}

func TestSaveRejectsBadIconReference(t *testing.T) {
	useConfig(t)
	if err := Save(Provider{Name: "Bad", Chat: "https://bad.example/v1", Key: "k", Icon: "file:../../providers.json"}); err == nil {
		t.Fatal("accepted an icon reference outside icons/")
	}
}

func TestFallbackCleanedAndKeptOnAccounts(t *testing.T) {
	signIn(t)
	if err := Save(Provider{Name: "Relay", Chat: "https://relay.example/v1", Key: "k",
		Fallback: []string{" openai/gpt-5.5 ", "", "openai/gpt-5.5", "deepseek/deepseek-chat "}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"openai/gpt-5.5", "deepseek/deepseek-chat"}; !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("fallback = %q, want %q", p.Fallback, want)
	}
	if err := Save(Provider{ID: "codex", Fallback: []string{"relay/gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	c, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if c.Account == nil || !reflect.DeepEqual(c.Fallback, []string{"relay/gpt-5.5"}) {
		t.Fatalf("account fallback = %+v", c)
	}
}

func TestCatalogListsMatchPrefixedIDs(t *testing.T) {
	useConfig(t)
	cache := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "magpie")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
	  "openai": {"id":"openai","name":"OpenAI","models":{
	    "gpt-5.5":{"id":"gpt-5.5","name":"GPT-5.5","reasoning_options":[{"type":"effort","values":["low","high"]}]}}},
	  "deepseek": {"id":"deepseek","name":"DeepSeek","models":{
	    "deepseek-chat":{"id":"deepseek-chat","name":"DeepSeek Chat"}}}
	}`
	if err := os.WriteFile(catalog.CachePath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	p := Provider{ID: "gw", Name: "GW", Chat: "https://gw.example/v1", Key: "k",
		Catalog: " openai , deepseek ",
		Models:  []string{"deepseek/deepseek-chat", "gpt-5.5", "unknown/thing"}}
	p = normalize(p)
	if p.Catalog != "openai,deepseek" {
		t.Fatalf("catalog = %q", p.Catalog)
	}
	got := map[string]catalog.Model{}
	for _, m := range p.Exposed() {
		got[m.ID] = m
	}
	if m := got["deepseek/deepseek-chat"]; m.Name != "DeepSeek Chat" {
		t.Errorf("deepseek/deepseek-chat = %+v", m)
	}
	if m := got["gpt-5.5"]; m.Name != "GPT-5.5" || !reflect.DeepEqual(m.Efforts, []string{"low", "high"}) {
		t.Errorf("gpt-5.5 = %+v", m)
	}
	if m := got["unknown/thing"]; m.Name != "unknown/thing" || m.Efforts != nil {
		t.Errorf("unmatched model = %+v", m)
	}
}

func TestValidCatalogs(t *testing.T) {
	for s, want := range map[string]bool{"": true, "openai": true, "openai,deepseek": true, "Bad Name": false, "ok,Not Ok": false} {
		if got := validCatalogs(s); got != want {
			t.Errorf("validCatalogs(%q) = %v", s, got)
		}
	}
}
