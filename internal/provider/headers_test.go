package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// rawServer answers every request with body and keeps each request's header
// block as it arrived on the wire. net/http would canonicalize the names, and
// the point of these tests is the case the user typed.
type rawServer struct {
	mu    sync.Mutex
	heads []string
}

func newRawServer(t *testing.T, body string) (string, *rawServer) {
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
			go rs.serve(conn, body)
		}
	}()
	return "http://" + ln.Addr().String(), rs
}

func (rs *rawServer) serve(conn net.Conn, body string) {
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
	rs.mu.Lock()
	rs.heads = append(rs.heads, head.String())
	rs.mu.Unlock()
	fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

func (rs *rawServer) seen() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]string(nil), rs.heads...)
}

// isolateConfig points magpie's config and cache at a temp dir.
func isolateConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
}

const modelList = `{"data":[{"id":"demo-chat"}]}`

func TestCustomHeadersReachEveryRequest(t *testing.T) {
	isolateConfig(t)
	base, rs := newRawServer(t, modelList)
	err := Save(Provider{Name: "Team Relay", Chat: base + "/v1", Key: "sk-test",
		Headers: map[string]string{"  X-Org-Id ": " acme ", "": "dropped"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find("team-relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 1 || p.Headers["X-Org-Id"] != "acme" {
		t.Fatalf("headers after save = %v, want only X-Org-Id=acme", p.Headers)
	}
	res := p.Test(context.Background())
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("test result = %+v, want one OK probe", res)
	}
	heads := rs.seen()
	if len(heads) < 2 {
		t.Fatalf("saw %d requests, want the model list and the probe", len(heads))
	}
	for _, h := range heads {
		if !strings.Contains(h, "\r\nX-Org-Id: acme\r\n") {
			t.Errorf("request without the exact-case header:\n%s", h)
		}
	}
}

func TestPresetIgnoresCustomHeaders(t *testing.T) {
	isolateConfig(t)
	base, rs := newRawServer(t, modelList)
	err := Save(Provider{Preset: "deepseek", Name: "DeepSeek", Chat: base + "/v1", Key: "sk-test",
		Headers: map[string]string{"X-Org-Id": "acme"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 0 || len(p.CustomHeaders()) != 0 {
		t.Fatalf("preset kept headers %v", p.Headers)
	}
	p.Test(context.Background())
	heads := rs.seen()
	if len(heads) == 0 {
		t.Fatal("no request reached the server")
	}
	for _, h := range heads {
		if strings.Contains(h, "X-Org-Id") {
			t.Errorf("preset sent a custom header:\n%s", h)
		}
	}
}

func TestCleanHeaders(t *testing.T) {
	got := cleanHeaders(map[string]string{
		" X-A ":     " 1 ",
		"":          "empty name",
		"   ":       "blank name",
		"Bad Name":  "space in name",
		"X-B":       "a\r\nInjected: 1",
		"X-Keep-Eq": "",
		"X-Other":   "x",
	})
	want := map[string]string{"X-A": "1", "X-Keep-Eq": "", "X-Other": "x"}
	if len(got) != len(want) {
		t.Fatalf("cleanHeaders = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("cleanHeaders[%q] = %q, want %q", k, got[k], v)
		}
	}
	if cleanHeaders(map[string]string{" ": "x"}) != nil {
		t.Error("an empty map should clean to nil so nothing is written")
	}
}

func TestSetHeaderExactReplacesCaseVariants(t *testing.T) {
	h := http.Header{}
	h["User-Agent"] = []string{"magpie"}
	catalog.SetHeaderExact(h, "user-agent", "custom")
	if len(h) != 1 || h["user-agent"][0] != "custom" {
		t.Fatalf("header = %v, want only user-agent: custom", h)
	}
	catalog.SetHeaderExact(h, "X-Org-Id", "acme")
	if h["X-Org-Id"][0] != "acme" {
		t.Fatalf("exact name not kept: %v", h)
	}
}

func TestValidHeader(t *testing.T) {
	for _, ok := range []string{"X-Org-Id", "x_gateway.token", "A"} {
		if !ValidHeader(ok) {
			t.Errorf("ValidHeader(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "Bad Name", "X:Y", "héllo"} {
		if ValidHeader(bad) {
			t.Errorf("ValidHeader(%q) = true", bad)
		}
	}
}
