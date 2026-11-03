package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// rawUpstream answers HTTP/1.1 requests and records each request's header
// lines exactly as they arrived, so header-name case can be checked. Go's
// own server canonicalizes names, which would hide what went on the wire.
type rawUpstream struct {
	mu   sync.Mutex
	reqs [][]string // "Name: value" lines per request
}

func newRawUpstream(t *testing.T) (*rawUpstream, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	u := &rawUpstream{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go u.serve(c)
		}
	}()
	return u, "http://" + ln.Addr().String()
}

func (u *rawUpstream) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	parts := strings.Fields(line)
	path := ""
	if len(parts) > 1 {
		path = parts[1]
	}
	var hdrs []string
	clen := 0
	for {
		l, err := r.ReadString('\n')
		if err != nil {
			return
		}
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			break
		}
		hdrs = append(hdrs, l)
		if n, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(strings.TrimSpace(n), "Content-Length") {
			clen, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	io.CopyN(io.Discard, r, int64(clen))
	u.mu.Lock()
	u.reqs = append(u.reqs, hdrs)
	u.mu.Unlock()

	body := `{"id":"x"}`
	if strings.HasSuffix(path, "/models") {
		body = `{"data":[{"id":"m1"}]}`
	}
	fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

func (u *rawUpstream) seen() [][]string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]string(nil), u.reqs...)
}

// countNamed counts lines whose name matches case-insensitively.
func countNamed(hdrs []string, name string) int {
	n := 0
	for _, l := range hdrs {
		if k, _, _ := strings.Cut(l, ":"); strings.EqualFold(strings.TrimSpace(k), name) {
			n++
		}
	}
	return n
}

func hasLine(hdrs []string, line string) bool {
	for _, l := range hdrs {
		if l == line {
			return true
		}
	}
	return false
}

func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestCustomHeadersReachEveryRequestWithCaseKept(t *testing.T) {
	isolateConfig(t)
	up, base := newRawUpstream(t)
	p := Provider{ID: "relay", Name: "Relay", Key: "sk-test", Models: []string{"m1"},
		Chat: base + "/v1", Headers: map[string]string{"X-Org-Id": "acme"}}

	// provider test: the probe, then the model list the test starts from
	res := p.Test(context.Background())
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("test: %+v", res)
	}
	// model list fetched on its own
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	seen := up.seen()
	if len(seen) < 3 {
		t.Fatalf("want a models fetch and a probe, saw %d requests", len(seen))
	}
	for i, hdrs := range seen {
		if !hasLine(hdrs, "X-Org-Id: acme") {
			t.Errorf("request %d lacks X-Org-Id with its case kept: %q", i, hdrs)
		}
		if countNamed(hdrs, "x-org-id") != 1 {
			t.Errorf("request %d: header duplicated: %q", i, hdrs)
		}
		if !hasLine(hdrs, "Authorization: Bearer sk-test") {
			t.Errorf("request %d lost its auth header: %q", i, hdrs)
		}
	}
}

func TestCustomHeaderOverridesDefaultAfterAuth(t *testing.T) {
	isolateConfig(t)
	up, base := newRawUpstream(t)
	p := Provider{ID: "anth", Name: "Anth", Key: "sk-test", Models: []string{"m1"},
		Anthropic: base, Headers: map[string]string{"anthropic-version": "2024-01-01"}}

	if res := p.Test(context.Background()); len(res) != 1 || !res[0].OK {
		t.Fatalf("test: %+v", res)
	}
	seen := up.seen()
	last := seen[len(seen)-1]
	if countNamed(last, "anthropic-version") != 1 || !hasLine(last, "anthropic-version: 2024-01-01") {
		t.Fatalf("override not applied once: %q", last)
	}
}

func TestPresetAndAccountSendNoCustomHeaders(t *testing.T) {
	preset := Provider{ID: "deepseek", Preset: "deepseek", Headers: map[string]string{"X-Org-Id": "acme"}}
	if h := preset.ExtraHeaders(); h != nil {
		t.Errorf("preset sends %v", h)
	}
	acct := Provider{ID: "claude", Account: &Account{Agent: "claude"}, Headers: map[string]string{"X-Org-Id": "acme"}}
	if h := acct.ExtraHeaders(); h != nil {
		t.Errorf("account sends %v", h)
	}
	custom := Provider{ID: "mine", Headers: map[string]string{"X-Org-Id": "acme"}}
	if h := custom.ExtraHeaders(); h["X-Org-Id"] != "acme" {
		t.Errorf("custom sends %v", h)
	}
}

func TestHeadersNormalizedAndStored(t *testing.T) {
	isolateConfig(t)
	if err := Save(Provider{ID: "mine", Name: "Mine", Chat: "https://x.example/v1", Key: "k",
		Headers: map[string]string{" X-Org-Id ": " acme ", "": "dropped", "  ": "dropped too"}}); err != nil {
		t.Fatal(err)
	}
	got, err := Find("mine")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Headers) != 1 || got.Headers["X-Org-Id"] != "acme" {
		t.Fatalf("normalized headers: %#v", got.Headers)
	}

	// an empty map is not written at all
	if err := Save(Provider{ID: "bare", Name: "Bare", Chat: "https://y.example/v1", Key: "k",
		Headers: map[string]string{"": "dropped"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.Providers {
		_, has := p["headers"]
		if p["id"] == "bare" && has {
			t.Errorf("empty headers were written: %v", p)
		}
		if p["id"] == "mine" && !has {
			t.Errorf("headers were not written: %v", p)
		}
	}
}
