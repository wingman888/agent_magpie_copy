package provider

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// rawReq is one request as it arrived on the wire, header names unchanged.
type rawReq struct {
	Line  string
	Lines []string          // header lines, "Name: value", as sent
	Hdr   map[string]string // header value by name exactly as sent
}

// rawServer answers every request with an empty JSON object and records what
// arrived. net/http's server canonicalizes names, so this reads raw bytes to
// see the case magpie actually sent.
func rawServer(t *testing.T) (string, chan rawReq) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	reqs := make(chan rawReq, 64)
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

func serveRaw(c net.Conn, reqs chan<- rawReq) {
	defer c.Close()
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	r := rawReq{Line: strings.TrimSpace(line), Hdr: map[string]string{}}
	clen := 0
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			return
		}
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			break
		}
		r.Lines = append(r.Lines, l)
		name, val, _ := strings.Cut(l, ":")
		val = strings.TrimSpace(val)
		r.Hdr[name] = val
		if strings.EqualFold(name, "Content-Length") {
			clen, _ = strconv.Atoi(val)
		}
	}
	io.CopyN(io.Discard, br, int64(clen))
	reqs <- r
	io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
}

// setupConfig points magpie's config and cache at a temp dir.
func setupConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func drain(reqs chan rawReq) []rawReq {
	var out []rawReq
	for {
		select {
		case r := <-reqs:
			out = append(out, r)
		default:
			return out
		}
	}
}

func TestHeaderNormalizeTrimsAndDropsEmptyNames(t *testing.T) {
	p := normalize(Provider{Name: "R", Headers: map[string]string{
		"  X-A  ":  "  v  ",
		"":         "dropped",
		"   ":      "dropped too",
		"X-ORG-id": "kept",
		"X-Org-Id": "also kept",
	}})
	want := map[string]string{"X-A": "v", "X-ORG-id": "kept", "X-Org-Id": "also kept"}
	if len(p.Headers) != len(want) {
		t.Fatalf("Headers = %v, want %v", p.Headers, want)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("Headers[%q] = %q, want %q", k, p.Headers[k], v)
		}
	}
}

