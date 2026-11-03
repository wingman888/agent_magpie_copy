package provider

import (
	"bufio"
	"context"
	"encoding/json"
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

// rawRequest is one request as it came off the wire: the request line and
// every header line, exactly as sent (case included).
type rawRequest struct {
	line    string
	headers []string
}

// rawServer answers every request with a model list and reports what each
// one looked like on the wire. net/http would canonicalise header names on
// the server side, which would hide the case this test is about.
func rawServer(t *testing.T) (string, <-chan rawRequest) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	reqs := make(chan rawRequest, 64)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveRaw(c, reqs)
		}
	}()
	return "http://" + ln.Addr().String(), reqs
}

func serveRaw(c net.Conn, reqs chan<- rawRequest) {
	defer c.Close()
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	req := rawRequest{line: strings.TrimRight(line, "\r\n")}
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
		req.headers = append(req.headers, l)
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			length, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	io.CopyN(io.Discard, br, int64(length))
	reqs <- req
	body := `{"data":[{"id":"m1"}]}`
	fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

// hasLine reports whether a header line appears exactly as given.
func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func drain(reqs <-chan rawRequest) []rawRequest {
	var out []rawRequest
	for {
		select {
		case r := <-reqs:
			out = append(out, r)
		default:
			return out
		}
	}
}

func TestCustomHeadersReachUpstreamWithCase(t *testing.T) {
	isolateConfig(t)
	base, reqs := rawServer(t)
	p := Provider{
		ID: "org", Name: "Org", Chat: base + "/v1", Key: "sk-1",
		Headers: map[string]string{"X-Org-Id": "acme", "x-Gateway": "gw", "authorization": "Token abc"},
	}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	saved, err := Find("org")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := saved.Fetch(ctx); err != nil {
		t.Fatalf("model list: %v", err)
	}
	for _, r := range drain(reqs) {
		if !hasLine(r.headers, "X-Org-Id: acme") {
			t.Errorf("%s: X-Org-Id missing or re-cased in %q", r.line, r.headers)
		}
		if !hasLine(r.headers, "x-Gateway: gw") {
			t.Errorf("%s: x-Gateway missing or re-cased in %q", r.line, r.headers)
		}
		// The user's authorization header overrides the default bearer one.
		if !hasLine(r.headers, "authorization: Token abc") {
			t.Errorf("%s: user authorization header not sent as typed: %q", r.line, r.headers)
		}
		for _, l := range r.headers {
			if strings.HasPrefix(strings.ToLower(l), "authorization:") && l != "authorization: Token abc" {
				t.Errorf("default authorization header still sent: %q", l)
			}
		}
	}

	// provider test goes through the same path.
	for _, res := range saved.Test(ctx) {
		if !res.OK {
			t.Errorf("test %s: %s", res.Protocol, res.Error)
		}
	}
	probes := drain(reqs)
	if len(probes) == 0 {
		t.Fatal("provider test sent nothing")
	}
	for _, r := range probes {
		if !hasLine(r.headers, "X-Org-Id: acme") {
			t.Errorf("test request %s lacks X-Org-Id: %q", r.line, r.headers)
		}
	}
}

func TestHeadersNotSentToPresetsOrAccounts(t *testing.T) {
	isolateConfig(t)
	hs := map[string]string{"X-Org-Id": "acme"}

	preset := Provider{ID: "openai", Name: "OpenAI", Preset: "openai", Chat: "https://api.openai.com/v1", Key: "sk", Headers: hs}
	req, _ := http.NewRequest(http.MethodPost, preset.Chat+"/chat/completions", nil)
	if err := preset.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Header["X-Org-Id"]; ok {
		t.Error("preset received a custom header")
	}

	acct := Provider{ID: "codex", Name: "Codex", Responses: CodexBase, Headers: hs, Account: &Account{Agent: "codex", User: "me@example.com"}}
	req, _ = http.NewRequest(http.MethodPost, acct.Responses+"/responses", nil)
	if err := acct.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Header["X-Org-Id"]; ok {
		t.Error("signed-in account received a custom header")
	}
}

func TestSignAppliesHeadersAfterAuth(t *testing.T) {
	isolateConfig(t)
	p := Provider{ID: "relay", Name: "Relay", Anthropic: "https://relay.example", Key: "sk-1",
		Headers: map[string]string{"x-api-key": "override", "X-Org-Id": "acme"}}
	req, _ := http.NewRequest(http.MethodPost, p.Anthropic+"/v1/messages", nil)
	if err := p.Sign(context.Background(), req, Anthropic, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header["x-api-key"]; len(got) != 1 || got[0] != "override" {
		t.Errorf("x-api-key = %q, want the user's override", got)
	}
	if _, ok := req.Header["X-Api-Key"]; ok {
		t.Error("default X-Api-Key kept beside the override")
	}
	if got := req.Header["X-Org-Id"]; len(got) != 1 || got[0] != "acme" {
		t.Errorf("X-Org-Id = %q", got)
	}
}

func TestSaveCleansHeaders(t *testing.T) {
	isolateConfig(t)
	err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk",
		Headers: map[string]string{"  X-Org-Id ": " acme ", "   ": "dropped", "": "dropped"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 1 || p.Headers["X-Org-Id"] != "acme" {
		t.Fatalf("headers = %#v", p.Headers)
	}

	// An empty map is never written.
	if err := Save(Provider{ID: "bare", Name: "Bare", Chat: "https://bare.example/v1", Key: "sk",
		Headers: map[string]string{" ": "x"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(Path()), "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, pv := range f.Providers {
		if pv["id"] == "bare" {
			if _, ok := pv["headers"]; ok {
				t.Errorf("empty headers written: %v", pv["headers"])
			}
		}
	}
}
