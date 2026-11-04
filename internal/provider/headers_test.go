package provider

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// f04RawUpstream answers like a tiny HTTP/1.1 server but keeps every header
// name exactly as it arrived; net/http would canonicalise them.
type f04RawUpstream struct {
	mu   sync.Mutex
	reqs []f04RawReq
}

type f04RawReq struct {
	path    string
	headers [][2]string // name as sent, value
}

func f04NewRawUpstream(t *testing.T) (*f04RawUpstream, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	u := &f04RawUpstream{}
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

func (u *f04RawUpstream) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	var rq f04RawReq
	if parts := strings.Fields(line); len(parts) > 1 {
		rq.path = parts[1]
	}
	length := 0
	for {
		l, err := r.ReadString('\n')
		if err != nil {
			return
		}
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			break
		}
		name, value, _ := strings.Cut(l, ":")
		value = strings.TrimSpace(value)
		rq.headers = append(rq.headers, [2]string{name, value})
		if strings.EqualFold(name, "Content-Length") {
			length, _ = strconv.Atoi(value)
		}
	}
	io.CopyN(io.Discard, r, int64(length))
	u.mu.Lock()
	u.reqs = append(u.reqs, rq)
	u.mu.Unlock()
	body := `{"data":[{"id":"m1"}]}`
	fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

func (u *f04RawUpstream) all() []f04RawReq {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]f04RawReq(nil), u.reqs...)
}

// exact reports whether the request carried name with value, name matched
// byte for byte.
func (r f04RawReq) exact(name, value string) bool {
	for _, h := range r.headers {
		if h[0] == name && h[1] == value {
			return true
		}
	}
	return false
}

// fold lists the values of every header whose name matches ignoring case.
func (r f04RawReq) fold(name string) []string {
	var out []string
	for _, h := range r.headers {
		if strings.EqualFold(h[0], name) {
			out = append(out, h[1])
		}
	}
	return out
}

func f04IsolateHeaderTest(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestF04CustomHeadersSentOnFetchAndTest(t *testing.T) {
	f04IsolateHeaderTest(t)
	up, base := f04NewRawUpstream(t)
	p := Provider{ID: "relay", Name: "Relay", Key: "k", Chat: base + "/v1", Models: []string{"m1"},
		Headers: map[string]string{"X-ORG-id": "acme", "Authorization": "Custom auth"}}
	ctx := context.Background()

	if _, err := p.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	res := p.Test(ctx)
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("test probe failed: %+v", res)
	}

	reqs := up.all()
	if len(reqs) < 2 {
		t.Fatalf("want a model-list request and a test request, got %d", len(reqs))
	}
	var sawList, sawTest bool
	for _, r := range reqs {
		if !r.exact("X-ORG-id", "acme") {
			t.Errorf("%s: X-ORG-id: acme not sent exactly; headers %v", r.path, r.headers)
		}
		if got := r.fold("Authorization"); len(got) != 1 || got[0] != "Custom auth" {
			t.Errorf("%s: Authorization = %v, want only the user's value", r.path, got)
		}
		switch r.path {
		case "/v1/models":
			sawList = true
		case "/v1/chat/completions":
			sawTest = true
		}
	}
	if !sawList || !sawTest {
		t.Errorf("list request seen %v, test request seen %v", sawList, sawTest)
	}
}

func TestF04CustomHeaderOverridesAuthCaseInsensitively(t *testing.T) {
	f04IsolateHeaderTest(t)
	up, base := f04NewRawUpstream(t)
	p := Provider{ID: "relay", Name: "Relay", Key: "k", Chat: base + "/v1", Models: []string{"m1"},
		Headers: map[string]string{"authorization": "Token zzz"}}
	p.Test(context.Background())
	for _, r := range up.all() {
		if got := r.fold("Authorization"); len(got) != 1 || got[0] != "Token zzz" {
			t.Errorf("Authorization = %v, want [Token zzz]", got)
		}
		if !r.exact("authorization", "Token zzz") {
			t.Errorf("name not kept as typed: %v", r.headers)
		}
	}
}

func TestF04PresetAndAccountSendNoCustomHeaders(t *testing.T) {
	f04IsolateHeaderTest(t)
	presetID := Presets()[0].ID
	up, base := f04NewRawUpstream(t)
	ctx := context.Background()

	preset := Provider{ID: "vendor", Name: "Vendor", Key: "k", Preset: presetID, Chat: base + "/v1",
		Models: []string{"m1"}, Headers: map[string]string{"X-ORG-id": "acme"}}
	preset.Test(ctx)

	acct := Provider{ID: "codex", Name: "Codex", Key: "k", Chat: base + "/v1",
		Account: &Account{Agent: "codex"}, Headers: map[string]string{"X-ORG-id": "acme"}}
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", nil)
	if err := acct.Sign(ctx, req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Values("X-ORG-id"); len(got) != 0 {
		t.Errorf("account request carried custom header: %v", got)
	}
	if got := req.Header.Values("X-Org-Id"); len(got) != 0 {
		t.Errorf("account request carried custom header: %v", got)
	}

	reqs := up.all()
	if len(reqs) == 0 {
		t.Fatal("preset test sent nothing")
	}
	for _, r := range reqs {
		if got := r.fold("X-ORG-id"); len(got) != 0 {
			t.Errorf("preset request carried custom header: %v", got)
		}
	}
}

func TestF04SaveCleansHeaders(t *testing.T) {
	f04IsolateHeaderTest(t)
	_, base := f04NewRawUpstream(t)
	err := Save(Provider{ID: "relay", Name: "Relay", Key: "k", Chat: base + "/v1",
		Headers: map[string]string{"  X-A  ": "  v  ", "   ": "dropped", "": "dropped too"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Headers) != 1 || got.Headers["X-A"] != "v" {
		t.Errorf("stored headers = %v, want map[X-A:v]", got.Headers)
	}

	presetID := Presets()[0].ID
	if err := Save(Provider{ID: "vendor", Name: "Vendor", Key: "k", Preset: presetID, Chat: base + "/v1",
		Headers: map[string]string{"X-A": "v"}}); err != nil {
		t.Fatal(err)
	}
	got, err = Find("vendor")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Headers) != 0 {
		t.Errorf("preset kept headers: %v", got.Headers)
	}
}
