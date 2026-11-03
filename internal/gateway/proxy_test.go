package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// isolateProxyEnv points settings at a temporary directory and clears the
// proxy variables, so only the setting under test decides the proxy.
func isolateProxyEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	return dir
}

// vendorTransport returns the transport the gateway sends vendor requests with.
func vendorTransport(t *testing.T) (*Server, *http.Transport) {
	t.Helper()
	s := New()
	tr, ok := s.client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("gateway client has no *http.Transport")
	}
	return s, tr
}

// The gateway sends a vendor request through the proxy from settings: a
// plain-HTTP request to a non-loopback host reaches the proxy with its
// absolute URL.
func TestGatewayVendorRequestUsesSettingsProxy(t *testing.T) {
	isolateProxyEnv(t)
	seen := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case seen <- r.URL.String():
		default:
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	defer proxy.Close()
	if err := settings.Save(settings.Settings{Proxy: proxy.URL}); err != nil {
		t.Fatal(err)
	}

	s, _ := vendorTransport(t)
	req, _ := http.NewRequest(http.MethodGet, "http://vendor.example.test/v1/models", nil)
	res, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("request through proxy failed: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusTeapot {
		t.Fatalf("status %d, want the proxy's answer", res.StatusCode)
	}
	if got := <-seen; got != "http://vendor.example.test/v1/models" {
		t.Fatalf("proxy saw %q", got)
	}
}

// "direct" in settings means the gateway picks no proxy for vendor hosts.
func TestGatewayVendorRequestDirectSetting(t *testing.T) {
	isolateProxyEnv(t)
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	_, tr := vendorTransport(t)
	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", nil)
	u, err := tr.Proxy(req)
	if err != nil || u != nil {
		t.Fatalf("direct setting: proxy %v, err %v; want no proxy and no error", u, err)
	}
}

// Loopback hosts are never proxied, even with a proxy configured.
func TestGatewayLoopbackNeverProxied(t *testing.T) {
	isolateProxyEnv(t)
	if err := settings.Save(settings.Settings{Proxy: "http://127.0.0.1:9"}); err != nil {
		t.Fatal(err)
	}
	_, tr := vendorTransport(t)
	for _, raw := range []string{"http://127.0.0.1:3425/", "http://localhost:3425/", "http://[::1]:3425/"} {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		u, err := tr.Proxy(req)
		if err != nil || u != nil {
			t.Errorf("%s: proxy %v, err %v; want direct", raw, u, err)
		}
	}
}

// A setting that cannot be used (for example edited by hand) fails the
// request; it must not quietly go direct.
func TestGatewayInvalidProxySettingFailsRequest(t *testing.T) {
	dir := isolateProxyEnv(t)
	if err := os.MkdirAll(filepath.Join(dir, "magpie"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "magpie", "settings.json"), []byte(`{"proxy":"ftp://x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := vendorTransport(t)
	req, _ := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/models", nil)
	res, err := s.client.Do(req)
	if err == nil {
		res.Body.Close()
		t.Fatal("request with an invalid proxy setting succeeded; it must fail")
	}
	if !strings.Contains(err.Error(), "proxy") {
		t.Fatalf("error does not mention the proxy setting: %v", err)
	}
}
