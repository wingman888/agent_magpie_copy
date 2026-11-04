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
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// f04HeaderUpstream records each request's header names exactly as they
// arrived, which net/http would canonicalise.
type f04HeaderUpstream struct {
	mu    sync.Mutex
	paths []string
	heads [][][2]string
}

func f04NewHeaderUpstream(t *testing.T) (*f04HeaderUpstream, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	u := &f04HeaderUpstream{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go u.serve(c)
		}
	}()
	return u, "http://" + ln.Addr().String()
}

func (u *f04HeaderUpstream) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	var path string
	if parts := strings.Fields(line); len(parts) > 1 {
		path = parts[1]
	}
	var heads [][2]string
	length := 0
	for {
		l, err := r.ReadString('\n')
		if err != nil {
			return
		}
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			break
		}
		name, value, _ := strings.Cut(l, ":")
		heads = append(heads, [2]string{name, strings.TrimSpace(value)})
		if strings.EqualFold(name, "Content-Length") {
			length, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	io.CopyN(io.Discard, r, int64(length))
	u.mu.Lock()
	u.paths = append(u.paths, path)
	u.heads = append(u.heads, heads)
	u.mu.Unlock()
	body := `{"id":"x","choices":[]}`
	fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

func TestF04ForwardSendsCustomHeadersAsTyped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up, base := f04NewHeaderUpstream(t)
	p := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: base + "/v1",
		Headers: map[string]string{"X-ORG-id": "acme", "Authorization": "Custom auth"}}

	res, err := New().forward(context.Background(), p, provider.Chat, "/chat/completions",
		[]byte(`{"model":"m1","messages":[]}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.heads) != 1 {
		t.Fatalf("want one forwarded request, got %d", len(up.heads))
	}
	var orgSeen bool
	var auth []string
	for _, h := range up.heads[0] {
		if h[0] == "X-ORG-id" && h[1] == "acme" {
			orgSeen = true
		}
		if strings.EqualFold(h[0], "Authorization") {
			auth = append(auth, h[1])
		}
	}
	if !orgSeen {
		t.Errorf("X-ORG-id: acme not forwarded exactly; headers %v", up.heads[0])
	}
	if len(auth) != 1 || auth[0] != "Custom auth" {
		t.Errorf("Authorization = %v, want only [Custom auth]", auth)
	}
}

func TestF04AnthropicForwardKeepsUserHeaderOverAuth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up, base := f04NewHeaderUpstream(t)
	p := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: base,
		Headers: map[string]string{"X-Api-Token": "tok", "x-api-key": "mine"}}

	// count_tokens goes through forward too, with the Anthropic version header.
	res, err := New().forward(context.Background(), p, provider.Anthropic, "/v1/messages/count_tokens",
		[]byte(`{"model":"m1","messages":[]}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.paths) != 1 || up.paths[0] != "/v1/messages/count_tokens" {
		t.Fatalf("forwarded paths = %v, want [/v1/messages/count_tokens]", up.paths)
	}
	var token bool
	var xkey []string
	for _, h := range up.heads[0] {
		if h[0] == "X-Api-Token" && h[1] == "tok" {
			token = true
		}
		if strings.EqualFold(h[0], "x-api-key") {
			xkey = append(xkey, h[1])
		}
	}
	if !token {
		t.Errorf("X-Api-Token: tok not forwarded exactly; headers %v", up.heads[0])
	}
	if len(xkey) != 1 || xkey[0] != "mine" {
		t.Errorf("x-api-key = %v, want only [mine]", xkey)
	}
}
