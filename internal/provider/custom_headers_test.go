package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// rawUpstream answers every request with body and reports the request's
// header lines as they arrived on the socket. httptest canonicalizes header
// names on the server side, so it cannot show whether the case was kept.
func rawUpstream(t *testing.T, body string) (string, <-chan []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	seen := make(chan []string, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveRaw(c, body, seen)
		}
	}()
	return "http://" + ln.Addr().String(), seen
}

func serveRaw(c net.Conn, body string, seen chan<- []string) {
	defer c.Close()
	br := bufio.NewReader(c)
	var lines []string
	n := 0
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		lines = append(lines, line)
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			n, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	io.CopyN(io.Discard, br, int64(n))
	fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	seen <- lines
}

func sawLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func sawPrefix(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func isolateConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func TestCustomHeadersOnProbe(t *testing.T) {
	base, seen := rawUpstream(t, `{}`)
	p := Provider{
		ID: "relay", Name: "Relay", Key: "sk-test", Anthropic: base,
		Headers: map[string]string{
			"X-Org-Id":      "acme",
			"X-ORG-id":      "upper",
			"authorization": "Custom auth",
		},
	}
	res := probe(context.Background(), p, Anthropic, base+"/v1/messages", []byte(`{}`), "m")
	if !res.OK {
		t.Fatalf("probe failed: %+v", res)
	}
	lines := <-seen
	for _, want := range []string{"X-Org-Id: acme", "X-ORG-id: upper", "authorization: Custom auth"} {
		if !sawLine(lines, want) {
			t.Errorf("upstream did not receive %q; got %q", want, lines)
		}
	}
	if sawPrefix(lines, "Authorization: Bearer") || sawPrefix(lines, "authorization: Bearer") {
		t.Errorf("auth header was not overridden by the custom one: %q", lines)
	}
}

func TestCustomHeadersOnModelList(t *testing.T) {
	isolateConfig(t)
	base, seen := rawUpstream(t, `{"data":[{"id":"chat-model"}]}`)
	p := Provider{
		ID: "relay", Name: "Relay", Key: "sk-test", Chat: base,
		Headers: map[string]string{"X-Tenant": "t1", "X-ORG-id": "upper"},
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	lines := <-seen
	for _, want := range []string{"X-Tenant: t1", "X-ORG-id: upper"} {
		if !sawLine(lines, want) {
			t.Errorf("model list request did not carry %q; got %q", want, lines)
		}
	}
}

func TestPresetSendsNoCustomHeaders(t *testing.T) {
	base, seen := rawUpstream(t, `{}`)
	p := Provider{
		ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Key: "sk-test", Anthropic: base,
		Headers: map[string]string{"X-Org-Id": "acme"},
	}
	if res := probe(context.Background(), p, Anthropic, base+"/v1/messages", []byte(`{}`), "m"); !res.OK {
		t.Fatalf("probe failed: %+v", res)
	}
	if lines := <-seen; sawPrefix(lines, "X-Org-Id:") {
		t.Errorf("preset sent a custom header: %q", lines)
	}
}

func TestAccountSendsNoCustomHeaders(t *testing.T) {
	base, seen := rawUpstream(t, `{}`)
	p := Provider{
		ID: "codex", Name: "Codex", Key: "sk-test", Chat: base,
		Account: &Account{Agent: "codex", User: "someone"},
		Headers: map[string]string{"X-Org-Id": "acme"},
	}
	if res := probe(context.Background(), p, Chat, base+"/chat/completions", []byte(`{}`), "m"); !res.OK {
		t.Fatalf("probe failed: %+v", res)
	}
	if lines := <-seen; sawPrefix(lines, "X-Org-Id:") {
		t.Errorf("signed-in account sent a custom header: %q", lines)
	}
}
