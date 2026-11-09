package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
)

// hdrRawServer answers every request with a small model list and reports
// the raw request head (exactly as it arrived, header name casing intact,
// which net/http's own server would canonicalize away).
func hdrRawServer(t *testing.T) (string, chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	heads := make(chan string, 32)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go hdrHandleConn(conn, heads)
		}
	}()
	return "http://" + ln.Addr().String(), heads
}

func hdrHandleConn(conn net.Conn, heads chan string) {
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
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			length, _ = strconv.Atoi(strings.TrimSpace(line[len("content-length:"):]))
		}
		if line == "\r\n" {
			break
		}
	}
	if _, err := io.CopyN(io.Discard, br, int64(length)); err != nil {
		return
	}
	heads <- head.String()
	body := `{"data":[{"id":"m1"}]}`
	fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

func hdrIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestHeadersStoredNormalized(t *testing.T) {
	hdrIsolate(t)
	err := Save(Provider{Name: "Relay", Chat: "http://127.0.0.1:1/v1", Key: "sk-test",
		Headers: map[string]string{"  X-Org-Id ": "  acme  ", "   ": "dropped", "": "dropped too", "x-org-id": "later"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 1 || p.Headers["x-org-id"] != "later" {
		t.Fatalf("headers = %#v, want only the later x-org-id entry", p.Headers)
	}

	// an empty map is never written
	if err := Save(Provider{Name: "Plain", Chat: "http://127.0.0.1:1/v1", Key: "sk-test", Headers: map[string]string{" ": "x"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"headers"`) && strings.Count(string(b), `"headers"`) != 1 {
		t.Fatalf("providers.json has unexpected headers entries:\n%s", b)
	}
	plain, err := Find("plain")
	if err != nil {
		t.Fatal(err)
	}
	if plain.Headers != nil {
		t.Fatalf("empty header map should not be kept, got %#v", plain.Headers)
	}
}

func TestHeadersSetHeaderRules(t *testing.T) {
	var p Provider
	if err := p.SetHeader("X-Foo", "1"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetHeader("x-foo", "2"); err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 1 || p.Headers["x-foo"] != "2" {
		t.Fatalf("headers = %#v, want one x-foo=2 (name casing of the latest set)", p.Headers)
	}
	if err := p.SetHeader("  ", "x"); err == nil {
		t.Fatal("an empty header name must be rejected")
	}
	if err := p.SetHeader("Bad Name", "x"); err == nil {
		t.Fatal("a header name with a space must be rejected")
	}
}

func TestHeadersOnlyForCustomVendors(t *testing.T) {
	hs := map[string]string{"X-Org-Id": "acme"}
	if got := (Provider{Name: "Custom", Chat: "https://x.test/v1", Headers: hs}).ExtraHeaders(); len(got) != 1 {
		t.Fatalf("custom vendor headers = %v, want one", got)
	}
	if got := (Provider{ID: "deepseek", Preset: "deepseek", Headers: hs}).ExtraHeaders(); got != nil {
		t.Fatalf("preset must ignore headers, got %v", got)
	}
	if got := (Provider{ID: "claude", Headers: hs, Account: &Account{Agent: "claude"}}).ExtraHeaders(); got != nil {
		t.Fatalf("signed-in account must ignore headers, got %v", got)
	}
}

func TestHeadersOnTheWire(t *testing.T) {
	hdrIsolate(t)
	base, heads := hdrRawServer(t)
	p := Provider{ID: "hdr-relay", Name: "Relay", Chat: base + "/v1", Key: "sk-test",
		Headers: map[string]string{"X-Org-Id": "acme", "x-Gateway-Token": "t1", "Authorization": "Bearer override"}}
	ctx := context.Background()
	for _, r := range p.Test(ctx) {
		if !r.OK {
			t.Fatalf("probe %s failed: %s", r.Protocol, r.Error)
		}
	}
	// one request for the model list, one for the probe; both must carry them
	for i := 0; i < 2; i++ {
		head := <-heads
		for _, want := range []string{"X-Org-Id: acme\r\n", "x-Gateway-Token: t1\r\n", "Authorization: Bearer override\r\n"} {
			if !strings.Contains(head, want) {
				t.Errorf("request %d missing %q in head:\n%s", i, want, head)
			}
		}
		if strings.Contains(head, "Authorization: Bearer sk-test") {
			t.Errorf("request %d kept the default Authorization instead of the override:\n%s", i, head)
		}
	}
}

func TestHeadersAbsentForPresetOnTheWire(t *testing.T) {
	hdrIsolate(t)
	base, heads := hdrRawServer(t)
	p := Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Chat: base + "/v1", Key: "sk-test",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	for _, r := range p.Test(context.Background()) {
		if !r.OK {
			t.Fatalf("probe %s failed: %s", r.Protocol, r.Error)
		}
	}
	for i := 0; i < 2; i++ {
		if head := <-heads; strings.Contains(strings.ToLower(head), "x-org-id") {
			t.Errorf("preset request %d carried a custom header:\n%s", i, head)
		}
	}
}

func TestHeadersOnModelListFetch(t *testing.T) {
	hdrIsolate(t)
	base, heads := hdrRawServer(t)
	p := Provider{ID: "hdr-list", Name: "List", Chat: base + "/v1", Key: "sk-test",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if head := <-heads; !strings.Contains(head, "X-Org-Id: acme\r\n") {
		t.Fatalf("model list request missing header:\n%s", head)
	}
}
