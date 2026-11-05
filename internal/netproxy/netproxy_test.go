package netproxy

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

var proxyEnvNames = []string{
	"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy",
	"ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy",
}

// isolate clears every proxy variable, points the settings file at a fresh
// directory, and replaces the system reader with sys. Everything is restored
// when the test ends.
func isolate(t *testing.T, sys systemConfig) {
	t.Helper()
	for _, n := range proxyEnvNames {
		t.Setenv(n, "")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := systemProxyFunc
	systemProxyFunc = func() systemConfig { return sys }
	resetSystemCache()
	t.Cleanup(func() {
		systemProxyFunc = old
		resetSystemCache()
	})
}

func setSetting(t *testing.T, proxy string) {
	t.Helper()
	if err := settings.Save(settings.Settings{Proxy: proxy}); err != nil {
		t.Fatal(err)
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func forString(t *testing.T, target string) string {
	t.Helper()
	p, err := For(mustURL(t, target))
	if err != nil {
		t.Fatalf("For(%s): %v", target, err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

var sysHTTP = systemConfig{proxy: "http://127.0.0.1:7890", bypass: []string{"*.corp"}}

func TestLoopbackAlwaysDirect(t *testing.T) {
	isolate(t, sysHTTP)
	t.Setenv("HTTPS_PROXY", "http://env:1")
	setSetting(t, "socks5://127.0.0.1:1080")
	for _, target := range []string{
		"http://127.0.0.1:3425/x", "http://localhost/", "https://api.localhost/",
		"http://[::1]:8080/", "http://127.9.8.7/",
	} {
		if got := forString(t, target); got != "" {
			t.Errorf("For(%s) = %q, want direct", target, got)
		}
	}
}

func TestSettingsWinsOverEverything(t *testing.T) {
	isolate(t, sysHTTP)
	t.Setenv("HTTPS_PROXY", "http://env:1")
	setSetting(t, "socks5://127.0.0.1:1080")
	if got := forString(t, "https://api.openai.com/v1"); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("For = %q", got)
	}
	if p, src := Describe(); p != "socks5://127.0.0.1:1080" || src != "settings" {
		t.Fatalf("Describe = %q, %q", p, src)
	}
}

func TestBareHostPortIsHTTP(t *testing.T) {
	isolate(t, systemConfig{})
	setSetting(t, "127.0.0.1:7890")
	if got := forString(t, "https://chatgpt.com/"); got != "http://127.0.0.1:7890" {
		t.Fatalf("For = %q", got)
	}
}

func TestDirectSetting(t *testing.T) {
	isolate(t, sysHTTP)
	t.Setenv("HTTPS_PROXY", "http://env:1")
	setSetting(t, "direct")
	if got := forString(t, "https://chatgpt.com/"); got != "" {
		t.Fatalf("For = %q, want direct", got)
	}
	if p, src := Describe(); p != "" || src != "off" {
		t.Fatalf("Describe = %q, %q; want \"\", off", p, src)
	}
}

func TestInvalidStoredSettingIsError(t *testing.T) {
	isolate(t, sysHTTP)
	dir := filepath.Dir(settings.Path())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.Path(), []byte(`{"proxy":"ftp://x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := For(mustURL(t, "https://chatgpt.com/")); err == nil {
		t.Fatal("For accepted an invalid stored proxy; want error, not a direct connection")
	}
}

func TestEnvironmentBeforeSystem(t *testing.T) {
	isolate(t, sysHTTP)
	t.Setenv("HTTPS_PROXY", "http://env:1")
	if got := forString(t, "https://chatgpt.com/"); got != "http://env:1" {
		t.Fatalf("For = %q, want env proxy", got)
	}
	if p, src := Describe(); p != "http://env:1" || src != "environment" {
		t.Fatalf("Describe = %q, %q", p, src)
	}
}

func TestEnvironmentNoProxyDirect(t *testing.T) {
	isolate(t, sysHTTP)
	t.Setenv("HTTPS_PROXY", "http://env:1")
	t.Setenv("NO_PROXY", ".corp.example, 10.0.0.0/8")
	if got := forString(t, "https://git.corp.example/x"); got != "" {
		t.Errorf("NO_PROXY domain: For = %q, want direct", got)
	}
	if got := forString(t, "https://10.1.2.3/x"); got != "" {
		t.Errorf("NO_PROXY CIDR: For = %q, want direct", got)
	}
	if got := forString(t, "https://chatgpt.com/"); got != "http://env:1" {
		t.Errorf("non-matching host: For = %q", got)
	}
}

func TestEnvironmentSchemeSelection(t *testing.T) {
	isolate(t, systemConfig{})
	t.Setenv("HTTP_PROXY", "http://plain:1")
	t.Setenv("ALL_PROXY", "socks5://all:2")
	if got := forString(t, "http://example.com/"); got != "http://plain:1" {
		t.Errorf("http: For = %q", got)
	}
	if got := forString(t, "https://example.com/"); got != "socks5://all:2" {
		t.Errorf("https falls back to ALL_PROXY: For = %q", got)
	}
}

func TestSystemProxyAndBypass(t *testing.T) {
	isolate(t, sysHTTP)
	if got := forString(t, "https://chatgpt.com/..."); got != "http://127.0.0.1:7890" {
		t.Fatalf("For = %q", got)
	}
	if got := forString(t, "https://git.corp/x"); got != "" {
		t.Fatalf("bypassed host: For = %q, want direct", got)
	}
	if p, src := Describe(); p != "http://127.0.0.1:7890" || src != "system" {
		t.Fatalf("Describe = %q, %q", p, src)
	}
}

func TestNoneWhenNothingConfigured(t *testing.T) {
	isolate(t, systemConfig{})
	if got := forString(t, "https://chatgpt.com/"); got != "" {
		t.Fatalf("For = %q, want direct", got)
	}
	if p, src := Describe(); p != "" || src != "none" {
		t.Fatalf("Describe = %q, %q; want \"\", none", p, src)
	}
}

func TestSystemCacheIsRefreshed(t *testing.T) {
	isolate(t, systemConfig{})
	if got := forString(t, "https://chatgpt.com/"); got != "" {
		t.Fatalf("first: %q", got)
	}
	systemProxyFunc = func() systemConfig { return sysHTTP }
	// Still within the TTL, so the old reading is used.
	if got := forString(t, "https://chatgpt.com/"); got != "" {
		t.Fatalf("within TTL: %q", got)
	}
	resetSystemCache()
	if got := forString(t, "https://chatgpt.com/"); got != "http://127.0.0.1:7890" {
		t.Fatalf("after refresh: %q", got)
	}
}

func TestNoProxyMatches(t *testing.T) {
	cases := []struct {
		list, host, port string
		want             bool
	}{
		{"", "a.com", "443", false},
		{"*", "a.com", "443", true},
		{"a.com", "a.com", "443", true},
		{"a.com", "x.a.com", "443", true},
		{"a.com", "xa.com", "443", false},
		{".a.com", "a.com", "443", true},
		{"*.a.com", "x.a.com", "443", true},
		{"a.com:8080", "a.com", "443", false},
		{"a.com:8080", "a.com", "8080", true},
		{"10.0.0.0/8", "10.9.9.9", "443", true},
		{"10.0.0.0/8", "11.0.0.1", "443", false},
		{"::1", "::1", "443", true},
	}
	for _, c := range cases {
		if got := noProxyMatches(c.list, c.host, c.port); got != c.want {
			t.Errorf("noProxyMatches(%q, %q, %q) = %v, want %v", c.list, c.host, c.port, got, c.want)
		}
	}
}

func TestNilURL(t *testing.T) {
	if _, err := For(nil); err == nil {
		t.Fatal("For(nil) returned no error")
	}
}
