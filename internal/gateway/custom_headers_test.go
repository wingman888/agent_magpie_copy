package gateway

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

	"github.com/yetone/magpie/internal/provider"
)

// rawHeaders is a TCP upstream that records the header lines of each request
// exactly as sent, so the header-name case is visible.
type rawHeaders struct {
	mu   sync.Mutex
	reqs [][]string
}

func (u *rawHeaders) start(t *testing.T) string {
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

func (u *rawHeaders) serve(c net.Conn) {
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
			continue
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
	body := `{"id":"x","object":"chat.completion","choices":[]}`
	io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\nConnection: close\r\n\r\n"+body)
}

func (u *rawHeaders) all() [][]string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]string(nil), u.reqs...)
}

func TestCustomHeadersOnForward(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")
	var up rawHeaders
	base := up.start(t)
	p := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: base + "/v1", Headers: map[string]string{
		"X-Org-Id":      "acme",
		"X-ORG-id":      "padded",
		"Authorization": "Token t",
	}}
	res, err := New().forward(context.Background(), p, provider.Chat, "/chat/completions", []byte(`{"model":"m1"}`), http.Header{})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	res.Body.Close()
	reqs := up.all()
	if len(reqs) != 1 {
		t.Fatalf("want 1 upstream request, got %d", len(reqs))
	}
	want := map[string]bool{"X-Org-Id: acme": false, "X-ORG-id: padded": false, "Authorization: Token t": false}
	auth := 0
	for _, l := range reqs[0] {
		if _, ok := want[l]; ok {
			want[l] = true
		}
		if i := strings.Index(l, ":"); i > 0 && strings.EqualFold(l[:i], "Authorization") {
			auth++
		}
	}
	for l, ok := range want {
		if !ok {
			t.Errorf("upstream did not receive %q; headers %q", l, reqs[0])
		}
	}
	if auth != 1 {
		t.Errorf("%d Authorization headers upstream, want 1", auth)
	}
}
