package provider_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

type raw struct {
	mu    sync.Mutex
	lines []string
	url   string
}

func (u *raw) has(line string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, l := range u.lines {
		if l == line {
			return true
		}
	}
	return false
}

func (u *raw) any(prefix string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, l := range u.lines {
		if strings.HasPrefix(strings.ToLower(l), strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}

func startRaw(t *testing.T) *raw {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	u := &raw{url: "http://" + ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				first, _ := r.ReadString('\n')
				path := strings.Fields(first)
				cl := 0
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					l = strings.TrimRight(l, "\r\n")
					if l == "" {
						break
					}
					u.mu.Lock()
					u.lines = append(u.lines, l)
					u.mu.Unlock()
					if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(k, "content-length") {
						for _, ch := range strings.TrimSpace(v) {
							cl = cl*10 + int(ch-'0')
						}
					}
				}
				for i := 0; i < cl; i++ {
					r.ReadByte()
				}
				body := `{"id":"x","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
				if len(path) > 1 && strings.HasSuffix(path[1], "/models") {
					body = `{"object":"list","data":[{"id":"m1","object":"model"}]}`
				}
				c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nConnection: close\r\nContent-Length: " + itoa(len(body)) + "\r\n\r\n" + body))
			}()
		}
	}()
	return u
}

func itoa(n int) string {
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func writeConfig(t *testing.T, ps ...map[string]any) {
	t.Helper()
	p := provider.Path()
	os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.Marshal(map[string]any{"providers": ps})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptCustomHeadersOnFetchAndTest(t *testing.T) {
	isolate(t)
	up := startRaw(t)
	writeConfig(t, map[string]any{
		"id": "relay", "name": "Relay", "key": "sk-abc", "chat": up.url + "/v1", "models": []string{"m1"},
		"headers": map[string]string{"X-ORG-id": "acme", "Authorization": "Token mine", " X-Pad ": "  v  ", "  ": "dropped"},
	})
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := p.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"X-ORG-id: acme", "Authorization: Token mine", "X-Pad: v"} {
		if !up.has(want) {
			t.Errorf("model fetch lacks %q; saw %q", want, up.lines)
		}
	}
	up.mu.Lock()
	up.lines = nil
	up.mu.Unlock()
	for _, r := range p.Test(ctx) {
		if !r.OK {
			t.Errorf("probe failed: %+v", r)
		}
	}
	for _, want := range []string{"X-ORG-id: acme", "Authorization: Token mine", "X-Pad: v"} {
		if !up.has(want) {
			t.Errorf("test probe lacks %q; saw %q", want, up.lines)
		}
	}
	if up.has("Authorization: Bearer sk-abc") {
		t.Error("auth not overridden")
	}
}

func TestAcceptPresetIgnoresHeaders(t *testing.T) {
	isolate(t)
	up := startRaw(t)
	writeConfig(t, map[string]any{
		"id": "deepseek", "name": "DeepSeek", "preset": "deepseek", "key": "sk-abc", "chat": up.url + "/v1", "models": []string{"m1"},
		"headers": map[string]string{"X-Org-Id": "acme"},
	})
	p, err := provider.Find("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p.Fetch(ctx)
	p.Test(ctx)
	if up.any("x-org-id") {
		t.Errorf("preset sent custom header: %q", up.lines)
	}
	// saving a preset provider keeps no headers on disk
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(provider.Path())
	if strings.Contains(string(b), "X-Org-Id") {
		t.Errorf("preset kept headers: %s", b)
	}
}

func TestAcceptHeadersOmittedWhenEmptyAndTrimmedOnSave(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{ID: "plain", Name: "Plain", Key: "k", Chat: "https://p.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(provider.Path())
	if strings.Contains(string(b), `"headers"`) {
		t.Errorf("empty headers written: %s", b)
	}
	// round trip through the file with messy names/values
	writeConfig(t, map[string]any{
		"id": "x", "name": "X", "key": "k", "chat": "https://x.example.com/v1",
		"headers": map[string]string{" X-A ": " 1 ", "": "gone"},
	})
	p, _ := provider.Find("x")
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	var f struct {
		Providers []struct {
			Headers map[string]string `json:"headers"`
		} `json:"providers"`
	}
	b, _ = os.ReadFile(provider.Path())
	json.Unmarshal(b, &f)
	if len(f.Providers) != 1 || len(f.Providers[0].Headers) != 1 || f.Providers[0].Headers["X-A"] != "1" {
		t.Errorf("saved headers: %s", b)
	}
}
