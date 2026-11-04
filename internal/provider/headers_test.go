package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// wireServer records each request's header lines exactly as they arrive.
// net/http canonicalises names on the server side, which would hide the case
// magpie sent, so this reads the raw bytes instead.
type wireServer struct {
	mu   sync.Mutex
	reqs [][]string // per request: "Name: value" lines, in order
}

func newWireServer(t *testing.T) (*wireServer, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ws := &wireServer{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go ws.serve(c)
		}
	}()
	return ws, "http://" + ln.Addr().String()
}

func (ws *wireServer) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	var lines []string
	length := 0
	first := true
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if first {
			first = false // request line
			continue
		}
		if line == "" {
			break
		}
		lines = append(lines, line)
		if i := strings.Index(line, ":"); i > 0 && line[:i] == "Content-Length" {
			length, _ = strconv.Atoi(strings.TrimSpace(line[i+1:]))
		}
	}
	io.CopyN(io.Discard, r, int64(length))
	ws.mu.Lock()
	ws.reqs = append(ws.reqs, lines)
	ws.mu.Unlock()

	body := `{"data":[{"id":"m1"}]}`
	fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

func (ws *wireServer) seen() [][]string {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return append([][]string(nil), ws.reqs...)
}

// exactHeader returns the values sent under exactly this name, case included.
func exactHeader(lines []string, name string) []string {
	var out []string
	for _, l := range lines {
		if i := strings.Index(l, ":"); i > 0 && l[:i] == name {
			out = append(out, strings.TrimSpace(l[i+1:]))
		}
	}
	return out
}

func isolateCache(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

func TestCustomHeadersOnEveryRequest(t *testing.T) {
	isolateCache(t)
	ws, base := newWireServer(t)
	p := Provider{ID: "relay", Name: "Relay", Chat: base + "/v1", Key: "sk-1", Headers: map[string]string{
		"X-Org-Id": "  acme  ",
		"   ":      "dropped",
		"X-ORG-id": "  padded  ",
	}}
	res := p.Test(context.Background())
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("test: %+v", res)
	}
	reqs := ws.seen()
	if len(reqs) != 2 { // the model list, then the probe
		t.Fatalf("want 2 upstream requests (models, probe), got %d", len(reqs))
	}
	for i, lines := range reqs {
		if got := exactHeader(lines, "X-Org-Id"); len(got) != 1 || got[0] != "acme" {
			t.Errorf("request %d: X-Org-Id = %q", i, got)
		}
		if got := exactHeader(lines, "X-ORG-id"); len(got) != 1 || got[0] != "padded" {
			t.Errorf("request %d: X-ORG-id = %q", i, got)
		}
		if got := exactHeader(lines, "Authorization"); len(got) != 1 || got[0] != "Bearer sk-1" {
			t.Errorf("request %d: Authorization = %q", i, got)
		}
	}
}

func TestCustomHeaderWinsOverAuth(t *testing.T) {
	isolateCache(t)
	ws, base := newWireServer(t)
	p := Provider{ID: "relay", Chat: base + "/v1", Key: "sk-1", Headers: map[string]string{
		"Authorization": "Custom abc",
		"x-api-key":     "tok-9",
	}}
	p.Test(context.Background())
	reqs := ws.seen()
	if len(reqs) == 0 {
		t.Fatal("no upstream requests")
	}
	for i, lines := range reqs {
		if got := exactHeader(lines, "Authorization"); len(got) != 1 || got[0] != "Custom abc" {
			t.Errorf("request %d: Authorization = %q", i, got)
		}
		if got := exactHeader(lines, "x-api-key"); len(got) != 1 || got[0] != "tok-9" {
			t.Errorf("request %d: x-api-key = %q", i, got)
		}
	}
}

func TestSignKeepsHeaderCaseAndOverrides(t *testing.T) {
	p := Provider{ID: "relay", Chat: "https://relay.example/v1", Key: "sk-1", Headers: map[string]string{
		"X-ORG-id":      "a",
		"X-Org-Id":      "b",
		"authorization": "Lower xyz",
	}}
	req, _ := http.NewRequest(http.MethodPost, p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if v := req.Header["X-ORG-id"]; len(v) != 1 || v[0] != "a" {
		t.Errorf("X-ORG-id: %q", v)
	}
	if v := req.Header["X-Org-Id"]; len(v) != 1 || v[0] != "b" {
		t.Errorf("X-Org-Id: %q", v)
	}
	if v := req.Header["authorization"]; len(v) != 1 || v[0] != "Lower xyz" {
		t.Errorf("authorization: %q", v)
	}
	if _, ok := req.Header["Authorization"]; ok {
		t.Errorf("standard Authorization should be replaced by the user's header")
	}
}

func TestPresetSendsNoCustomHeaders(t *testing.T) {
	isolateCache(t)
	ws, base := newWireServer(t)
	p := Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Chat: base + "/v1", Key: "sk-1",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	if n := normalize(p); n.Headers != nil {
		t.Errorf("preset kept headers after normalize: %v", n.Headers)
	}
	p.Test(context.Background())
	reqs := ws.seen()
	if len(reqs) == 0 {
		t.Fatal("no upstream requests")
	}
	for i, lines := range reqs {
		if got := exactHeader(lines, "X-Org-Id"); len(got) != 0 {
			t.Errorf("request %d: preset sent custom header: %q", i, got)
		}
	}
}

func TestAccountSendsNoCustomHeaders(t *testing.T) {
	p := Provider{ID: "codex", Account: &Account{Agent: "codex"}, Responses: "https://chatgpt.example/backend-api/codex",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	req, _ := http.NewRequest(http.MethodPost, p.Responses+"/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Header["X-Org-Id"]; ok {
		t.Errorf("signed-in account sent a custom header")
	}
}

func TestCleanHeaders(t *testing.T) {
	p := normalize(Provider{ID: "relay", Chat: "https://relay.example/v1", Headers: map[string]string{
		"  X-A  ":  "  1  ",
		"":          "no name",
		" \t ":      "blank name",
		"X-ORG-id": "x",
		"X-Org-Id": "y",
	}})
	want := map[string]string{"X-A": "1", "X-ORG-id": "x", "X-Org-Id": "y"}
	if len(p.Headers) != len(want) {
		t.Fatalf("headers = %v, want %v", p.Headers, want)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("%q = %q, want %q", k, p.Headers[k], v)
		}
	}
	if n := normalize(Provider{ID: "x", Chat: "https://x.example/v1", Headers: map[string]string{" ": "v"}}); n.Headers != nil {
		t.Errorf("only-blank names should leave no headers, got %v", n.Headers)
	}
}

func TestHeadersJSON(t *testing.T) {
	b, _ := json.Marshal(Provider{ID: "a", Chat: "https://a.example/v1"})
	if strings.Contains(string(b), "headers") {
		t.Errorf("empty headers were written: %s", b)
	}
	b, _ = json.Marshal(Provider{ID: "a", Chat: "https://a.example/v1", Headers: map[string]string{"X-Org-Id": "acme"}})
	if !strings.Contains(string(b), `"headers":{"X-Org-Id":"acme"}`) {
		t.Errorf("headers not written as expected: %s", b)
	}
}