func TestPresetNeverKeepsHeaders(t *testing.T) {
	setupConfig(t)
	p := Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Key: "k",
		Chat: "https://api.deepseek.com/v1", Headers: map[string]string{"X-Org-Id": "acme"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Find("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Headers) != 0 {
		t.Errorf("preset kept headers: %v", got.Headers)
	}
	if hs := got.customHeaders(); len(hs) != 0 {
		t.Errorf("preset has custom headers: %v", hs)
	}
}

func TestSaveRejectsBadHeaders(t *testing.T) {
	setupConfig(t)
	for _, hs := range []map[string]string{
		{"bad name": "v"},
		{"X-Bad:": "v"},
		{"X-Ok": "line\r\nInjected: 1"},
	} {
		err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k", Headers: hs})
		if err == nil {
			t.Errorf("Save accepted %v", hs)
		}
	}
}

func TestSaveKeepsHeaderNameCase(t *testing.T) {
	setupConfig(t)
	err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k",
		Headers: map[string]string{"X-ORG-id": "a", "X-Org-Id": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if got.Headers["X-ORG-id"] != "a" || got.Headers["X-Org-Id"] != "b" || len(got.Headers) != 2 {
		t.Errorf("Headers = %v", got.Headers)
	}
}

func TestApplyHeadersOverridesAuthIgnoringCase(t *testing.T) {
	p := Provider{ID: "relay", Chat: "https://relay.example.com/v1", Key: "k",
		Headers: map[string]string{"authorization": "Token mine", "X-ORG-id": "acme", "X-Org-Id": "other"}}
	req := httptest.NewRequest(http.MethodPost, "https://relay.example.com/v1/models", nil)
	req.Header.Set("Authorization", "Bearer k")
	for k, v := range AuthHeaders(p, Chat) {
		req.Header.Set(k, v)
	}
	p.ApplyHeaders(req)
	if got := req.Header["Authorization"]; len(got) != 0 {
		t.Errorf("canonical Authorization still present: %v", got)
	}
	if got := req.Header["authorization"]; len(got) != 1 || got[0] != "Token mine" {
		t.Errorf("authorization = %v, want [Token mine]", got)
	}
	if got := req.Header["X-ORG-id"]; len(got) != 1 || got[0] != "acme" {
		t.Errorf("X-ORG-id = %v", got)
	}
	if got := req.Header["X-Org-Id"]; len(got) != 1 || got[0] != "other" {
		t.Errorf("X-Org-Id = %v", got)
	}
}

func TestPresetAndAccountSendNoCustomHeaders(t *testing.T) {
	hs := map[string]string{"X-Org-Id": "acme", "Authorization": "mine"}
	for name, p := range map[string]Provider{
		"preset":  {ID: "deepseek", Preset: "deepseek", Key: "k", Headers: hs},
		"account": {ID: "codex", Account: &Account{Agent: "codex", User: "u"}, Headers: hs},
	} {
		req := httptest.NewRequest(http.MethodGet, "https://example.com/v1/models", nil)
		req.Header.Set("Authorization", "Bearer real")
		p.ApplyHeaders(req)
		if req.Header.Get("X-Org-Id") != "" {
			t.Errorf("%s: sent X-Org-Id", name)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer real" {
			t.Errorf("%s: Authorization = %q", name, got)
		}
		if c := p.ClientFor(http.DefaultClient); c != http.DefaultClient {
			t.Errorf("%s: client changed", name)
		}
	}
}

func TestIsToken(t *testing.T) {
	for s, want := range map[string]bool{
		"X-Org-Id": true, "X-ORG-id": true, "x_tenant.1": true,
		"": false, "bad name": false, "X-Bad:": false, "X-é": false,
	} {
		if got := isToken(s); got != want {
			t.Errorf("isToken(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestFetchSendsCustomHeadersAsTyped(t *testing.T) {
	setupConfig(t)
	base, reqs := rawServer(t)
	p := Provider{ID: "relay", Name: "Relay", Chat: base + "/v1", Key: "k",
		Headers: map[string]string{"X-ORG-id": "acme", "X-Org-Id": "other", "Authorization": "Token mine"}}
	p.Fetch(context.Background()) // the list is empty; only the requests matter

	got := drain(reqs)
	if len(got) == 0 {
		t.Fatal("no request reached the upstream")
	}
	for _, r := range got {
		if r.Hdr["X-ORG-id"] != "acme" || r.Hdr["X-Org-Id"] != "other" {
			t.Errorf("%s: custom headers %v", r.Line, r.Hdr)
		}
		auth := 0
		for _, l := range r.Lines {
			if strings.HasPrefix(strings.ToLower(l), "authorization:") {
				auth++
				if l != "Authorization: Token mine" {
					t.Errorf("%s: auth line %q", r.Line, l)
				}
			}
		}
		if auth != 1 {
			t.Errorf("%s: %d Authorization headers, want 1", r.Line, auth)
		}
	}
}

func TestTestProbeSendsCustomHeaders(t *testing.T) {
	setupConfig(t)
	base, reqs := rawServer(t)
	p := Provider{ID: "relay", Name: "Relay", Anthropic: base, Key: "k", Models: []string{"m1"},
		Headers: map[string]string{"X-Org-Id": "acme", "x-api-key": "tenant-key"}}
	p.Test(context.Background())

	got := drain(reqs)
	var posts int
	for _, r := range got {
		if r.Hdr["X-Org-Id"] != "acme" {
			t.Errorf("%s: X-Org-Id = %q", r.Line, r.Hdr["X-Org-Id"])
		}
		if r.Hdr["x-api-key"] != "tenant-key" {
			t.Errorf("%s: x-api-key = %q, want the user's value", r.Line, r.Hdr["x-api-key"])
		}
		if strings.HasPrefix(r.Line, "POST ") {
			posts++
		}
	}
	if posts != 1 {
		t.Errorf("probe POSTs = %d, want 1 (requests: %v)", posts, got)
	}
}

func TestFetchWithoutHeadersStillWorks(t *testing.T) {
	setupConfig(t)
	base, reqs := rawServer(t)
	p := Provider{ID: "relay", Name: "Relay", Chat: base + "/v1", Key: "k"}
	p.Fetch(context.Background())
	for _, r := range drain(reqs) {
		for _, l := range r.Lines {
			if strings.HasPrefix(strings.ToLower(l), "x-org-id:") {
				t.Errorf("unexpected header %q", l)
			}
		}
	}
}
