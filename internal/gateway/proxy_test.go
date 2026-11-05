package gateway

import (
	"net/http"
	"testing"
)

// The vendor transport must consult netproxy: loopback URLs go direct even
// when an environment proxy is configured, and vendor URLs use it.
func TestVendorTransportProxy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	s := New()
	tr, ok := s.client.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatalf("vendor transport has no Proxy func: %#v", s.client.Transport)
	}

	loop, _ := http.NewRequest("POST", "http://127.0.0.1:3425/v1/messages", nil)
	if u, err := tr.Proxy(loop); u != nil || err != nil {
		t.Errorf("loopback should be direct, got %v, %v", u, err)
	}
	local, _ := http.NewRequest("POST", "http://localhost:3425/v1/messages", nil)
	if u, err := tr.Proxy(local); u != nil || err != nil {
		t.Errorf("localhost should be direct, got %v, %v", u, err)
	}

	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")
	vendor, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/x", nil)
	u, err := tr.Proxy(vendor)
	if err != nil || u == nil || u.String() != "http://127.0.0.1:7890" {
		t.Errorf("vendor should use env proxy, got %v, %v", u, err)
	}
}
