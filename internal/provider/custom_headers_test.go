package provider

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// wireUpstream is a raw TCP upstream that records each request's header
// lines exactly as they arrive, so header-name case is visible (Go's own
// server canonicalises names).
type wireUpstream struct {
	mu   sync.Mutex
	reqs [][]string // per request: header lines as sent ("Name: value")
}

func (u *wireUpstream) start(t *testing.T) string {
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
			go u.serve(c)
		}
	}()
	return "http://" + ln.Addr().String()
}

func (u *wireUpstream) serve(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	var lines []string
	length := 0
	for first := true; ; first = false {
		l, err := br.ReadString('\n')
		if err != nil {
			return
		}
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			break
		}
		if first {
			continue // request line
		}
		if i := strings.Index(l, ":"); i > 0 && strings.EqualFold(strings.TrimSpace(l[:i]), "Content-Length") {
			length, _ = strconv.Atoi(strings.TrimSpace(l[i+1:]))
		}
		lines = append(lines, l)
	}
	io.CopyN(io.Discard, br, int64(length))
	u.mu.Lock()
	u.reqs = append(u.reqs, lines)
	u.mu.Unlock()
	body := `{"data":[{"id":"m1"}]}`
	io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\nConnection: close\r\n\r\n"+body)
}

func (u *wireUpstream) all() [][]string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]string(nil), u.reqs...)
}

// wireHas reports whether a header line is exactly "name: value", name case included.
func wireHas(lines []string, name, value string) bool {
	for _, l := range lines {
		if l == name+": "+value {
			return true
		}
	}
	return false
}

// wireCount counts header lines whose name matches name in any letter case.
func wireCount(lines []string, name string) int {
	n := 0
	for _, l := range lines {
		if i := strings.Index(l, ":"); i > 0 && strings.EqualFold(l[:i], name) {
			n++
		}
	}
	return n
}

func isolateConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")
}

func TestCustomHeadersSentOnFetchAndTest(t *testing.T) {
	isolateConfig(t)
	var up wireUpstream
	base := up.start(t)
	p := Provider{ID: "relay", Name: "Relay", Key: "k", Chat: base + "/v1", Headers: map[string]string{
		"X-Org-Id":      "acme",
		"X-ORG-id":      "padded",
		"Authorization": "Token t",
	}}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if res := p.Test(context.Background()); len(res) == 0 {
		t.Fatal("test returned no results")
	}
	reqs := up.all()
	if len(reqs) < 2 {
		t.Fatalf("want a model-list request and a probe, got %d requests", len(reqs))
	}
	for i, h := range reqs {
		if !wireHas(h, "X-Org-Id", "acme") {
			t.Errorf("request %d: X-Org-Id: acme missing; headers %q", i, h)
		}
		if !wireHas(h, "X-ORG-id", "padded") {
			t.Errorf("request %d: X-ORG-id kept its case? headers %q", i, h)
		}
		if !wireHas(h, "Authorization", "Token t") {
			t.Errorf("request %d: custom Authorization did not replace the key auth; headers %q", i, h)
		}
		if n := wireCount(h, "Authorization"); n != 1 {
			t.Errorf("request %d: %d Authorization headers, want 1", i, n)
		}
	}
}

func TestNoCustomHeadersForPresetOrAccount(t *testing.T) {
	isolateConfig(t)
	var up wireUpstream
	base := up.start(t)
	headers := map[string]string{"X-Org-Id": "acme"}
	preset := Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Key: "k", Chat: base + "/v1", Headers: headers}
	account := Provider{ID: "codex", Name: "Codex", Account: &Account{Agent: "codex"}, Chat: base + "/v1", Headers: headers}
	if _, err := preset.Fetch(context.Background()); err != nil {
		t.Fatalf("preset fetch: %v", err)
	}
	if _, err := account.Fetch(context.Background()); err != nil {
		t.Fatalf("account fetch: %v", err)
	}
	for i, h := range up.all() {
		if wireCount(h, "X-Org-Id") != 0 {
			t.Errorf("request %d carried a custom header: %q", i, h)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	preset.ApplyCustomHeaders(req)
	account.ApplyCustomHeaders(req)
	if len(req.Header) != 0 {
		t.Errorf("preset/account ApplyCustomHeaders set %v", req.Header)
	}
}

func TestApplyCustomHeadersOverridesAuthCaseInsensitively(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	req.Header.Set("Authorization", "Bearer k")
	p := Provider{ID: "relay", Headers: map[string]string{"authorization": "custom", "X-Org-Id": "acme"}}
	p.ApplyCustomHeaders(req)
	if v := req.Header["authorization"]; len(v) != 1 || v[0] != "custom" {
		t.Errorf("authorization = %q, want [custom]", v)
	}
	if _, ok := req.Header["Authorization"]; ok {
		t.Error("canonical Authorization still present next to the custom one")
	}
	if v := req.Header["X-Org-Id"]; len(v) != 1 || v[0] != "acme" {
		t.Errorf("X-Org-Id = %q, want [acme]", v)
	}
}
