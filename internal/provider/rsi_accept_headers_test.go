package provider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

func isolate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func TestAcceptHeadersNormalized(t *testing.T) {
	isolate(t)
	err := provider.Save(provider.Provider{
		ID: "relay", Name: "Relay", Key: "sk-1", Chat: "https://relay.example.com/v1",
		Headers: map[string]string{" X-Org ": " acme ", "  ": "x", "": "y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 1 || p.Headers["X-Org"] != "acme" {
		t.Fatalf("headers = %#v", p.Headers)
	}
	b, _ := os.ReadFile(provider.Path())
	if !strings.Contains(string(b), `"headers"`) {
		t.Errorf("headers missing from json: %s", b)
	}

	if err := provider.Save(provider.Provider{
		ID: "empty", Name: "Empty", Key: "sk-1", Chat: "https://e.example.com/v1",
		Headers: map[string]string{" ": "x"},
	}); err != nil {
		t.Fatal(err)
	}
	e, _ := provider.Find("empty")
	if e.Headers != nil {
		t.Errorf("headers = %#v, want nil", e.Headers)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	b, _ = os.ReadFile(provider.Path())
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, m := range f.Providers {
		if m["id"] == "empty" {
			if _, ok := m["headers"]; ok {
				t.Errorf("empty headers serialized: %v", m)
			}
		}
	}
}

func TestAcceptHeadersPresetCleared(t *testing.T) {
	isolate(t)
	p, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Skip("no deepseek preset")
	}
	p.Key = "sk-1"
	p.Headers = map[string]string{"X-A": "1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := provider.Find(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Headers) != 0 {
		t.Errorf("preset kept headers: %#v", got.Headers)
	}
}

func TestAcceptSignAppliesHeaders(t *testing.T) {
	p := provider.Provider{
		ID: "relay", Name: "Relay", Key: "sk-1", Chat: "https://relay.example.com/v1",
		Headers: map[string]string{"x-foo": "bar", "Authorization": "Token zzz"},
	}
	req, _ := http.NewRequest("POST", "https://relay.example.com/v1/chat/completions", nil)
	if err := p.Sign(context.Background(), req, provider.Chat, nil); err != nil {
		t.Fatal(err)
	}
	if v := req.Header["x-foo"]; len(v) != 1 || v[0] != "bar" {
		t.Errorf("x-foo = %v (header map %v)", v, req.Header)
	}
	if got := req.Header.Get("Authorization"); got != "Token zzz" {
		t.Errorf("Authorization = %q", got)
	}

	// no override: default auth is intact
	q := provider.Provider{ID: "r", Name: "R", Key: "sk-1", Chat: "https://r.example.com/v1", Headers: map[string]string{"X-Org": "acme"}}
	req, _ = http.NewRequest("POST", "https://r.example.com/v1/chat/completions", nil)
	q.Sign(context.Background(), req, provider.Chat, nil)
	if req.Header.Get("Authorization") != "Bearer sk-1" || req.Header["X-Org"][0] != "acme" {
		t.Errorf("headers = %v", req.Header)
	}
}

func TestAcceptCatalogFetchHeaders(t *testing.T) {
	isolate(t)
	var mu sync.Mutex
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()

	ms, err := catalog.Fetch(context.Background(), srv.URL+"/v1", "sk-1", false, map[string]string{"x-org": "acme", "Authorization": "Custom abc"})
	if err != nil || len(ms) == 0 {
		t.Fatalf("fetch: %v %v", ms, err)
	}
	h := seen[0]
	if h.Get("x-org") != "acme" {
		t.Errorf("x-org missing: %v", h)
	}
	if h.Get("Authorization") != "Custom abc" {
		t.Errorf("Authorization = %q", h.Get("Authorization"))
	}

	seen = nil
	if _, err := catalog.Fetch(context.Background(), srv.URL+"/v1", "sk-1", false, nil); err != nil {
		t.Fatal(err)
	}
	if seen[0].Get("Authorization") != "Bearer sk-1" {
		t.Errorf("default auth = %q", seen[0].Get("Authorization"))
	}
}

func TestAcceptProviderFetchHeaders(t *testing.T) {
	isolate(t)
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get("X-Org-Id"))
		mu.Unlock()
		w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()
	p := provider.Provider{ID: "relay", Name: "Relay", Key: "sk-1", Chat: srv.URL + "/v1", Headers: map[string]string{"X-Org-Id": "acme"}}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0] != "acme" {
		t.Errorf("seen header values %v", got)
	}
}
