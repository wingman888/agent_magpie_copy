package netproxy

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// isolate points settings at an empty folder, clears every proxy variable,
// and makes the system proxy whatever sys says for the test's duration.
func isolate(t *testing.T, sys system) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, n := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy",
		"ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(n, "")
	}
	old := readSystem
	readSystem = func() system { return sys }
	t.Cleanup(func() { readSystem = old })
}

func setProxy(t *testing.T, p string) {
	t.Helper()
	if err := settings.Save(settings.Settings{Proxy: p}); err != nil {
		t.Fatal(err)
	}
}

func forURL(t *testing.T, raw string) string {
	t.Helper()
	u, _ := url.Parse(raw)
	p, err := For(u)
	if err != nil {
		t.Fatalf("For(%s): %v", raw, err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

func expect(t *testing.T, raw, want string) {
	t.Helper()
	if got := forURL(t, raw); got != want {
		t.Errorf("For(%s) = %q, want %q", raw, got, want)
	}
}

func TestDirectSetting(t *testing.T) {
	isolate(t, system{HTTPS: "sys.example:8443"})
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	setProxy(t, "direct")
	expect(t, "https://api.example.com/v1", "")
}

func TestSettingBeatsEnvironment(t *testing.T) {
	isolate(t, system{HTTPS: "sys.example:8443"})
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	setProxy(t, "socks5://127.0.0.1:1080")
	expect(t, "https://api.example.com/v1", "socks5://127.0.0.1:1080")
	setProxy(t, "127.0.0.1:7890")
	expect(t, "https://api.example.com/v1", "http://127.0.0.1:7890")
}

func TestEnvironmentBeatsSystem(t *testing.T) {
	isolate(t, system{HTTPS: "sys.example:8443"})
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	expect(t, "https://api.example.com/v1", "http://env.example:3128")
	// an http request does not take the HTTPS variable, so the system proxy applies
	expect(t, "http://api.example.com/v1", "http://sys.example:8443")
	t.Setenv("HTTP_PROXY", "env.example:8080")
	expect(t, "http://api.example.com/v1", "http://env.example:8080")
}

func TestAllProxyFallback(t *testing.T) {
	isolate(t, system{})
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1080")
	expect(t, "http://api.example.com", "socks5://127.0.0.1:1080")
	expect(t, "https://api.example.com", "socks5://127.0.0.1:1080")
}

func TestNoProxyEnvironment(t *testing.T) {
	isolate(t, system{HTTPS: "sys.example:8443"})
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	t.Setenv("NO_PROXY", ".internal.test, 10.0.0.0/8, example.org:8443")
	expect(t, "https://svc.internal.test", "")
	expect(t, "https://10.9.8.7", "")
	expect(t, "https://example.org:8443", "")
	// the port in the exception does not match, so the proxy applies
	expect(t, "https://example.org", "http://env.example:3128")
	t.Setenv("NO_PROXY", "*")
	expect(t, "https://api.example.com", "")
}

func TestNoProxyWildcard(t *testing.T) {
	isolate(t, system{})
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	t.Setenv("NO_PROXY", "*.example.com")
	expect(t, "https://a.example.com", "")
	expect(t, "https://example.com", "")
	expect(t, "https://example.org", "http://env.example:3128")
}

func TestSystemFallback(t *testing.T) {
	isolate(t, system{HTTPS: "sys.example:8443"})
	expect(t, "https://api.example.com", "http://sys.example:8443")
}

func TestSystemPriority(t *testing.T) {
	isolate(t, system{HTTP: "h.example:1", SOCKS: "s.example:2"})
	expect(t, "https://api.example.com", "http://h.example:1")
	isolate(t, system{SOCKS: "s.example:2"})
	expect(t, "https://api.example.com", "socks5://s.example:2")
	isolate(t, system{HTTPS: "x.example:3", HTTP: "h.example:1", SOCKS: "s.example:2"})
	expect(t, "https://api.example.com", "http://x.example:3")
}

func TestSystemExceptions(t *testing.T) {
	isolate(t, system{HTTPS: "sys.example:8443",
		Exceptions: []string{"*.corp.example", "10.*", "intra.example:443"}})
	expect(t, "https://a.corp.example", "")
	expect(t, "https://corp.example", "")
	expect(t, "https://10.1.2.3", "")
	expect(t, "https://intra.example", "")
	expect(t, "https://public.example", "http://sys.example:8443")
}

func TestSystemSimpleHostnames(t *testing.T) {
	isolate(t, system{HTTPS: "sys.example:8443", SimpleLocal: true})
	expect(t, "https://intranet", "")
	expect(t, "https://api.example.com", "http://sys.example:8443")
}

func TestLoopbackNeverProxied(t *testing.T) {
	isolate(t, system{HTTPS: "sys.example:8443"})
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	setProxy(t, "http://127.0.0.1:7890")
	for _, u := range []string{"http://localhost:3425", "http://127.0.0.1:3425",
		"http://127.1.2.3/", "http://[::1]:3425", "http://dev.localhost/"} {
		expect(t, u, "")
	}
	expect(t, "https://api.example.com", "http://127.0.0.1:7890")
}

func TestProxyHook(t *testing.T) {
	isolate(t, system{})
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	req, _ := http.NewRequest("GET", "https://api.example.com/x", nil)
	p, err := Proxy(req)
	if err != nil || p == nil || p.Host != "env.example:3128" {
		t.Fatalf("Proxy: %v %v", p, err)
	}
	if Transport().Proxy == nil {
		t.Fatal("transport without proxy hook")
	}
}

func TestInvalidStoredProxy(t *testing.T) {
	isolate(t, system{})
	dir := filepath.Dir(settings.Path())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.Path(), []byte(`{"proxy":"ftp://x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://api.example.com")
	if _, err := For(u); err == nil {
		t.Fatal("ftp proxy in the file was used")
	}
}
