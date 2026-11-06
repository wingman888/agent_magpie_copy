package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// isolateHome points every magpie path at a fresh temp dir.
func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/cfg")
	t.Setenv("XDG_CACHE_HOME", home+"/cache")
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// rawServer answers each request with a 200 and records the header lines
// exactly as they arrived on the wire (case included).
func rawServer(t *testing.T) (string, chan []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan []string, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				var lines []string
				length := 0
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
					if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
						fmt.Sscan(strings.TrimSpace(v), &length)
					}
				}
				io.CopyN(io.Discard, r, int64(length))
				got <- lines
				io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
			}(conn)
		}
	}()
	return "http://" + ln.Addr().String(), got
}

func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func TestCustomHeadersGoOutWithTheirCase(t *testing.T) {
	isolateHome(t)
	base, got := rawServer(t)
	p := Provider{ID: "relay", Chat: base + "/v1", Key: "sk-1", Headers: map[string]string{"X-Org-Id": "acme"}}
	req, _ := http.NewRequest(http.MethodPost, p.Chat+"/chat/completions", strings.NewReader(`{}`))
	if err := p.Sign(context.Background(), req, Chat, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	lines := <-got
	if !hasLine(lines, "X-Org-Id: acme") {
		t.Fatalf("header not sent with its case; wire had %q", lines)
	}
	if !hasLine(lines, "Authorization: Bearer sk-1") {
		t.Fatalf("auth header missing: %q", lines)
	}
}

func TestCustomHeaderOverridesAuthHeader(t *testing.T) {
	p := Provider{ID: "relay", Chat: "https://x/v1", Key: "sk-1", Headers: map[string]string{"Authorization": "Token abc"}}
	req, _ := http.NewRequest(http.MethodPost, p.Chat, nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if v := req.Header["Authorization"]; len(v) != 1 || v[0] != "Token abc" {
		t.Fatalf("custom header should win over the auth header: %q", v)
	}
}

func TestPresetsAndAccountsSendNoCustomHeaders(t *testing.T) {
	preset := Provider{ID: "deepseek", Preset: "deepseek", Chat: "https://x/v1", Key: "k", Headers: map[string]string{"X-A": "b"}}
	if h := preset.customHeaders(); h != nil {
		t.Fatalf("preset sent headers: %v", h)
	}
	acct := Provider{ID: "codex", Chat: "https://x/v1", Account: &Account{Agent: "codex"}, Headers: map[string]string{"X-A": "b"}}
	if h := acct.customHeaders(); h != nil {
		t.Fatalf("account sent headers: %v", h)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://x/v1", nil)
	applyHeaders(req, preset)
	if len(req.Header) != 0 {
		t.Fatalf("preset request got headers: %v", req.Header)
	}
}

func TestFetchAndTestCarryHeaders(t *testing.T) {
	isolateHome(t)
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Method+" "+r.URL.Path] = r.Header.Get("X-Org-Id")
		mu.Unlock()
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"data":[{"id":"m1"}]}`)
			return
		}
		io.WriteString(w, `{"id":"x"}`)
	}))
	defer srv.Close()
	p := Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "k", Models: []string{"m1"}, Headers: map[string]string{"X-Org-Id": "acme"}}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, r := range p.Test(context.Background()) {
		if !r.OK {
			t.Fatalf("probe failed: %+v", r)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for k, v := range seen {
		if v != "acme" {
			t.Errorf("%s got X-Org-Id %q", k, v)
		}
	}
	if len(seen) < 2 {
		t.Fatalf("expected model list and test requests, saw %v", seen)
	}
}

func TestSaveKeepsHeadersAndDropsEmptyOnes(t *testing.T) {
	isolateHome(t)
	if err := Save(Provider{Name: "My Relay", Chat: "https://relay.example.com/v1", Key: "k",
		Headers: map[string]string{"  X-Org-Id ": " acme ", "": "dropped", "X-Empty": "  "}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("my-relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Headers["X-Org-Id"] != "acme" || len(p.Headers) != 2 || p.Headers["X-Empty"] != "" {
		t.Fatalf("headers after save: %#v", p.Headers)
	}
	if err := Save(Provider{Name: "Bare", Chat: "https://bare.example.com/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	for _, x := range load().Providers {
		if x.ID == "bare" && x.Headers != nil {
			t.Fatalf("empty headers stored: %#v", x.Headers)
		}
	}
}

func TestCleanHeadersEmptyIsNil(t *testing.T) {
	if cleanHeaders(map[string]string{" ": "x"}, true) != nil {
		t.Fatal("only-nameless headers should clean to nil")
	}
	if cleanHeaders(map[string]string{"A": "b"}, false) != nil {
		t.Fatal("presets must not keep headers")
	}
}

func TestFallbackCleanedAndKeptOnAccounts(t *testing.T) {
	isolateHome(t)
	if err := Save(Provider{Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k",
		Fallback: []string{" openai/gpt-5.5 ", "", "openai/gpt-5.5", "deepseek/deepseek-chat"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Fallback, "|") != "openai/gpt-5.5|deepseek/deepseek-chat" {
		t.Fatalf("fallback = %q", p.Fallback)
	}
	// an account keeps its fallback list through Save
	n := normalize(Provider{ID: "claude", Fallback: []string{"a/b", "a/b", " "}})
	if strings.Join(n.Fallback, "|") != "a/b" {
		t.Fatalf("normalize fallback = %q", n.Fallback)
	}
}

func TestMultiCatalogNamesAndEffortsWithPrefix(t *testing.T) {
	isolateHome(t)
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(catalog.CachePath(), `{
	  "openai": {"id":"openai","name":"OpenAI","env":[],"models":{
	     "gpt-5.5":{"id":"gpt-5.5","name":"GPT-5.5","reasoning_options":[{"type":"effort","values":["low","high"]}]}}},
	  "deepseek": {"id":"deepseek","name":"DeepSeek","env":[],"models":{
	     "deepseek-chat":{"id":"deepseek-chat","name":"DeepSeek Chat"}}}
	}`)
	catalog.Reset()
	p := Provider{ID: "gw", Chat: "https://gw.example.com/v1", Key: "k", Catalog: " openai, deepseek,",
		Models: []string{"deepseek/deepseek-chat", "gpt-5.5", "vendor/unknown"}}
	p = normalize(p)
	if p.Catalog != "openai,deepseek" {
		t.Fatalf("catalog = %q", p.Catalog)
	}
	ex := p.Exposed()
	if len(ex) != 3 {
		t.Fatalf("exposed = %+v", ex)
	}
	if ex[0].ID != "deepseek/deepseek-chat" || ex[0].Name != "DeepSeek Chat" {
		t.Errorf("prefixed model: %+v", ex[0])
	}
	if ex[1].ID != "gpt-5.5" || ex[1].Name != "GPT-5.5" || strings.Join(ex[1].Efforts, ",") != "low,high" {
		t.Errorf("gpt-5.5: %+v", ex[1])
	}
	if ex[2].ID != "vendor/unknown" || len(ex[2].Efforts) != 0 {
		t.Errorf("unmatched model should pass through bare: %+v", ex[2])
	}
	if !validCatalogs("openai,deepseek") || validCatalogs("Open AI") || validCatalogs(",") {
		t.Error("validCatalogs rejects good lists or accepts bad ones")
	}
}
