package provider

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hdrS1Env points config, cache and home at a fresh temp dir.
func hdrS1Env(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)
}

// hdrS1RawGET serves one connection and returns the raw request head it
// received, so the test sees header names exactly as they went on the wire.
func hdrS1RawGET(t *testing.T) (string, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- ""
			return
		}
		defer conn.Close()
		var head strings.Builder
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			head.WriteString(line)
			if err != nil || line == "\r\n" {
				break
			}
		}
		io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
		got <- head.String()
	}()
	return "http://" + ln.Addr().String() + "/v1/models", got
}

func TestHdrS1SaveTrimsAndDropsEmptyNames(t *testing.T) {
	hdrS1Env(t)
	err := Save(Provider{ID: "hdrs1-relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk",
		Headers: map[string]string{
			"  X-Org-Id ": "  acme  ",
			"":            "dropped",
			"   ":         "dropped too",
			"X-ORG-id":    "b",
		}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Find("hdrs1-relay")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-Org-Id": "acme", "X-ORG-id": "b"}
	if len(got.Headers) != len(want) {
		t.Fatalf("headers = %v, want %v", got.Headers, want)
	}
	for k, v := range want {
		if got.Headers[k] != v {
			t.Errorf("header %q = %q, want %q", k, got.Headers[k], v)
		}
	}
}

func TestHdrS1PresetSaveKeepsNoHeaders(t *testing.T) {
	hdrS1Env(t)
	err := Save(Provider{ID: "hdrs1-preset", Name: "Preset", Preset: "deepseek",
		Chat: "https://api.deepseek.com/v1", Key: "sk", Headers: map[string]string{"X-Org-Id": "acme"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Find("hdrs1-preset")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Headers) != 0 {
		t.Fatalf("preset kept headers: %v", got.Headers)
	}
}

func TestHdrS1SignWireCaseAndOverride(t *testing.T) {
	url, got := hdrS1RawGET(t)
	p := Provider{ID: "hdrs1-wire", Name: "Wire", Chat: "http://x/v1", Key: "sk-real",
		Headers: map[string]string{
			"X-ORG-id":      "a",
			"Authorization": "Bearer user-token",
			"X-Org-Id":      "b",
		}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if v := req.Header["Authorization"]; len(v) != 1 || v[0] != "Bearer user-token" {
		t.Fatalf("Authorization = %v, want the user's value only", v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	head := <-got
	for _, want := range []string{"X-ORG-id: a\r\n", "X-Org-Id: b\r\n", "Authorization: Bearer user-token\r\n"} {
		if !strings.Contains(head, want) {
			t.Errorf("wire head missing %q:\n%s", want, head)
		}
	}
	if strings.Contains(head, "Bearer sk-real") {
		t.Errorf("auth header from the key survived the override:\n%s", head)
	}
}

func TestHdrS1PresetAndAccountSendNoCustomHeaders(t *testing.T) {
	ctx := context.Background()
	cases := map[string]Provider{
		"preset":  {Preset: "deepseek", Chat: "http://x/v1", Key: "sk"},
		"account": {Account: &Account{Agent: "codex", User: "u"}, Chat: "http://x/v1", Key: "sk"},
	}
	for name, p := range cases {
		p.Headers = map[string]string{"X-Org-Id": "acme"}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://x/v1/chat/completions", nil)
		if err := p.Sign(ctx, req, Chat, nil); err != nil {
			t.Fatal(err)
		}
		if _, ok := req.Header["X-Org-Id"]; ok {
			t.Errorf("%s: custom header was sent", name)
		}
	}
}

func TestHdrS1ModelListCarriesHeaders(t *testing.T) {
	hdrS1Env(t)
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("X-Org-Id")+"|"+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"m1"}]}`)
	}))
	defer srv.Close()
	p := Provider{ID: "hdrs1-models", Name: "Models", Chat: srv.URL + "/v1", Key: "sk-real",
		Headers: map[string]string{"X-Org-Id": "acme", "Authorization": "Bearer user"}}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("models = %v", ms)
	}
	if len(seen) == 0 {
		t.Fatal("no model list request")
	}
	for _, s := range seen {
		if s != "acme|Bearer user" {
			t.Errorf("model list request got %q, want %q", s, "acme|Bearer user")
		}
	}
}
