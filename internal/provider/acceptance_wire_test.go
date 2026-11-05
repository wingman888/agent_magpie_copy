package provider

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// rawServer is an HTTP/1.1 listener that keeps the request header names
// exactly as they arrive on the wire (net/http would canonicalise them in
// r.Header), then answers with a fixed JSON body.
type rawServer struct {
	mu    sync.Mutex
	lines []string // raw request header lines, in order
	paths []string // request targets, in order
}

func startRaw(t *testing.T, reply string) (*rawServer, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	rs := &rawServer{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go rs.handle(conn, reply)
		}
	}()
	return rs, "http://" + ln.Addr().String()
}

func (rs *rawServer) handle(conn net.Conn, reply string) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	first, err := br.ReadString('\n')
	if err != nil {
		return
	}
	if f := strings.Fields(first); len(f) > 1 {
		rs.mu.Lock()
		rs.paths = append(rs.paths, f[1])
		rs.mu.Unlock()
	}
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
		rs.mu.Lock()
		rs.lines = append(rs.lines, line)
		rs.mu.Unlock()
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			n, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	io.CopyN(io.Discard, br, int64(n))
	io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "+
		strconv.Itoa(len(reply))+"\r\nConnection: close\r\n\r\n"+reply)
}

// sent reports the raw header lines whose name matches name case-insensitively,
// and returns them exactly as sent.
func (rs *rawServer) sent(name string) []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	var out []string
	for _, l := range rs.lines {
		if k, _, ok := strings.Cut(l, ":"); ok && strings.EqualFold(strings.TrimSpace(k), name) {
			out = append(out, l)
		}
	}
	return out
}

const wireModelsReply = `{"object":"list","data":[{"id":"m1","object":"model"}]}`

func TestCustomHeaderIsSentWithItsOwnCaseOnTheWire(t *testing.T) {
	useConfig(t)
	rs, base := startRaw(t, wireModelsReply)
	p := Provider{ID: "gw", Name: "GW", Key: "sk-1", Chat: base + "/v1",
		Headers: map[string]string{"X-Org-Id": "acme", "x-gateway-token": "t1"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	saved, err := Find("gw")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	saved.Test(ctx)
	if got := rs.sent("X-Org-Id"); len(got) == 0 || got[0] != "X-Org-Id: acme" {
		t.Fatalf("connectivity test sent %q, want X-Org-Id: acme", got)
	}
	if got := rs.sent("x-gateway-token"); len(got) == 0 || got[0] != "x-gateway-token: t1" {
		t.Fatalf("connectivity test sent %q, want x-gateway-token: t1", got)
	}
	rs.mu.Lock()
	rs.lines, rs.paths = nil, nil
	rs.mu.Unlock()
	if _, err := saved.Fetch(ctx); err != nil {
		t.Fatalf("model list: %v", err)
	}
	if got := rs.sent("X-Org-Id"); len(got) == 0 || got[0] != "X-Org-Id: acme" {
		t.Fatalf("model list fetch sent %q, want X-Org-Id: acme", got)
	}
}

func TestPresetAndAccountNeverGetCustomHeaders(t *testing.T) {
	useConfig(t)
	pre := Provider{ID: "openai", Preset: "openai", Headers: map[string]string{"X-Org-Id": "acme"}}
	if h := pre.CustomHeaders(); len(h) != 0 {
		t.Fatalf("preset sends headers %v", h)
	}
	acct := Provider{ID: "codex", Account: &Account{Agent: "codex"}, Headers: map[string]string{"X-Org-Id": "acme"}}
	if h := acct.CustomHeaders(); len(h) != 0 {
		t.Fatalf("account sends headers %v", h)
	}
}
