package gateway

import (
	"net/http"
	"net/url"
	"testing"
)

// gwproxyWiringTest checks that the gateway's vendor transport asks
// netproxy for its proxy, and that loopback stays direct.
func TestGwproxyWiringVendorTransportUsesNetproxy(t *testing.T) {
	s := New()
	tr, ok := s.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("vendor transport is %T, want *http.Transport", s.client.Transport)
	}
	if tr.Proxy == nil {
		t.Fatal("vendor transport has no Proxy func")
	}
	for _, raw := range []string{"http://127.0.0.1:3425/v1/messages", "http://localhost:3425/", "http://[::1]:3425/"} {
		req, err := http.NewRequest(http.MethodPost, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		p, err := tr.Proxy(req)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", raw, err)
		}
		if p != nil {
			t.Fatalf("%s: loopback went through proxy %s", raw, p)
		}
	}
	// A nil URL yields no proxy and no error through the same path.
	if p, err := tr.Proxy(&http.Request{URL: &url.URL{}}); err != nil || p != nil {
		t.Fatalf("empty URL: got proxy %v, err %v", p, err)
	}
}
