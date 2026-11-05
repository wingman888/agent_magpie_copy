package netproxy

import (
	"net/url"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// clearEnv blanks the proxy variables for the test and restores them after.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, n := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(n, "")
	}
}

func stubs(t *testing.T, setting string, sys *sysConfig) {
	t.Helper()
	oldS, oldSys := settingsProxy, systemConfig
	settingsProxy = func() string { return setting }
	systemConfig = func() (*sysConfig, error) { return sys, nil }
	t.Cleanup(func() { settingsProxy, systemConfig = oldS, oldSys })
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestLoopbackNeverProxied(t *testing.T) {
	clearEnv(t)
	stubs(t, "http://10.0.0.1:8080", &sysConfig{HTTP: mustURL(t, "http://10.0.0.2:8080")})
	t.Setenv("HTTPS_PROXY", "http://10.0.0.3:8080")
	for _, u := range []string{"http://127.0.0.1:3425/", "http://localhost:1/", "http://[::1]:9/", "http://127.9.9.9/"} {
		p, err := For(mustURL(t, u))
		if err != nil || p != nil {
			t.Fatalf("%s: got %v %v, want direct", u, p, err)
		}
	}
}

func TestSettingsWinsOverEnvAndSystem(t *testing.T) {
	clearEnv(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:1")
	stubs(t, "socks5://127.0.0.1:1080", &sysConfig{HTTPS: mustURL(t, "http://sys.example:2")})
	p, err := For(mustURL(t, "https://api.example.com/v1"))
	if err != nil || p == nil || p.String() != "socks5://127.0.0.1:1080" {
		t.Fatalf("got %v %v", p, err)
	}
	stubs(t, "direct", &sysConfig{HTTPS: mustURL(t, "http://sys.example:2")})
	if p, err := For(mustURL(t, "https://api.example.com")); err != nil || p != nil {
		t.Fatalf("direct: got %v %v", p, err)
	}
}

func TestEnvWinsOverSystem(t *testing.T) {
	clearEnv(t)
	stubs(t, "", &sysConfig{HTTPS: mustURL(t, "http://sys.example:2")})
	t.Setenv("HTTPS_PROXY", "http://env.example:1")
	p, err := For(mustURL(t, "https://api.example.com"))
	if err != nil || p == nil || p.Host != "env.example:1" {
		t.Fatalf("got %v %v", p, err)
	}
	// http requests do not see HTTPS_PROXY; they fall through to the system
	p, err = For(mustURL(t, "http://api.example.com"))
	if err != nil || p == nil || p.Host != "sys.example:2" {
		t.Fatalf("http: got %v %v", p, err)
	}
}

func TestNoProxyList(t *testing.T) {
	clearEnv(t)
	stubs(t, "", nil)
	t.Setenv("HTTPS_PROXY", "http://env.example:1")
	t.Setenv("NO_PROXY", "example.com, .internal.net")
	for _, h := range []string{"https://example.com", "https://api.example.com", "https://a.internal.net"} {
		if p, err := For(mustURL(t, h)); err != nil || p != nil {
			t.Fatalf("%s should be direct, got %v %v", h, p, err)
		}
	}
	if p, err := For(mustURL(t, "https://other.org")); err != nil || p == nil {
		t.Fatalf("other.org should use proxy, got %v %v", p, err)
	}
	t.Setenv("NO_PROXY", "*")
	if p, _ := For(mustURL(t, "https://other.org")); p != nil {
		t.Fatal("NO_PROXY=* should be direct")
	}
}

func TestSystemExceptions(t *testing.T) {
	clearEnv(t)
	sys := &sysConfig{
		HTTPS:           mustURL(t, "http://sys.example:2"),
		Exceptions:      []string{"*.corp.example", "169.254/16", "192.168.0.*"},
		SimpleHostnames: true,
	}
	stubs(t, "", sys)
	for _, h := range []string{"https://a.corp.example", "https://intranet", "https://169.254.1.1", "https://192.168.0.7"} {
		if p, _ := For(mustURL(t, h)); p != nil {
			t.Fatalf("%s should be direct, got %v", h, p)
		}
	}
	if p, _ := For(mustURL(t, "https://api.example.com")); p == nil || p.Host != "sys.example:2" {
		t.Fatalf("api.example.com: got %v", p)
	}
}

func TestSystemPreferenceOrder(t *testing.T) {
	clearEnv(t)
	stubs(t, "", &sysConfig{HTTP: mustURL(t, "http://h:1"), SOCKS: mustURL(t, "socks5://s:3")})
	if p, _ := For(mustURL(t, "https://x.example")); p == nil || p.Host != "h:1" {
		t.Fatalf("HTTP before SOCKS: got %v", p)
	}
	stubs(t, "", &sysConfig{SOCKS: mustURL(t, "socks5://s:3")})
	if p, _ := For(mustURL(t, "https://x.example")); p == nil || p.Scheme != "socks5" {
		t.Fatalf("SOCKS last resort: got %v", p)
	}
}

func TestCIDRShortForm(t *testing.T) {
	if got := cidrOf("169.254/16"); got != "169.254.0.0/16" {
		t.Fatal(got)
	}
}
