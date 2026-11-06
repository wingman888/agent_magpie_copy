package netproxy

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// setupL3x isolates the test from the developer's settings, environment and system proxy.
func setupL3x(t *testing.T, sys sysProxyL3x) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, n := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(n, "")
	}
	old := systemProxyL3x
	systemProxyL3x = func() sysProxyL3x { return sys }
	t.Cleanup(func() { systemProxyL3x = old })
}

func setSettingL3x(t *testing.T, value string) {
	t.Helper()
	if err := settings.Save(settings.Settings{Proxy: value}); err != nil {
		t.Fatal(err)
	}
}

func mustURLL3x(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func forHostL3x(t *testing.T, raw string) string {
	t.Helper()
	p, err := For(mustURLL3x(t, raw))
	if err != nil {
		t.Fatalf("For(%s): %v", raw, err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

func TestSettingsWinOverEnvAndSystemL3x(t *testing.T) {
	setupL3x(t, sysProxyL3x{HTTPS: "http://sys:1"})
	t.Setenv("HTTPS_PROXY", "http://env:2")
	setSettingL3x(t, "10.0.0.1:8080")
	if got := forHostL3x(t, "https://api.example.com/v1"); got != "http://10.0.0.1:8080" {
		t.Fatalf("settings proxy: %q", got)
	}
}

func TestDirectSettingL3x(t *testing.T) {
	setupL3x(t, sysProxyL3x{HTTPS: "http://sys:1"})
	t.Setenv("HTTPS_PROXY", "http://env:2")
	setSettingL3x(t, "direct")
	if got := forHostL3x(t, "https://api.example.com"); got != "" {
		t.Fatalf("direct should connect directly, got %q", got)
	}
}

func TestEnvironmentBeforeSystemL3x(t *testing.T) {
	setupL3x(t, sysProxyL3x{HTTPS: "http://sys:1"})
	t.Setenv("HTTPS_PROXY", "http://env:2")
	if got := forHostL3x(t, "https://api.example.com"); got != "http://env:2" {
		t.Fatalf("env proxy: %q", got)
	}
	// HTTP_PROXY applies to http requests only, so this one falls to the system
	if got := forHostL3x(t, "http://api.example.com"); got != "http://sys:1" {
		t.Fatalf("http request should fall through to system: %q", got)
	}
}

func TestEnvironmentHTTPProxyL3x(t *testing.T) {
	setupL3x(t, sysProxyL3x{})
	t.Setenv("http_proxy", "127.0.0.1:3128")
	if got := forHostL3x(t, "http://api.example.com"); got != "http://127.0.0.1:3128" {
		t.Fatalf("http_proxy: %q", got)
	}
}

func TestNoProxyL3x(t *testing.T) {
	setupL3x(t, sysProxyL3x{HTTPS: "http://sys:1"})
	t.Setenv("HTTPS_PROXY", "http://env:2")
	t.Setenv("NO_PROXY", ".corp.example, internal.test")
	if got := forHostL3x(t, "https://api.corp.example"); got != "" {
		t.Fatalf("NO_PROXY suffix should be direct: %q", got)
	}
	if got := forHostL3x(t, "https://internal.test"); got != "" {
		t.Fatalf("NO_PROXY exact should be direct: %q", got)
	}
	if got := forHostL3x(t, "https://api.example.com"); got != "http://env:2" {
		t.Fatalf("non-excluded host: %q", got)
	}
}

func TestLoopbackNeverProxiedL3x(t *testing.T) {
	setupL3x(t, sysProxyL3x{HTTPS: "http://sys:1", HTTP: "http://sys:1"})
	setSettingL3x(t, "http://10.0.0.1:8080")
	for _, raw := range []string{"http://127.0.0.1:3425/", "http://localhost:3425", "http://[::1]:3425", "http://127.9.9.9/"} {
		if got := forHostL3x(t, raw); got != "" {
			t.Fatalf("%s went through proxy %q", raw, got)
		}
	}
	if p, err := Proxy(&http.Request{URL: mustURLL3x(t, "http://127.0.0.1:1")}); p != nil || err != nil {
		t.Fatalf("Proxy for loopback: %v %v", p, err)
	}
}

func TestSystemOrderHTTPSHTTPSocksL3x(t *testing.T) {
	all := sysProxyL3x{HTTPS: "http://h:443", HTTP: "http://x:80", SOCKS: "socks5://s:1080"}
	setupL3x(t, all)
	if got := forHostL3x(t, "https://api.example.com"); got != "http://h:443" {
		t.Fatalf("HTTPS first: %q", got)
	}
	setupL3x(t, sysProxyL3x{HTTP: "http://x:80", SOCKS: "socks5://s:1080"})
	if got := forHostL3x(t, "https://api.example.com"); got != "http://x:80" {
		t.Fatalf("HTTP second: %q", got)
	}
	setupL3x(t, sysProxyL3x{SOCKS: "socks5://s:1080"})
	if got := forHostL3x(t, "https://api.example.com"); got != "socks5://s:1080" {
		t.Fatalf("SOCKS last: %q", got)
	}
	setupL3x(t, sysProxyL3x{})
	if got := forHostL3x(t, "https://api.example.com"); got != "" {
		t.Fatalf("no system proxy should be direct: %q", got)
	}
}

func TestSystemBypassL3x(t *testing.T) {
	setupL3x(t, sysProxyL3x{HTTPS: "http://h:443", Bypass: []string{"*.local", "<local>", "10.0.0.0/8", "*.intranet.example"}})
	for _, raw := range []string{"https://printer/", "https://nas.local/", "https://10.1.2.3/", "https://wiki.intranet.example/"} {
		if got := forHostL3x(t, raw); got != "" {
			t.Fatalf("%s should bypass system proxy, got %q", raw, got)
		}
	}
	if got := forHostL3x(t, "https://api.example.com"); got != "http://h:443" {
		t.Fatalf("public host: %q", got)
	}
}

func TestSettingsDoesNotBypassSystemListL3x(t *testing.T) {
	// an explicit proxy in settings is used as the user wrote it
	setupL3x(t, sysProxyL3x{HTTPS: "http://h:443", Bypass: []string{"*"}})
	setSettingL3x(t, "http://10.0.0.1:8080")
	if got := forHostL3x(t, "https://nas.local/"); got != "http://10.0.0.1:8080" {
		t.Fatalf("settings proxy: %q", got)
	}
}

func TestBypassMatcherL3x(t *testing.T) {
	cases := []struct {
		host string
		list []string
		want bool
	}{
		{"example.com", []string{"example.com"}, true},
		{"a.example.com", []string{"example.com"}, true},
		{"notexample.com", []string{"example.com"}, false},
		{"a.example.com", []string{".example.com"}, true},
		{"example.com", []string{"*.example.com"}, false},
		{"a.example.com", []string{"*.example.com"}, true},
		{"anything", []string{"*"}, true},
		{"host", []string{"<local>"}, true},
		{"host.lan", []string{"<local>"}, false},
		{"172.16.4.4", []string{"172.16.0.0/12"}, true},
		{"8.8.8.8", []string{"172.16.0.0/12"}, false},
		{"8.8.8.8", []string{"8.8.8.8"}, true},
	}
	for _, c := range cases {
		if got := bypassedL3x(c.host, c.list); got != c.want {
			t.Errorf("bypassedL3x(%q, %v) = %v", c.host, c.list, got)
		}
	}
}

func TestParseScutilL3x(t *testing.T) {
	out := `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
  }
  ExcludeSimpleHostnames : 1
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : 10.0.0.2
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : 10.0.0.3
  SOCKSEnable : 0
  SOCKSPort : 1080
  SOCKSProxy : 10.0.0.4
}`
	sp := parseScutilL3x(out)
	if sp.HTTPS != "http://10.0.0.3:8443" || sp.HTTP != "http://10.0.0.2:8080" || sp.SOCKS != "" {
		t.Fatalf("scutil: %+v", sp)
	}
	want := []string{"*.local", "169.254/16", "<local>"}
	if len(sp.Bypass) != len(want) {
		t.Fatalf("bypass: %v", sp.Bypass)
	}
	for i := range want {
		if sp.Bypass[i] != want[i] {
			t.Fatalf("bypass: %v", sp.Bypass)
		}
	}
}

func TestParseWindowsL3x(t *testing.T) {
	sp := parseWindowsL3x("0x1", "http=p1:80;https=p2:443;socks=s:1080", "localhost;*.corp;<local>")
	if sp.HTTP != "http://p1:80" || sp.HTTPS != "http://p2:443" || sp.SOCKS != "socks5://s:1080" {
		t.Fatalf("windows: %+v", sp)
	}
	if len(sp.Bypass) != 3 {
		t.Fatalf("override: %v", sp.Bypass)
	}
	if one := parseWindowsL3x("0x1", "127.0.0.1:7890", ""); one.HTTPS != "http://127.0.0.1:7890" || one.HTTP != one.HTTPS {
		t.Fatalf("single address: %+v", one)
	}
	if off := parseWindowsL3x("0x0", "p:1", ""); off.HTTP != "" || off.HTTPS != "" {
		t.Fatalf("disabled: %+v", off)
	}
}

func TestRegValueL3x(t *testing.T) {
	out := "\r\nHKEY_CURRENT_USER\\Software\\X\r\n    ProxyEnable    REG_DWORD    0x1\r\n"
	typ, v, ok := regValueL3x(out, "ProxyEnable")
	if !ok || typ != "REG_DWORD" || v != "0x1" {
		t.Fatalf("regValueL3x: %q %q %v", typ, v, ok)
	}
	out = "    ProxyOverride    REG_SZ    localhost;<local>\n"
	if _, v, _ := regValueL3x(out, "ProxyOverride"); v != "localhost;<local>" {
		t.Fatalf("regValueL3x string: %q", v)
	}
}

func TestTransportUsesProxyL3x(t *testing.T) {
	setupL3x(t, sysProxyL3x{})
	setSettingL3x(t, "http://10.0.0.1:8080")
	tr := Transport()
	if tr.Proxy == nil {
		t.Fatal("Transport has no Proxy")
	}
	p, err := tr.Proxy(&http.Request{URL: mustURLL3x(t, "https://api.example.com")})
	if err != nil || p == nil || p.String() != "http://10.0.0.1:8080" {
		t.Fatalf("transport proxy: %v %v", p, err)
	}
	if Client().Transport == nil {
		t.Fatal("shared clientL3x has no transport")
	}
}

func TestInvalidEnvNotEchoedL3x(t *testing.T) {
	setupL3x(t, sysProxyL3x{})
	t.Setenv("HTTPS_PROXY", "ftp://user:secret@h:1")
	_, err := For(mustURLL3x(t, "https://api.example.com"))
	if err == nil {
		t.Fatal("invalid env accepted")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("credentials in error: %v", err)
	}
}

func TestBypassTrailingWildcardL3x(t *testing.T) {
	cases := []struct {
		host string
		list []string
		want bool
	}{
		{"192.168.1.5", []string{"192.168.*"}, true},
		{"192.168.1.5", []string{"10.*", "192.168.*"}, true},
		{"8.8.8.8", []string{"192.168.*"}, false},
		{"intranet.example.com", []string{"intranet.*"}, true},
		{"example.com", []string{"intranet.*"}, false},
		{"localhost", []string{"*.local"}, false},
	}
	for _, c := range cases {
		if got := bypassedL3x(c.host, c.list); got != c.want {
			t.Errorf("bypassedL3x(%q, %v) = %v, want %v", c.host, c.list, got, c.want)
		}
	}
}
