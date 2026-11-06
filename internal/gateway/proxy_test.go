package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// vendorProxy returns the proxy the gateway's vendor transport picks for u.
func vendorProxy(t *testing.T, s *Server, u string) *url.URL {
	t.Helper()
	tr, ok := s.client.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatal("gateway transport has no Proxy function")
	}
	req, err := http.NewRequest(http.MethodPost, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy(%s): %v", u, err)
	}
	return p
}

func isolateProxyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
}

func TestGatewayVendorProxySettingsDirect(t *testing.T) {
	isolateProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if p := vendorProxy(t, s, "https://api.vendor.example/v1/chat"); p != nil {
		t.Fatalf("direct setting: got proxy %v, want none", p)
	}
}

func TestGatewayVendorProxyLoopbackAlwaysDirect(t *testing.T) {
	isolateProxyEnv(t)
	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	if p := vendorProxy(t, s, "http://127.0.0.1:3425/v1/messages"); p != nil {
		t.Fatalf("loopback: got proxy %v, want none", p)
	}
	if p := vendorProxy(t, s, "http://localhost:3425/"); p != nil {
		t.Fatalf("localhost: got proxy %v, want none", p)
	}
}

func TestGatewayVendorProxyFromEnvironment(t *testing.T) {
	isolateProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://proxy.env.example:8080")
	s := New()
	p := vendorProxy(t, s, "https://api.vendor.example/v1/chat")
	if p == nil || p.String() != "http://proxy.env.example:8080" {
		t.Fatalf("environment proxy: got %v", p)
	}
}

// The request really goes through the configured proxy: the proxy receives
// the absolute-form request for the vendor host, without any real network.
func TestGatewayVendorRequestUsesSettingsProxy(t *testing.T) {
	isolateProxyEnv(t)
	var gotHost, gotPath string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotPath = r.URL.Host, r.URL.Path
		io.WriteString(w, "via proxy")
	}))
	defer proxy.Close()

	if err := settings.Save(settings.Settings{Proxy: proxy.URL}); err != nil {
		t.Fatal(err)
	}
	s := New()
	res, err := s.client.Get("http://api.vendor.example/v1/models")
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if string(body) != "via proxy" {
		t.Fatalf("body = %q, want the proxy's reply", body)
	}
	if gotHost != "api.vendor.example" || gotPath != "/v1/models" {
		t.Fatalf("proxy saw host=%q path=%q", gotHost, gotPath)
	}
}
