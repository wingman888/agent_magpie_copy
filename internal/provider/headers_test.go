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
	"sync"
	"testing"
)

// rawReq is one request as it arrived, header names unmodified. net/http
// canonicalises names when it reads them, so the case checks need the bytes.
type rawReq struct {
	Path   string
	Header [][2]string
}

// values returns every value sent under a name, matched case-insensitively.
func (r rawReq) values(name string) []string {
	var out []string
	for _, h := range r.Header {
		if strings.EqualFold(h[0], name) {
			out = append(out, h[1])
		}
	}
	return out
}

// has reports whether a header goes out under exactly this spelling.
func (r rawReq) has(name, value string) bool {
	for _, h := range r.Header {
		if h[0] == name && h[1] == value {
			return true
		}
	}
	return false
}

// rawServer answers every request with a model list and records what came in.
func rawServer(t *testing.T) (string, func() []rawReq) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var got []rawReq
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				r := serveRaw(c)
				mu.Lock()
				got = append(got, r)
				mu.Unlock()
			}()
		}
	}()
	return "http://" + ln.Addr().String(), func() []rawReq {
		mu.Lock()
		defer mu.Unlock()
		return append([]rawReq(nil), got...)
	}
}

func serveRaw(c net.Conn) rawReq {
	defer c.Close()
	br := bufio.NewReader(c)
	var r rawReq
	line, err := br.ReadString('\n')
	if err != nil {
		return r
	}
	if f := strings.Fields(line); len(f) >= 2 {
		r.Path = f[1]
	}
	length := 0
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			return r
		}
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			break
		}
		name, value, _ := strings.Cut(l, ":")
		value = strings.TrimSpace(value)
		r.Header = append(r.Header, [2]string{name, value})
		if strings.EqualFold(name, "Content-Length") {
			length, _ = strconv.Atoi(value)
		}
	}
	io.ReadFull(br, make([]byte, length))
	body := `{"data":[{"id":"m1"}]}`
	fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	return r
}

func headerTestEnv(t *testing.T) {
	t.Helper()
	isolate(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestCustomHeadersReachEveryRequest(t *testing.T) {
	headerTestEnv(t)
	base, reqs := rawServer(t)
	p := Provider{ID: "relay", Name: "Relay", Key: "k", Chat: base + "/v1", Headers: map[string]string{
		"X-Org-Id": "acme",
		"X-ORG-id": "other",
	}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	saved, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := saved.Fetch(ctx); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, r := range saved.Test(ctx) {
		if !r.OK {
			t.Fatalf("test %s failed: %s", r.Protocol, r.Error)
		}
	}
	got := reqs()
	if len(got) < 2 {
		t.Fatalf("want a fetch and a probe, upstream saw %d requests", len(got))
	}
	for _, r := range got {
		if !r.has("X-Org-Id", "acme") {
			t.Errorf("%s: X-Org-Id: acme not sent as written: %v", r.Path, r.Header)
		}
		if !r.has("X-ORG-id", "other") {
			t.Errorf("%s: X-ORG-id not kept in its case: %v", r.Path, r.Header)
		}
	}
}

func TestCustomHeaderOverridesAuthHeaderOfSameName(t *testing.T) {
	headerTestEnv(t)
	base, reqs := rawServer(t)
	// Chat sends Authorization: Bearer k; the user's Authorization wins.
	p := Provider{ID: "relay", Name: "Relay", Key: "k", Chat: base + "/v1", Headers: map[string]string{
		"Authorization": "Token mine",
	}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	saved, _ := Find("relay")
	saved.Test(context.Background())
	got := reqs()
	if len(got) == 0 {
		t.Fatal("upstream saw nothing")
	}
	for _, r := range got {
		if v := r.values("Authorization"); len(v) != 1 || v[0] != "Token mine" {
			t.Errorf("%s: Authorization = %v, want only the user's value", r.Path, v)
		}
	}
}

func TestCustomHeaderOverridesAnthropicKeyHeaderInAnyCase(t *testing.T) {
	headerTestEnv(t)
	base, reqs := rawServer(t)
	// the Anthropic protocol sends x-api-key itself; a user x-api-key replaces it
	p := Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: base, Headers: map[string]string{
		"x-api-key": "mine",
	}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	saved, _ := Find("relay")
	saved.Test(context.Background())
	var probe *rawReq
	for _, r := range reqs() {
		if r.Path == "/v1/messages" {
			r := r
			probe = &r
		}
	}
	if probe == nil {
		t.Fatal("no probe reached the upstream")
	}
	if v := probe.values("x-api-key"); len(v) != 1 || v[0] != "mine" {
		t.Errorf("x-api-key = %v, want exactly the user's value", v)
	}
	if !probe.has("x-api-key", "mine") {
		t.Errorf("x-api-key not sent in the user's spelling: %v", probe.Header)
	}
}

func TestPresetAndAccountSendNoCustomHeaders(t *testing.T) {
	headerTestEnv(t)
	base, reqs := rawServer(t)
	// a stored header on a preset provider is ignored by CustomHeaders…
	pre := Provider{ID: "ds", Preset: "deepseek", Key: "k", Chat: base + "/v1", Headers: map[string]string{"X-Org-Id": "acme"}}
	if got := pre.CustomHeaders(); got != nil {
		t.Errorf("preset CustomHeaders = %v, want nil", got)
	}
	// …and Save does not keep them
	if err := Save(pre); err != nil {
		t.Fatal(err)
	}
	saved, _ := Find("ds")
	if len(saved.Headers) != 0 {
		t.Errorf("preset saved with headers %v", saved.Headers)
	}
	saved.Test(context.Background())
	for _, r := range reqs() {
		if len(r.values("X-Org-Id")) != 0 {
			t.Errorf("preset request carried a custom header: %v", r.Header)
		}
	}

	// an account signs requests itself and never gets the custom headers
	acct := Provider{ID: "codex", Headers: map[string]string{"X-Org-Id": "acme"},
		Account: &Account{sign: func(ctx context.Context, req *http.Request, body []byte) error { return nil }}}
	if got := acct.CustomHeaders(); got != nil {
		t.Errorf("account CustomHeaders = %v, want nil", got)
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", nil)
	if err := acct.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if v := req.Header.Values("X-Org-Id"); len(v) != 0 {
		t.Errorf("account request carried X-Org-Id: %v", v)
	}
}

func TestHeaderNamesAndValuesAreTrimmedAndEmptyNamesDropped(t *testing.T) {
	headerTestEnv(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example.com/v1",
		Headers: map[string]string{
			"  X-Pad  ": "  padded  ",
			"":         "dropped",
			"   ":      "dropped too",
			"X-Org-Id": "acme",
			"X-ORG-id": "other",
		}}); err != nil {
		t.Fatal(err)
	}
	saved, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-Pad": "padded", "X-Org-Id": "acme", "X-ORG-id": "other"}
	if len(saved.Headers) != len(want) {
		t.Fatalf("headers = %v, want %v", saved.Headers, want)
	}
	for k, v := range want {
		if saved.Headers[k] != v {
			t.Errorf("headers[%q] = %q, want %q", k, saved.Headers[k], v)
		}
	}
}

func TestNoHeadersFieldWhenEmpty(t *testing.T) {
	headerTestEnv(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example.com/v1",
		Headers: map[string]string{"  ": "dropped"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "magpie", "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string][]map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["providers"][0]["headers"]; ok {
		t.Errorf("empty headers were written: %s", b)
	}
}
