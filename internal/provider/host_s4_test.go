package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestHostS4SignSetsRequestHost checks that a user Host header reaches the
// wire: Go's client ignores Host inside req.Header, so Sign must set req.Host.
func TestHostS4SignSetsRequestHost(t *testing.T) {
	hdrS1Env(t)
	url, got := hdrS1RawGET(t)
	p := Provider{ID: "hosts4-sign", Name: "HostSign", Chat: "http://x/v1", Key: "sk",
		Headers: map[string]string{"host": "tenant.example.com"}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if req.Host != "tenant.example.com" {
		t.Fatalf("req.Host = %q, want the user's value", req.Host)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	head := <-got
	if !strings.Contains(head, "Host: tenant.example.com\r\n") {
		t.Errorf("wire head missing user Host:\n%s", head)
	}
}
