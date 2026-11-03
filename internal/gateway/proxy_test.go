package gateway

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// vendorTransport returns the transport the gateway uses for vendor requests.
func vendorTransport(t *testing.T) *http.Transport {
	t.Helper()
	tr, ok := New().client.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatalf("gateway client transport is not *http.Transport")
	}
	if tr.Proxy == nil {
		t.Fatalf("gateway transport has no Proxy function")
	}
	return tr
}

func TestVendorTransportUsesNetproxy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(k, "")
	}
	tr := vendorTransport(t)

	vendor, _ := url.Parse("https://chatgpt.com/backend-api/v1")
	loopback, _ := url.Parse("http://127.0.0.1:3425/")

	// Settings "direct": vendor requests connect directly.
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if p, err := tr.Proxy(&http.Request{URL: vendor}); err != nil || p != nil {
		t.Fatalf("direct: got proxy %v, err %v; want nil", p, err)
	}

	// Settings with a proxy: vendor requests go through it, applied without restart.
	if err := settings.Save(settings.Settings{Proxy: "http://127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if p, err := tr.Proxy(&http.Request{URL: vendor}); err != nil || p == nil || p.String() != "http://127.0.0.1:7890" {
		t.Fatalf("explicit proxy: got %v, err %v; want http://127.0.0.1:7890", p, err)
	}

	// Loopback is always direct, even with a proxy configured.
	if p, err := tr.Proxy(&http.Request{URL: loopback}); err != nil || p != nil {
		t.Fatalf("loopback: got proxy %v, err %v; want nil", p, err)
	}
}
