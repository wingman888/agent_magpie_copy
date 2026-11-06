package provider

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// isolate points HOME and the XDG dirs at a fresh temp dir, so nothing reads
// or writes the developer's real config, cache or agent sign-ins.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// rawServer answers every request with a one-model list and records the
// request lines and header lines exactly as they arrive. net/http's server
// canonicalises header names, which would hide the case the test checks.
type rawServer struct {
	ln   net.Listener
	mu   sync.Mutex
	reqs []rawRequest
}

type rawRequest struct {
	line    string
	headers []string
}

func newRawServer(t *testing.T) *rawServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &rawServer{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *rawServer) URL() string { return "http://" + s.ln.Addr().String() }

func (s *rawServer) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	var req rawRequest
	length := 0
	for first := true; ; first = false {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if first {
			req.line = line
			continue
		}
		if line == "" {
			break
		}
		req.headers = append(req.headers, line)
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			length, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	if length > 0 {
		io.CopyN(io.Discard, r, int64(length))
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	body := `{"data":[{"id":"m1"}]}`
	io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "+
		strconv.Itoa(len(body))+"\r\nConnection: close\r\n\r\n"+body)
}

func (s *rawServer) seen() []rawRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]rawRequest(nil), s.reqs...)
}

func hasLine(headers []string, want string) bool {
	for _, h := range headers {
		if h == want {
			return true
		}
	}
	return false
}

func TestCustomHeadersReachUpstreamWithTheirCase(t *testing.T) {
	isolate(t)
	srv := newRawServer(t)
	p := Provider{ID: "relay", Name: "Relay", Chat: srv.URL() + "/v1", Key: "sk-test",
		Headers: map[string]string{"X-Org-Id": "acme"}}

	for _, r := range p.Test(context.Background()) {
		if !r.OK {
			t.Fatalf("probe %s failed: %s", r.Protocol, r.Error)
		}
	}
	reqs := srv.seen()
	if len(reqs) < 2 {
		t.Fatalf("want the model list and the probe on the wire, got %d requests", len(reqs))
	}
	for _, r := range reqs {
		if !hasLine(r.headers, "X-Org-Id: acme") {
			t.Errorf("%s: X-Org-Id not sent as typed; headers %q", r.line, r.headers)
		}
	}
}

func TestCustomHeaderOverridesDefaultAuth(t *testing.T) {
	isolate(t)
	srv := newRawServer(t)
	p := Provider{ID: "relay", Name: "Relay", Chat: srv.URL() + "/v1", Key: "sk-test",
		Headers: map[string]string{"authorization": "Bearer custom"}}

	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := srv.seen()
	if len(reqs) == 0 {
		t.Fatal("no request reached the upstream")
	}
	for _, r := range reqs {
		if !hasLine(r.headers, "authorization: Bearer custom") {
			t.Errorf("override not sent as typed; headers %q", r.headers)
		}
		for _, h := range r.headers {
			if strings.EqualFold(h, "Authorization: Bearer sk-test") {
				t.Errorf("default Authorization was not replaced: %q", h)
			}
		}
	}
}

func TestPresetAndAccountIgnoreCustomHeaders(t *testing.T) {
	pr, err := FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	pr.Key = "sk-test"
	pr.Headers = map[string]string{"X-Org-Id": "acme"}
	if pr.CustomHeaders() != nil {
		t.Fatal("a preset must not carry custom headers")
	}
	req, _ := http.NewRequest(http.MethodPost, pr.Chat+"/chat/completions", nil)
	if err := pr.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	for k := range req.Header {
		if strings.EqualFold(k, "X-Org-Id") {
			t.Errorf("preset request carries %s", k)
		}
	}

	acct := Provider{ID: "claude", Account: &Account{}, Headers: map[string]string{"X-Org-Id": "acme"}}
	if acct.CustomHeaders() != nil {
		t.Fatal("a signed-in account must not carry custom headers")
	}
}

func TestHeadersAreNormalized(t *testing.T) {
	p := normalize(Provider{ID: "x", Headers: map[string]string{" X-A ": " 1 ", "": "dropped", "   ": "dropped too"}})
	if len(p.Headers) != 1 || p.Headers["X-A"] != "1" {
		t.Fatalf("headers = %#v, want only X-A=1", p.Headers)
	}
	if q := normalize(Provider{ID: "x", Headers: map[string]string{"": "x"}}); q.Headers != nil {
		t.Fatalf("an empty map must not be kept, got %#v", q.Headers)
	}
}
