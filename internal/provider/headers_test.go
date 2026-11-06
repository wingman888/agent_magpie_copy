package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// freshHome points HOME, the config and the cache at a fresh temp dir.
func freshHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

// rawServer answers every request with body and records the request exactly
// as it arrived on the wire, header names and their case included. Go's own
// server would canonicalise names, so this reads the bytes itself.
func rawServer(t *testing.T, body string) (string, func() [][]string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var seen [][]string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				lines := serveRaw(c, body)
				if lines != nil {
					mu.Lock()
					seen = append(seen, lines)
					mu.Unlock()
				}
			}()
		}
	}()
	return "http://" + ln.Addr().String(), func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		return append([][]string(nil), seen...)
	}
}

// serveRaw reads one request, returns its header lines verbatim, and replies 200.
func serveRaw(c net.Conn, body string) []string {
	defer c.Close()
	br := bufio.NewReader(c)
	if _, err := br.ReadString('\n'); err != nil {
		return nil
	}
	var lines []string
	clen := 0
	for {
		h, err := br.ReadString('\n')
		if err != nil {
			return nil
		}
		h = strings.TrimRight(h, "\r\n")
		if h == "" {
			break
		}
		lines = append(lines, h)
		if k, v, ok := strings.Cut(h, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			clen, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	if clen > 0 {
		io.CopyN(io.Discard, br, int64(clen))
	}
	fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	return lines
}

// hasLine reports whether a request carried exactly "name: value" (name case-sensitive).
func hasLine(lines []string, name, value string) bool {
	for _, l := range lines {
		k, v, ok := strings.Cut(l, ":")
		if ok && k == name && strings.TrimSpace(v) == value {
			return true
		}
	}
	return false
}

// countFold counts header lines whose name matches name in any case.
func countFold(lines []string, name string) int {
	n := 0
	for _, l := range lines {
		if k, _, ok := strings.Cut(l, ":"); ok && strings.EqualFold(k, name) {
			n++
		}
	}
	return n
}

const modelsBody = `{"data":[{"id":"m1"}]}`

func TestCustomHeadersReachForwardTestAndModelList(t *testing.T) {
	freshHome(t)
	base, seen := rawServer(t, modelsBody)
	p := Provider{ID: "relay", Name: "Relay", Chat: base + "/v1", Key: "sk-test",
		Headers: map[string]string{"X-Org-Id": "acme"}}

	res := p.Test(context.Background())
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("probe failed: %+v", res)
	}
	reqs := seen()
	if len(reqs) < 2 {
		t.Fatalf("expected a model-list fetch and a probe, saw %d requests", len(reqs))
	}
	for i, r := range reqs {
		if !hasLine(r, "X-Org-Id", "acme") {
			t.Errorf("request %d lacks X-Org-Id: acme (raw headers %q)", i, r)
		}
	}

	// the model-list fetch on its own
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	last := seen()
	if !hasLine(last[len(last)-1], "X-Org-Id", "acme") {
		t.Errorf("Fetch request lacks the custom header: %q", last[len(last)-1])
	}
}

func TestCustomHeaderNameKeepsCaseAndOverridesDefaults(t *testing.T) {
	freshHome(t)
	base, seen := rawServer(t, modelsBody)
	p := Provider{ID: "relay", Name: "Relay", Chat: base + "/v1", Key: "sk-test",
		Headers: map[string]string{"x-org-id": "acme", "user-agent": "relay-cli"}}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := seen()
	if len(reqs) == 0 {
		t.Fatal("no request reached the upstream")
	}
	r := reqs[0]
	if !hasLine(r, "x-org-id", "acme") {
		t.Errorf("header name not sent as typed: %q", r)
	}
	if !hasLine(r, "User-Agent", "relay-cli") {
		t.Errorf("custom User-Agent not sent: %q", r)
	}
	if n := countFold(r, "User-Agent"); n != 1 {
		t.Errorf("expected exactly one User-Agent header (the custom one), found %d: %q", n, r)
	}
}

func TestPresetAndAccountNeverSendCustomHeaders(t *testing.T) {
	freshHome(t)
	base, seen := rawServer(t, modelsBody)
	p := Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Chat: base + "/v1", Key: "k",
		Headers: map[string]string{"X-Org-Id": "acme"}}

	p.Test(context.Background())
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, r := range seen() {
		if countFold(r, "X-Org-Id") != 0 {
			t.Errorf("preset request %d carried a custom header: %q", i, r)
		}
	}

	// the forwarding path: Sign is what the gateway calls before sending
	acct := Provider{ID: "codex", Name: "Codex", Headers: map[string]string{"X-Org-Id": "acme"},
		Account: &Account{Agent: "codex", User: "a@example.com", sign: func(ctx context.Context, req *http.Request, body []byte) error {
			return nil
		}}}
	req, _ := http.NewRequest(http.MethodPost, "http://example.invalid/v1/responses", nil)
	if err := acct.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Header["X-Org-Id"]; ok {
		t.Error("signed-in account request carried a custom header")
	}
}

func TestForwardedRequestGetsHeadersAfterAuth(t *testing.T) {
	freshHome(t)
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-test",
		Headers: map[string]string{"X-Org-Id": "acme", "authorization": "Token override"}}
	req, _ := http.NewRequest(http.MethodPost, "https://relay.example.com/v1/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if v := req.Header["X-Org-Id"]; len(v) != 1 || v[0] != "acme" {
		t.Errorf("X-Org-Id not set as typed: %v", req.Header)
	}
	if v := req.Header["authorization"]; len(v) != 1 || v[0] != "Token override" {
		t.Errorf("custom header did not override the default auth: %v", req.Header)
	}
	if _, ok := req.Header["Authorization"]; ok {
		t.Error("default Authorization still sent next to the override")
	}
}

func TestSaveNormalisesHeaders(t *testing.T) {
	freshHome(t)
	if err := Save(Provider{Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k",
		Headers: map[string]string{"  X-Org-Id ": " acme ", "  ": "dropped"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 1 || p.Headers["X-Org-Id"] != "acme" {
		t.Errorf("headers not normalised: %#v", p.Headers)
	}

	// an empty map is not written at all
	if err := Save(Provider{Name: "Plain", Chat: "https://plain.example.com/v1", Key: "k",
		Headers: map[string]string{" ": "x"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), `"headers"`) != 1 {
		t.Errorf("expected only the non-empty header map to be written:\n%s", b)
	}
}
