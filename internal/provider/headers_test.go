package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// rawReq is one request as it arrived on the wire: header lines are kept
// byte-for-byte, because net/http's server canonicalises names on read and
// would hide the casing this test is about.
type rawReq struct {
	line    string
	headers []string // "Name: value", as sent
}

// values returns the values sent under exactly this header name (case-sensitive).
func (r rawReq) values(name string) []string {
	var out []string
	for _, h := range r.headers {
		i := strings.IndexByte(h, ':')
		if i > 0 && h[:i] == name {
			out = append(out, strings.TrimSpace(h[i+1:]))
		}
	}
	return out
}

// rawUpstream starts a fake OpenAI-style upstream that records every request
// and answers each with a one-model list. It returns the base URL (ending in
// /v1) and an accessor for what it has seen so far.
func rawUpstream(t *testing.T) (string, func() []rawReq) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var seen []rawReq
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveRaw(c, &mu, &seen)
		}
	}()
	base := "http://" + ln.Addr().String() + "/v1"
	return base, func() []rawReq {
		mu.Lock()
		defer mu.Unlock()
		return append([]rawReq(nil), seen...)
	}
}

func serveRaw(c net.Conn, mu *sync.Mutex, seen *[]rawReq) {
	defer c.Close()
	r := bufio.NewReader(c)
	var rq rawReq
	clen := 0
	for first := true; ; first = false {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if first {
			rq.line = line
			continue
		}
		if line == "" {
			break
		}
		rq.headers = append(rq.headers, line)
		if i := strings.IndexByte(line, ':'); i > 0 && strings.EqualFold(strings.TrimSpace(line[:i]), "Content-Length") {
			clen, _ = strconv.Atoi(strings.TrimSpace(line[i+1:]))
		}
	}
	io.CopyN(io.Discard, r, int64(clen))
	mu.Lock()
	*seen = append(*seen, rq)
	mu.Unlock()
	body := `{"data":[{"id":"m1"}]}`
	fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

// isolateAll keeps config and cache inside a temp dir for the test.
func isolateAll(t *testing.T) {
	t.Helper()
	isolateConfig(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// sendSigned builds a request to url, signs it for p, and sends it the way
// the gateway's forward path does.
func sendSigned(t *testing.T, p Provider, proto Protocol, url string) {
	t.Helper()
	body := []byte(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := p.Sign(context.Background(), req, proto, body); err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
}

func TestCustomHeadersReachEveryUpstreamRequest(t *testing.T) {
	isolateAll(t)
	base, seen := rawUpstream(t)
	p := Provider{
		ID: "relay", Name: "Relay", Key: "sk-user", Chat: base,
		Headers: map[string]string{
			"X-Org-Id":      "acme",
			"X-ORG-id":      "lower",
			"Authorization": "Bearer mine", // overrides the key-derived one
		},
	}
	ctx := context.Background()

	// model list
	if _, err := p.Fetch(ctx); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// connectivity test
	res := p.Test(ctx)
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("Test: %+v", res)
	}
	// forwarded call
	sendSigned(t, p, Chat, base+"/chat/completions")

	got := seen()
	// Fetch, then Test (which fetches again and probes), then the forward.
	if len(got) != 4 {
		t.Fatalf("want 4 upstream requests (fetch, fetch+probe, forward), got %d", len(got))
	}
	for _, rq := range got {
		if v := rq.values("X-Org-Id"); len(v) != 1 || v[0] != "acme" {
			t.Errorf("%s: X-Org-Id = %v, want [acme]; headers %v", rq.line, v, rq.headers)
		}
		if v := rq.values("X-ORG-id"); len(v) != 1 || v[0] != "lower" {
			t.Errorf("%s: X-ORG-id = %v, want [lower] (name kept as typed)", rq.line, v)
		}
		if v := rq.values("Authorization"); len(v) != 1 || v[0] != "Bearer mine" {
			t.Errorf("%s: Authorization = %v, want only the user's value", rq.line, v)
		}
	}
}

func TestCustomHeadersSkippedForPresetAndAccount(t *testing.T) {
	isolateAll(t)
	base, seen := rawUpstream(t)
	hs := map[string]string{"X-Org-Id": "acme"}

	preset := Provider{ID: "openai", Preset: "openai", Name: "OpenAI", Key: "sk-p", Chat: base, Headers: hs}
	sendSigned(t, preset, Chat, base+"/chat/completions")

	account := Provider{ID: "codex", Name: "Codex", Chat: base, Account: &Account{Agent: "codex", User: "me"}, Headers: hs}
	sendSigned(t, account, Chat, base+"/chat/completions")

	got := seen()
	if len(got) != 2 {
		t.Fatalf("want 2 requests, got %d", len(got))
	}
	for _, rq := range got {
		if v := rq.values("X-Org-Id"); len(v) != 0 {
			t.Errorf("%s: custom header leaked: %v", rq.line, v)
		}
	}
}

func TestSaveCleansHeaders(t *testing.T) {
	isolateAll(t)
	err := Save(Provider{
		Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k",
		Headers: map[string]string{
			"  X-A  ":  "  v  ",
			"":         "dropped",
			"   ":      "dropped too",
			"X-ORG-id": "1",
			"X-Org-Id": "2",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	for _, p := range load().Providers {
		if p.ID == "relay" {
			got = p.Headers
		}
	}
	want := map[string]string{"X-A": "v", "X-ORG-id": "1", "X-Org-Id": "2"}
	if len(got) != len(want) {
		t.Fatalf("headers = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("header %q = %q, want %q", k, got[k], v)
		}
	}
}

func TestPresetSaveDropsHeaders(t *testing.T) {
	isolateAll(t)
	if err := Save(Provider{Preset: "openai", Name: "OpenAI", Chat: "https://api.openai.com/v1", Key: "k",
		Headers: map[string]string{"X-Org-Id": "acme"}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range load().Providers {
		if p.ID == "openai" && len(p.Headers) != 0 {
			t.Errorf("preset kept headers: %v", p.Headers)
		}
	}
}

func TestNoHeadersFieldWhenEmpty(t *testing.T) {
	isolateAll(t)
	if err := Save(Provider{Name: "Plain", Chat: "https://plain.example.com/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"headers"`) {
		t.Errorf("empty headers were written: %s", b)
	}
}
