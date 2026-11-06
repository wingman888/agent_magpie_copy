package provider

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestHeadersTrimmedAndEmptyDropped(t *testing.T) {
	isolateConfig(t)
	err := Save(Provider{Name: "Org Relay", Chat: "https://relay.example/v1", Key: "sk-test",
		Headers: map[string]string{" X-Org-Id ": " acme ", "": "dropped", "  ": "dropped", "X-Bad Name": "dropped"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find("org-relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 1 || p.Headers["X-Org-Id"] != "acme" {
		t.Fatalf("headers: %v", p.Headers)
	}
}

func TestEmptyHeadersNotWritten(t *testing.T) {
	isolateConfig(t)
	if err := Save(Provider{Name: "Plain", Chat: "https://plain.example/v1", Key: "sk-test",
		Headers: map[string]string{"": "x"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "headers") {
		t.Fatalf("an empty header map was written:\n%s", b)
	}
}

func TestHeadersOverrideAuthAndKeepCase(t *testing.T) {
	p := Provider{Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-1",
		Headers: map[string]string{"X-Org-Id": "acme", "authorization": "Bearer custom"}}
	req, err := http.NewRequest(http.MethodPost, p.Chat+"/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header["X-Org-Id"]; len(got) != 1 || got[0] != "acme" {
		t.Fatalf("X-Org-Id: %v", req.Header)
	}
	if got := req.Header["authorization"]; len(got) != 1 || got[0] != "Bearer custom" {
		t.Fatalf("authorization not overridden: %v", req.Header)
	}
	if _, ok := req.Header["Authorization"]; ok {
		t.Fatalf("the default Authorization survived: %v", req.Header)
	}
}

func TestPresetIgnoresHeaders(t *testing.T) {
	p := Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Chat: "https://api.deepseek.com/v1", Key: "sk-1",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	req, _ := http.NewRequest(http.MethodPost, p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Header["X-Org-Id"]; ok {
		t.Fatal("a preset sent a custom header")
	}
}

// The header name must reach the wire exactly as typed; net/http would
// otherwise canonicalize it.
func TestHeaderNameOnTheWire(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- ""
			return
		}
		defer c.Close()
		var buf []byte
		tmp := make([]byte, 1024)
		for !bytes.Contains(buf, []byte("\r\n\r\n")) {
			n, err := c.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if err != nil {
				break
			}
		}
		got <- string(buf)
		c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}"))
	}()
	p := Provider{Name: "Relay", Chat: "http://" + ln.Addr().String() + "/v1", Key: "sk-1",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	req, _ := http.NewRequest(http.MethodPost, p.Chat+"/chat/completions", strings.NewReader("{}"))
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	raw := <-got
	if !strings.Contains(raw, "\r\nX-Org-Id: acme\r\n") {
		t.Fatalf("header not sent as typed:\n%s", raw)
	}
}

func TestFallbackCleanedAndKeptForAccounts(t *testing.T) {
	isolateConfig(t)
	if err := Save(Provider{Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-test",
		Fallback: []string{" openai/gpt-5.5 ", "", "openai/gpt-5.5", "deepseek/deepseek-chat"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Fallback, "|") != "openai/gpt-5.5|deepseek/deepseek-chat" {
		t.Fatalf("fallback: %q", p.Fallback)
	}
}
