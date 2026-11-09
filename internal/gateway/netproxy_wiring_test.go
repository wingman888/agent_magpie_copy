package gateway

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// netproxyWiringClearEnv isolates the proxy tests from the developer's machine.
func netproxyWiringClearEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY",
		"https_proxy", "http_proxy", "all_proxy", "no_proxy"} {
		t.Setenv(k, "")
	}
}

// netproxyWiringWriteSettings writes the settings file directly, so a value
// that Save would refuse can still be placed there, as a hand edit could.
func netproxyWiringWriteSettings(t *testing.T, proxy string) {
	t.Helper()
	p := settings.Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"proxy":"` + proxy + `"}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func netproxyWiringProxyFunc(t *testing.T) func(*http.Request) (*url.URL, error) {
	t.Helper()
	tr, ok := New().client.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatal("gateway upstream transport has no Proxy func")
	}
	return tr.Proxy
}

func TestGatewayNetproxyWiringUsesFor(t *testing.T) {
	netproxyWiringClearEnv(t)
	netproxyWiringWriteSettings(t, "http://127.0.0.1:7890")
	proxy := netproxyWiringProxyFunc(t)

	vendor, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/x", nil)
	got, err := proxy(vendor)
	if err != nil {
		t.Fatalf("vendor request: unexpected error %v", err)
	}
	if got == nil || got.String() != "http://127.0.0.1:7890" {
		t.Fatalf("vendor request proxy = %v, want http://127.0.0.1:7890", got)
	}

	local, _ := http.NewRequest("GET", "http://127.0.0.1:3425/", nil)
	got, err = proxy(local)
	if err != nil || got != nil {
		t.Fatalf("loopback request should be direct, got %v, %v", got, err)
	}
}

func TestGatewayNetproxyWiringInvalidSettingFails(t *testing.T) {
	netproxyWiringClearEnv(t)
	netproxyWiringWriteSettings(t, "ftp://x")
	proxy := netproxyWiringProxyFunc(t)

	vendor, _ := http.NewRequest("POST", "https://chatgpt.com/backend-api/x", nil)
	got, err := proxy(vendor)
	if err == nil {
		t.Fatalf("invalid settings proxy must fail the request, got proxy %v and no error", got)
	}
	if got != nil {
		t.Fatalf("invalid settings proxy must not fall back to direct, got %v", got)
	}
}
