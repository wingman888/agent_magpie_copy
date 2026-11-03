package catalog

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// rawServer answers every request with a model list and keeps the raw
// header lines as they came off the wire, which Go's server would otherwise
// canonicalize.
func rawServer(t *testing.T) (addr string, lines func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var seen []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var buf []byte
				tmp := make([]byte, 1)
				for !strings.HasSuffix(string(buf), "\r\n\r\n") {
					n, err := c.Read(tmp)
					if n > 0 {
						buf = append(buf, tmp[:n]...)
					}
					if err != nil {
						return
					}
				}
				mu.Lock()
				seen = append(seen, string(buf))
				mu.Unlock()
				body := `{"data":[{"id":"m1"}]}`
				fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
			}(c)
		}
	}()
	return ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func TestFetchWithHeadersKeepsHeaderNameCase(t *testing.T) {
	addr, lines := rawServer(t)
	ms, err := FetchWithHeaders(context.Background(), "http://"+addr+"/v1", "sk-test", false,
		map[string]string{"x-org-id": "acme", "X-Gateway-Auth": "g"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("models = %v", ms)
	}
	reqs := lines()
	if len(reqs) == 0 {
		t.Fatal("no request reached the server")
	}
	for _, want := range []string{"\r\nx-org-id: acme\r\n", "\r\nX-Gateway-Auth: g\r\n"} {
		if !strings.Contains(reqs[0], want) {
			t.Errorf("request lacks %q:\n%s", want, reqs[0])
		}
	}
}

func TestFetchWithoutHeadersUsesDefaultClient(t *testing.T) {
	if ClientFor(nil) != http.DefaultClient {
		t.Fatal("no headers should keep the default client")
	}
	if ClientFor(map[string]string{}) != http.DefaultClient {
		t.Fatal("empty headers should keep the default client")
	}
}

func TestHTTP1TransportDoesNotNegotiateHTTP2(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Proto)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	// srv.Client() already trusts the test certificate; keep its TLS settings
	// and only change the protocol negotiation.
	base := srv.Client().Transport.(*http.Transport)
	base.ForceAttemptHTTP2 = true
	c := &http.Client{Transport: HTTP1Transport(base)}
	res, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET %s: %v", srv.URL, err)
	}
	defer res.Body.Close()
	if res.ProtoMajor != 1 {
		t.Fatalf("negotiated %s; want HTTP/1.x", res.Proto)
	}
}
