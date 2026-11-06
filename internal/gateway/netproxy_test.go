package gateway

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
)

// The gateway's vendor client must pick proxies with netproxy.For, not with
// the environment alone.
func TestVendorTransportUsesNetproxy(t *testing.T) {
	s := New()
	tr, ok := s.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", s.client.Transport)
	}
	if tr.Proxy == nil {
		t.Fatal("Proxy is nil")
	}
	if reflect.ValueOf(tr.Proxy).Pointer() != reflect.ValueOf(netproxy.For).Pointer() {
		t.Fatal("Proxy is not netproxy.For")
	}
}

// Loopback targets always connect directly, whatever the proxy settings are.
func TestVendorTransportLoopbackDirect(t *testing.T) {
	s := New()
	tr := s.client.Transport.(*http.Transport)
	for _, raw := range []string{"http://127.0.0.1:3425/", "http://localhost:3425/", "http://[::1]:3425/"} {
		u, _ := url.Parse(raw)
		req := &http.Request{URL: u}
		p, err := tr.Proxy(req)
		if err != nil || p != nil {
			t.Errorf("%s: proxy %v err %v, want direct", raw, p, err)
		}
	}
}
