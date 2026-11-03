package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// rawHeader is one header line as it came off the wire.
type rawHeader struct{ Name, Value string }

// rawRequest is one request the raw upstream received.
type rawRequest struct {
	Method, Path string
	Headers      []rawHeader
}

// rawUpstream is an HTTP/1.1 server that records each request's header
// names exactly as sent. net/http's server canonicalizes names on read, so
// it cannot show whether a header kept its case.
type rawUpstream struct {
	URL  string
	body string

	mu   sync.Mutex
	reqs []rawRequest
}

func newRawUpstream(t *testing.T, body string) *rawUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	u := &rawUpstream{URL: "http://" + ln.Addr().String(), body: body}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go u.serve(c)
		}
	}()
	return u
}

func (u *rawUpstream) serve(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return
		}
		req := rawRequest{Method: fields[0], Path: fields[1]}
		length := 0
		for {
			h, err := br.ReadString('\n')
			if err != nil {
				return
			}
			h = strings.TrimRight(h, "\r\n")
			if h == "" {
				break
			}
			name, value, _ := strings.Cut(h, ":")
			value = strings.TrimSpace(value)
			req.Headers = append(req.Headers, rawHeader{Name: name, Value: value})
			if strings.EqualFold(name, "Content-Length") {
				length, _ = strconv.Atoi(value)
			}
		}
		if _, err := io.CopyN(io.Discard, br, int64(length)); err != nil {
			return
		}
		u.mu.Lock()
		u.reqs = append(u.reqs, req)
		u.mu.Unlock()
		fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(u.body), u.body)
	}
}

// requests returns what the upstream has seen so far.
func (u *rawUpstream) requests() []rawRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]rawRequest(nil), u.reqs...)
}

// values returns the values of the header lines named exactly name, and how
// many lines carry that name ignoring case.
func values(r rawRequest, name string) (exact []string, count int) {
	for _, h := range r.Headers {
		if strings.EqualFold(h.Name, name) {
			count++
		}
		if h.Name == name {
			exact = append(exact, h.Value)
		}
	}
	return exact, count
}

// isolateConfig points magpie's config and cache at temp dirs.
func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

const modelsBody = `{"data":[{"id":"m1"}]}`

func TestCustomHeadersReachProbeAndModelList(t *testing.T) {
	isolateConfig(t)
	up := newRawUpstream(t, modelsBody)
	p := Provider{
		ID: "relay", Name: "Relay", Key: "sk-test",
		Chat:    up.URL + "/v1",
		Headers: map[string]string{"X-Org-Id": "acme", "x-gateway-auth": "tok"},
	}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	saved, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}

	// model list fetch
	if _, err := saved.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	// connectivity test (also fetches, then probes the chat endpoint)
	for _, r := range saved.Test(context.Background()) {
		if !r.OK {
			t.Fatalf("probe failed: %+v", r)
		}
	}

	reqs := up.requests()
	if len(reqs) < 2 {
		t.Fatalf("upstream saw %d requests, want the model list and the probe", len(reqs))
	}
	for _, r := range reqs {
		if got, _ := values(r, "X-Org-Id"); len(got) != 1 || got[0] != "acme" {
			t.Errorf("%s %s: X-Org-Id = %q, want exactly [acme]", r.Method, r.Path, got)
		}
		if got, _ := values(r, "x-gateway-auth"); len(got) != 1 || got[0] != "tok" {
			t.Errorf("%s %s: x-gateway-auth = %q, want exactly [tok] with the name as typed", r.Method, r.Path, got)
		}
		if got, _ := values(r, "Authorization"); len(got) != 1 || got[0] != "Bearer sk-test" {
			t.Errorf("%s %s: Authorization = %q", r.Method, r.Path, got)
		}
	}
}

func TestCustomHeadersKeepCaseOnWire(t *testing.T) {
	isolateConfig(t)
	up := newRawUpstream(t, modelsBody)
	p := Provider{ID: "relay", Name: "Relay", Key: "k", Chat: up.URL + "/v1",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(up.requests()) == 0 {
		t.Fatal("no request reached upstream")
	}
	r := up.requests()[0]
	if got, _ := values(r, "X-Org-Id"); len(got) != 1 {
		t.Errorf("X-Org-Id not sent with that exact case; headers: %+v", r.Headers)
	}
	for _, h := range r.Headers {
		if strings.EqualFold(h.Name, "X-Org-Id") && h.Name != "X-Org-Id" {
			t.Errorf("header sent as %q, want X-Org-Id", h.Name)
		}
	}
}

func TestCustomHeaderOverridesDefault(t *testing.T) {
	isolateConfig(t)
	up := newRawUpstream(t, modelsBody)
	// the probe sets anthropic-version itself; the custom one must replace it
	p := Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: up.URL,
		Headers: map[string]string{"anthropic-version": "2099-01-01"}}
	p.Test(context.Background())
	reqs := up.requests()
	if len(reqs) == 0 {
		t.Fatal("no request reached upstream")
	}
	for _, r := range reqs {
		got, count := values(r, "anthropic-version")
		if count != 1 || len(got) != 1 || got[0] != "2099-01-01" {
			t.Errorf("%s %s: anthropic-version = %v (%d lines), want one line 2099-01-01", r.Method, r.Path, got, count)
		}
	}
}

func TestPresetAndAccountNeverSendCustomHeaders(t *testing.T) {
	isolateConfig(t)
	up := newRawUpstream(t, modelsBody)
	// a preset whose file somehow carries headers
	p := Provider{ID: "deepseek", Name: "DeepSeek", Key: "k", Preset: "deepseek", Chat: up.URL + "/v1",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.Test(context.Background())
	for _, r := range up.requests() {
		if got, count := values(r, "X-Org-Id"); count != 0 {
			t.Errorf("preset sent X-Org-Id %q", got)
		}
	}
	if got := p.CustomHeaders(); got != nil {
		t.Errorf("preset CustomHeaders = %v, want nil", got)
	}
	acct := Provider{ID: "claude", Account: &Account{Agent: "claude"}, Headers: map[string]string{"X-Org-Id": "acme"}}
	if got := acct.CustomHeaders(); got != nil {
		t.Errorf("account CustomHeaders = %v, want nil", got)
	}
}

func TestHeaderCleanupOnSave(t *testing.T) {
	isolateConfig(t)
	err := Save(Provider{Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k",
		Headers: map[string]string{
			"  X-Org-Id  ": "  acme  ",
			"":             "dropped",
			"   ":          "dropped too",
			"x-empty":      "",
		}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-Org-Id": "acme", "x-empty": ""}
	if len(p.Headers) != len(want) {
		t.Fatalf("Headers = %v, want %v", p.Headers, want)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("Headers[%q] = %q, want %q", k, p.Headers[k], v)
		}
	}
}

func TestEmptyHeadersNotWritten(t *testing.T) {
	isolateConfig(t)
	if err := Save(Provider{Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k",
		Headers: map[string]string{"": "x", " ": "y"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "magpie", "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "headers") {
		t.Errorf("providers.json still has a headers key:\n%s", b)
	}
}

func TestHeadersSurviveReload(t *testing.T) {
	isolateConfig(t)
	if err := Save(Provider{Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k",
		Headers: map[string]string{"X-Org-Id": "acme"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Headers["X-Org-Id"] != "acme" {
		t.Errorf("Headers after reload = %v", p.Headers)
	}
}
