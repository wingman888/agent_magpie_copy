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

// TestForwardSendsCustomHeaders checks the wire, not net/http's view of it:
// the header must arrive with the case the user typed, after the key's own
// Authorization header.
func TestForwardSendsCustomHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	heads := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		var head strings.Builder
		length := 0
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			head.WriteString(line)
			if line == "\r\n" {
				break
			}
			if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "content-length") {
				length, _ = strconv.Atoi(strings.TrimSpace(v))
			}
		}
		_, _ = io.CopyN(io.Discard, br, int64(length))
		heads <- head.String()
		fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
	}()

	s := &Server{client: http.DefaultClient}
	p := provider.Provider{ID: "relay", Name: "Relay", Key: "sk-test",
		Chat:    "http://" + ln.Addr().String() + "/v1",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	res, err := s.forward(context.Background(), p, provider.Chat, "/chat/completions", []byte(`{"model":"m"}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	head := <-heads
	if !strings.Contains(head, "\r\nX-Org-Id: acme\r\n") {
		t.Errorf("custom header missing or re-cased:\n%s", head)
	}
	if !strings.Contains(head, "\r\nAuthorization: Bearer sk-test\r\n") {
		t.Errorf("auth header missing:\n%s", head)
	}
}
