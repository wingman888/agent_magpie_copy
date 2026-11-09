package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// hdrCLIStdout runs f with stdout captured and returns what it printed.
func hdrCLIStdout(t *testing.T, f func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	ferr := f()
	os.Stdout = old
	w.Close()
	out := <-done
	if ferr != nil {
		t.Fatal(ferr)
	}
	return out
}

func TestHdrCLIApplyPairsHeaders(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{"url=https://relay.example.com/v1", "key=sk-test", "Header.X-Org-Id=acme", "HEADER.x-gateway-token=t1"}); err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://relay.example.com/v1" || p.Key != "sk-test" {
		t.Fatalf("plain pairs not applied: %+v", p)
	}
	if got := p.Headers["X-Org-Id"]; got != "acme" {
		t.Errorf("X-Org-Id = %q, want acme (name casing kept)", got)
	}
	if got := p.Headers["x-gateway-token"]; got != "t1" {
		t.Errorf("x-gateway-token = %q, want t1 (name casing kept)", got)
	}
	if _, ok := p.Headers["X-Gateway-Token"]; ok {
		t.Error("header name casing must not be changed")
	}
}

func TestHdrCLIApplyPairsRejectsBadHeader(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{"header.=acme"}); err == nil {
		t.Error("an empty header name must be rejected")
	}
	if err := applyPairs(&p, []string{"header.Bad Name=x"}); err == nil {
		t.Error("a header name with a space must be rejected")
	}
}

func TestHdrCLIShowProviderListsHeaders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := provider.Provider{ID: "hdr-show", Name: "Show Relay", Chat: "https://relay.example.com/v1", Key: "sk-test-1234567890",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	out := hdrCLIStdout(t, func() error { return showProvider(p) })
	if !strings.Contains(out, "X-Org-Id: acme") {
		t.Fatalf("magpie provider output does not list the header:\n%s", out)
	}
}
