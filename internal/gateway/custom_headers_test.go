package gateway

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// rawHeaderUpstream answers every request with body and reports the request's
// header lines exactly as they arrived, since httptest canonicalizes names.
func rawHeaderUpstream(t *testing.T, body string) (string, <-chan []string) {
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
			go replyRaw(c, body, seen)
		}
	}()
	return "http://" + ln.Addr().String(), seen
}

func replyRaw(c net.Conn, body string, seen chan<- []string) {
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

func hasHeaderLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func hasHeaderNamed(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func TestForwardSendsCustomHeaders(t *testing.T) {
	base, seen := rawHeaderUpstream(t, `{}`)
	p := provider.Provider{
		ID: "relay", Name: "Relay", Key: "sk-test", Chat: base,
		Headers: map[string]string{
			"X-Org-Id":      "acme",
			"X-ORG-id":      "upper",
			"authorization": "Custom auth",
		},
	}
	res, err := New().forward(context.Background(), p, provider.Chat, "/chat/completions", []byte(`{}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	lines := <-seen
	for _, want := range []string{"X-Org-Id: acme", "X-ORG-id: upper", "authorization: Custom auth"} {
		if !hasHeaderLine(lines, want) {
			t.Errorf("forwarded request did not carry %q; got %q", want, lines)
		}
	}
	if hasHeaderNamed(lines, "Authorization: Bearer") || hasHeaderNamed(lines, "authorization: Bearer") {
		t.Errorf("auth header was not overridden: %q", lines)
	}
}

func TestForwardPresetAndAccountSendNoCustomHeaders(t *testing.T) {
	cases := map[string]provider.Provider{
		"preset": {ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Key: "sk-test"},
		"agent":  {ID: "codex", Name: "Codex", Key: "sk-test", Account: &provider.Account{Agent: "codex", User: "someone"}},
	}
	for name, p := range cases {
		base, seen := rawHeaderUpstream(t, `{}`)
		p.Chat = base
		p.Headers = map[string]string{"X-Org-Id": "acme"}
		res, err := New().forward(context.Background(), p, provider.Chat, "/chat/completions", []byte(`{}`), http.Header{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		res.Body.Close()
		if lines := <-seen; hasHeaderNamed(lines, "X-Org-Id:") {
			t.Errorf("%s sent a custom header: %q", name, lines)
		}
	}
}
