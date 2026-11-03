package gateway

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// rawUpstream records the header names of each request exactly as they were
// written; net/http would canonicalise them on read.
type rawUpstream struct {
	mu  sync.Mutex
	got [][][2]string
}

func (u *rawUpstream) start(t *testing.T) string {
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

func (u *rawUpstream) serve(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	if _, err := br.ReadString('\n'); err != nil {
		return
	}
	var hs [][2]string
	length := 0
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			return
		}
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			break
		}
		name, value, _ := strings.Cut(l, ":")
		value = strings.TrimSpace(value)
		hs = append(hs, [2]string{name, value})
		if strings.EqualFold(name, "Content-Length") {
			length, _ = strconv.Atoi(value)
		}
	}
	io.ReadFull(br, make([]byte, length))
	u.mu.Lock()
	u.got = append(u.got, hs)
	u.mu.Unlock()
	body := `{"error":{"message":"stop here"}}`
	fmt.Fprintf(c, "HTTP/1.1 500 Internal Server Error\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

func (u *rawUpstream) requests() [][][2]string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][][2]string(nil), u.got...)
}

func TestForwardedRequestCarriesCustomHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := &rawUpstream{}
	base := up.start(t)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"},
		Chat: base + "/v1", Headers: map[string]string{"X-Org-Id": "acme", "X-ORG-id": "other", "Authorization": "Token mine"}}); err != nil {
		t.Fatal(err)
	}
	post(t, "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`)
	reqs := up.requests()
	if len(reqs) == 0 {
		t.Fatal("upstream got no request")
	}
	for _, hs := range reqs {
		has := func(name, value string) bool {
			for _, h := range hs {
				if h[0] == name && h[1] == value {
					return true
				}
			}
			return false
		}
		if !has("X-Org-Id", "acme") {
			t.Errorf("X-Org-Id: acme missing or mis-cased: %v", hs)
		}
		if !has("X-ORG-id", "other") {
			t.Errorf("X-ORG-id not kept in its case: %v", hs)
		}
		n := 0
		for _, h := range hs {
			if strings.EqualFold(h[0], "Authorization") {
				n++
				if h[1] != "Token mine" {
					t.Errorf("Authorization = %q, want the user's value", h[1])
				}
			}
		}
		if n != 1 {
			t.Errorf("Authorization sent %d times, want once", n)
		}
	}
}
