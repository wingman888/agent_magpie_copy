package provider

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
)

// chdrRawServer answers every request with 200 and hands back the request
// head exactly as it arrived, so header-name casing can be checked.
func chdrRawServer(t *testing.T) (addr string, heads <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan string, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				var head strings.Builder
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					head.WriteString(line)
					if line == "\r\n" {
						break
					}
				}
				ch <- head.String()
				c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}"))
			}(c)
		}
	}()
	return ln.Addr().String(), ch
}

func TestChdrWireKeepsCaseAndOverridesAuth(t *testing.T) {
	addr, heads := chdrRawServer(t)
	p := Provider{
		ID: "chdr-relay", Name: "Chdr Relay", Key: "sk-user",
		Chat: "http://" + addr + "/v1",
		Headers: cleanHeaders(map[string]string{
			"X-ORG-id":      "  acme  ",
			"X-Org-Id":      "other",
			"Authorization": "Bearer custom",
			"   ":           "dropped",
		}),
	}
	r := probe(context.Background(), p, Chat, p.Chat+"/chat/completions", []byte(`{}`), "m")
	if !r.OK {
		t.Fatalf("probe failed: %+v", r)
	}
	head := <-heads
	for _, want := range []string{"X-ORG-id: acme\r\n", "X-Org-Id: other\r\n", "Authorization: Bearer custom\r\n"} {
		if !strings.Contains(head, want) {
			t.Errorf("upstream head lacks %q:\n%s", want, head)
		}
	}
	if strings.Contains(head, "sk-user") {
		t.Errorf("auth header from the key leaked through alongside the override:\n%s", head)
	}
	if strings.Contains(head, "dropped") {
		t.Errorf("entry with an empty name was sent:\n%s", head)
	}
}

func TestChdrPresetIgnoresHeaders(t *testing.T) {
	addr, heads := chdrRawServer(t)
	p := Provider{
		ID: "chdr-preset", Name: "Chdr Preset", Preset: "deepseek", Key: "sk-user",
		Chat:    "http://" + addr + "/v1",
		Headers: map[string]string{"X-ORG-id": "acme"},
	}
	if got := p.CustomHeaders(); got != nil {
		t.Fatalf("preset CustomHeaders = %v, want nil", got)
	}
	probe(context.Background(), p, Chat, p.Chat+"/chat/completions", []byte(`{}`), "m")
	if head := <-heads; strings.Contains(strings.ToLower(head), "x-org-id") {
		t.Errorf("preset sent a custom header:\n%s", head)
	}
}

func TestChdrAccountIgnoresHeaders(t *testing.T) {
	p := Provider{ID: "chdr-acct", Account: &Account{Agent: "claude"}, Headers: map[string]string{"X-A": "1"}}
	if got := p.CustomHeaders(); got != nil {
		t.Fatalf("account CustomHeaders = %v, want nil", got)
	}
}

func TestChdrApplyKeepsSpellingAndWins(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "http://example.invalid/", nil)
	req.Header.Set("Authorization", "Bearer auth")
	req.Header.Set("X-Api-Key", "auth")
	p := Provider{ID: "chdr-apply", Name: "x", Chat: "http://example.invalid/v1",
		Headers: map[string]string{"authorization": "Bearer user", "X-ORG-id": "acme", "X-Org-Id": "other"}}
	p.ApplyCustomHeaders(req)
	if _, ok := req.Header["Authorization"]; ok {
		t.Error("canonical Authorization survived an override under another spelling")
	}
	if got := req.Header["authorization"]; len(got) != 1 || got[0] != "Bearer user" {
		t.Errorf("authorization = %v, want [Bearer user]", got)
	}
	if got := req.Header["X-ORG-id"]; len(got) != 1 || got[0] != "acme" {
		t.Errorf("X-ORG-id = %v, want [acme]", got)
	}
	if got := req.Header["X-Org-Id"]; len(got) != 1 || got[0] != "other" {
		t.Errorf("X-Org-Id = %v, want [other]", got)
	}
	if _, ok := req.Header["X-Api-Key"]; !ok {
		t.Error("unrelated auth header was dropped")
	}
}

func TestChdrSaveTrimsAndKeepsOnlyCustom(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{Name: "Chdr Save", Chat: "https://relay.example/v1", Key: "k",
		Headers: map[string]string{" X-A ": " v ", "": "x", "X-B": ""}}); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	for _, p := range load().Providers {
		if p.ID == "chdr-save" {
			got = p.Headers
		}
	}
	if len(got) != 2 || got["X-A"] != "v" {
		t.Fatalf("saved headers = %v, want {X-A: v, X-B: ''}", got)
	}
	if v, ok := got["X-B"]; !ok || v != "" {
		t.Errorf("empty value should be kept as empty string, got %v", got)
	}
}

func TestChdrPresetSaveDropsHeaders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := FromPreset("deepseek")
	if err != nil {
		t.Skip("no deepseek preset in this build")
	}
	p.Key = "sk-test"
	p.Headers = map[string]string{"X-ORG-id": "acme"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	for _, s := range load().Providers {
		if s.ID == p.ID && len(s.Headers) != 0 {
			t.Errorf("preset kept headers on save: %v", s.Headers)
		}
	}
}
