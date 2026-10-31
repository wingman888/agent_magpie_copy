package provider

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
)

// A raw listener, because net/http's server canonicalizes header names and
// would hide whether the name was sent as typed.
func TestCustomHeadersReachUpstream(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mu sync.Mutex
	var reqs []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				var head strings.Builder
				clen := 0
				method := ""
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if method == "" {
						method = strings.Fields(line)[0]
					}
					if strings.HasPrefix(strings.ToLower(line), "content-length:") {
						n := strings.TrimSpace(line[len("content-length:"):])
						for _, ch := range n {
							clen = clen*10 + int(ch-'0')
						}
					}
					head.WriteString(line)
					if line == "\r\n" {
						break
					}
				}
				for i := 0; i < clen; i++ {
					br.ReadByte()
				}
				mu.Lock()
				reqs = append(reqs, head.String())
				mu.Unlock()
				body := `{"data":[{"id":"m1"}],"choices":[{"message":{"content":"x"}}]}`
				c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nConnection: close\r\nContent-Length: " +
					itoa(len(body)) + "\r\n\r\n" + body))
			}(c)
		}
	}()
	url := "http://" + ln.Addr().String()
	p := Provider{ID: "hdr", Name: "Hdr", Key: "k", Models: []string{"m1"}, Chat: url + "/v1",
		Headers: map[string]string{"X-Org-Id": "acme", "x-lower": "v"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Find("hdr")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := got.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	got.Test(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(reqs) < 2 {
		t.Fatalf("requests seen: %d", len(reqs))
	}
	for _, h := range reqs {
		if !strings.Contains(h, "\r\nX-Org-Id: acme\r\n") || !strings.Contains(h, "\r\nx-lower: v\r\n") {
			t.Fatalf("headers not sent as typed:\n%s", h)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}
