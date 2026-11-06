package gateway

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// The gateway's vendor client must pick proxies through netproxy.For: a
// proxy saved in settings applies to the very next request, and loopback
// targets stay direct.
func TestVendorTransportUsesNetproxy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, n := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(n, "")
	}
	s := New()
	tr, ok := s.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", s.client.Transport)
	}
	if tr.Proxy == nil {
		t.Fatal("Proxy is nil")
	}
	req := func(raw string) *http.Request {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Request{URL: u}
	}

	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	p, err := tr.Proxy(req("https://api.example.com/v1/messages"))
	if err != nil || p == nil || p.String() != "socks5://127.0.0.1:1080" {
		t.Errorf("vendor request: proxy %v err %v, want socks5://127.0.0.1:1080", p, err)
	}

	for _, raw := range []string{"http://127.0.0.1:3425/", "http://localhost:3425/", "http://[::1]:3425/"} {
		p, err := tr.Proxy(req(raw))
		if err != nil || p != nil {
			t.Errorf("%s: proxy %v err %v, want direct", raw, p, err)
		}
	}
}
