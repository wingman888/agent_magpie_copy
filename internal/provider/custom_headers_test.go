package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// customHdrsEnv points config and cache at a fresh directory for one test.
func customHdrsEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// customHdrsServer answers every request with a small model list and sends
// the raw request head (exact header casing, as on the wire) on heads.
func customHdrsServer(t *testing.T) (base string, heads chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	heads = make(chan string, 32)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				r := bufio.NewReader(conn)
				var head strings.Builder
				n := 0
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					head.WriteString(line)
					if strings.HasPrefix(strings.ToLower(line), "content-length:") {
						n, _ = strconv.Atoi(strings.TrimSpace(line[len("content-length:"):]))
					}
					if line == "\r\n" {
						break
					}
				}
				io.CopyN(io.Discard, r, int64(n))
				heads <- head.String()
				body := `{"data":[{"id":"m1"}]}`
				fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
			}(conn)
		}
	}()
	return "http://" + ln.Addr().String() + "/v1", heads
}

func TestCustomHdrsSaveKeepsCustomOnly(t *testing.T) {
	customHdrsEnv(t)
	err := Save(Provider{Name: "Acme Relay", Chat: "https://relay.example.com/v1", Key: "sk-test",
		Headers: map[string]string{"  X-Org-Id ": " acme ", "": "dropped", "   ": "dropped too", "X-ORG-id": "other"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find("acme-relay")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-Org-Id": "acme", "X-ORG-id": "other"}
	if len(p.Headers) != len(want) {
		t.Fatalf("headers = %v, want %v", p.Headers, want)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("header %q = %q, want %q", k, p.Headers[k], v)
		}
	}
	if got := p.CustomHeaders(); len(got) != 2 {
		t.Errorf("CustomHeaders = %v", got)
	}

	// a provider with no headers must not write the field at all
	if err := Save(Provider{ID: "plain", Chat: "https://plain.example.com/v1", Key: "sk"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), `"headers"`) != 1 {
		t.Errorf("providers.json should write headers only for acme: %s", b)
	}
}

func TestCustomHdrsPresetDropsHeaders(t *testing.T) {
	customHdrsEnv(t)
	if err := Save(Provider{Preset: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "sk-test",
		Headers: map[string]string{"X-Org-Id": "acme"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 0 || len(p.CustomHeaders()) != 0 {
		t.Errorf("preset kept headers: %v", p.Headers)
	}
	// even if a hand-edited file carries them, a preset sends none
	q := Provider{Preset: "deepseek", Chat: "https://api.deepseek.com/v1", Headers: map[string]string{"X-A": "1"}}
	if q.CustomHeaders() != nil {
		t.Errorf("preset CustomHeaders = %v", q.CustomHeaders())
	}
}

func TestCustomHdrsSignOverridesAuthAndKeepsCase(t *testing.T) {
	p := Provider{ID: "x", Chat: "https://relay.example.com/v1", Key: "sk-test",
		Headers: map[string]string{"Authorization": "Token abc", "X-ORG-id": "acme"}}
	req, _ := http.NewRequest(http.MethodPost, p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header["Authorization"]; len(got) != 1 || got[0] != "Token abc" {
		t.Errorf("Authorization = %v", got)
	}
	if got := req.Header["X-ORG-id"]; len(got) != 1 || got[0] != "acme" {
		t.Errorf("X-ORG-id = %v", got)
	}
	if _, ok := req.Header["X-Org-Id"]; ok {
		t.Errorf("header name was canonicalized: %v", req.Header)
	}

	// a lower-case user name replaces the auth header written under its
	// canonical name, instead of being sent alongside it
	q := Provider{ID: "y", Chat: "https://relay.example.com/v1", Key: "sk-test",
		Headers: map[string]string{"authorization": "Token low"}}
	req, _ = http.NewRequest(http.MethodPost, q.Chat, nil)
	if err := q.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Header["Authorization"]; ok {
		t.Errorf("auth header survived a same-named user header: %v", req.Header)
	}
	if got := req.Header["authorization"]; len(got) != 1 || got[0] != "Token low" {
		t.Errorf("authorization = %v", got)
	}
}

func TestCustomHdrsOnTheWire(t *testing.T) {
	customHdrsEnv(t)
	base, heads := customHdrsServer(t)
	p := Provider{ID: "wire", Name: "Wire", Chat: base, Key: "sk-test",
		Headers: map[string]string{"X-ORG-id": "acme", "Authorization": "Token abc"}}

	// the forwarded/tested request
	if r := probe(context.Background(), p, Chat, base+"/chat/completions", []byte(`{}`), "m1"); !r.OK {
		t.Fatalf("probe failed: %+v", r)
	}
	head := <-heads
	for _, want := range []string{"X-ORG-id: acme\r\n", "Authorization: Token abc\r\n"} {
		if !strings.Contains(head, want) {
			t.Errorf("request missing %q:\n%s", want, head)
		}
	}
	if strings.Contains(head, "Bearer sk-test") || strings.Contains(head, "X-Org-Id") {
		t.Errorf("auth or canonical name leaked:\n%s", head)
	}

	// the model list fetch
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	head = <-heads
	if !strings.Contains(head, "X-ORG-id: acme\r\n") {
		t.Errorf("model list request missing header:\n%s", head)
	}
}

func TestCustomHdrsNoneSendsNone(t *testing.T) {
	// a provider without headers gets only its usual auth header
	p := Provider{ID: "none", Chat: "https://relay.example.com/v1", Key: "sk-test"}
	req, _ := http.NewRequest(http.MethodPost, p.Chat, nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q", got)
	}
}
