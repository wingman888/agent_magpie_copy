package netproxy

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// setup isolates the test from the developer's settings, environment and system proxy.
func setup(t *testing.T, sys sysProxy) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, n := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(n, "")
	}
	old := systemProxy
	systemProxy = func() sysProxy { return sys }
	t.Cleanup(func() { systemProxy = old })
}

func setSetting(t *testing.T, value string) {
	t.Helper()
	if err := settings.Save(settings.Settings{Proxy: value}); err != nil {
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

func forHost(t *testing.T, raw string) string {
	t.Helper()
	p, err := For(mustURL(t, raw))
	if err != nil {
		t.Fatalf("For(%s): %v", raw, err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

func TestSettingsWinOverEnvAndSystem(t *testing.T) {
	setup(t, sysProxy{HTTPS: "http://sys:1"})
	t.Setenv("HTTPS_PROXY", "http://env:2")
	setSetting(t, "10.0.0.1:8080")
	if got := forHost(t, "https://api.example.com/v1"); got != "http://10.0.0.1:8080" {
		t.Fatalf("settings proxy: %q", got)
	}
}

func TestDirectSetting(t *testing.T) {
	setup(t, sysProxy{HTTPS: "http://sys:1"})
	t.Setenv("HTTPS_PROXY", "http://env:2")
	setSetting(t, "direct")
	if got := forHost(t, "https://api.example.com"); got != "" {
		t.Fatalf("direct should connect directly, got %q", got)
	}
}

func TestEnvironmentBeforeSystem(t *testing.T) {
	setup(t, sysProxy{HTTPS: "http://sys:1"})
	t.Setenv("HTTPS_PROXY", "http://env:2")
	if got := forHost(t, "https://api.example.com"); got != "http://env:2" {
		t.Fatalf("env proxy: %q", got)
	}
	// HTTP_PROXY applies to http requests only, so this one falls to the system
	if got := forHost(t, "http://api.example.com"); got != "http://sys:1" {
		t.Fatalf("http request should fall through to system: %q", got)
	}
}

func TestEnvironmentHTTPProxy(t *testing.T) {
	setup(t, sysProxy{})
	t.Setenv("http_proxy", "127.0.0.1:3128")
	if got := forHost(t, "http://api.example.com"); got != "http://127.0.0.1:3128" {
		t.Fatalf("http_proxy: %q", got)
	}
}

func TestNoProxy(t *testing.T) {
	setup(t, sysProxy{HTTPS: "http://sys:1"})
	t.Setenv("HTTPS_PROXY", "http://env:2")
	t.Setenv("NO_PROXY", ".corp.example, internal.test")
	if got := forHost(t, "https://api.corp.example"); got != "" {
		t.Fatalf("NO_PROXY suffix should be direct: %q", got)
	}
	if got := forHost(t, "https://internal.test"); got != "" {
		t.Fatalf("NO_PROXY exact should be direct: %q", got)
	}
	if got := forHost(t, "https://api.example.com"); got != "http://env:2" {
		t.Fatalf("non-excluded host: %q", got)
	}
}

func TestLoopbackNeverProxied(t *testing.T) {
	setup(t, sysProxy{HTTPS: "http://sys:1", HTTP: "http://sys:1"})
	setSetting(t, "http://10.0.0.1:8080")
	for _, raw := range []string{"http://127.0.0.1:3425/", "http://localhost:3425", "http://[::1]:3425", "http://127.9.9.9/"} {
		if got := forHost(t, raw); got != "" {
			t.Fatalf("%s went through proxy %q", raw, got)
		}
	}
	if p, err := Proxy(&http.Request{URL: mustURL(t, "http://127.0.0.1:1")}); p != nil || err != nil {
		t.Fatalf("Proxy for loopback: %v %v", p, err)
	}
}

func TestSystemOrderHTTPSHTTPSocks(t *testing.T) {
	all := sysProxy{HTTPS: "http://h:443", HTTP: "http://x:80", SOCKS: "socks5://s:1080"}
	setup(t, all)
	if got := forHost(t, "https://api.example.com"); got != "http://h:443" {
		t.Fatalf("HTTPS first: %q", got)
	}
	setup(t, sysProxy{HTTP: "http://x:80", SOCKS: "socks5://s:1080"})
	if got := forHost(t, "https://api.example.com"); got != "http://x:80" {
		t.Fatalf("HTTP second: %q", got)
	}
	setup(t, sysProxy{SOCKS: "socks5://s:1080"})
	if got := forHost(t, "https://api.example.com"); got != "socks5://s:1080" {
		t.Fatalf("SOCKS last: %q", got)
	}
	setup(t, sysProxy{})
	if got := forHost(t, "https://api.example.com"); got != "" {
		t.Fatalf("no system proxy should be direct: %q", got)
	}
}

func TestSystemBypass(t *testing.T) {
	setup(t, sysProxy{HTTPS: "http://h:443", Bypass: []string{"*.local", "<local>", "10.0.0.0/8", "*.intranet.example"}})
	for _, raw := range []string{"https://printer/", "https://nas.local/", "https://10.1.2.3/", "https://wiki.intranet.example/"} {
		if got := forHost(t, raw); got != "" {
			t.Fatalf("%s should bypass system proxy, got %q", raw, got)
		}
	}
	if got := forHost(t, "https://api.example.com"); got != "http://h:443" {
		t.Fatalf("public host: %q", got)
	}
}

func TestSettingsDoesNotBypassSystemList(t *testing.T) {
	// an explicit proxy in settings is used as the user wrote it
	setup(t, sysProxy{HTTPS: "http://h:443", Bypass: []string{"*"}})
	setSetting(t, "http://10.0.0.1:8080")
	if got := forHost(t, "https://nas.local/"); got != "http://10.0.0.1:8080" {
		t.Fatalf("settings proxy: %q", got)
	}
}

func TestBypassMatcher(t *testing.T) {
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
		if got := bypassed(c.host, c.list); got != c.want {
			t.Errorf("bypassed(%q, %v) = %v", c.host, c.list, got)
		}
	}
}

func TestParseScutil(t *testing.T) {
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
	sp := parseScutil(out)
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

func TestParseWindows(t *testing.T) {
	sp := parseWindows("0x1", "http=p1:80;https=p2:443;socks=s:1080", "localhost;*.corp;<local>")
	if sp.HTTP != "http://p1:80" || sp.HTTPS != "http://p2:443" || sp.SOCKS != "socks5://s:1080" {
		t.Fatalf("windows: %+v", sp)
	}
	if len(sp.Bypass) != 3 {
		t.Fatalf("override: %v", sp.Bypass)
	}
	if one := parseWindows("0x1", "127.0.0.1:7890", ""); one.HTTPS != "http://127.0.0.1:7890" || one.HTTP != one.HTTPS {
		t.Fatalf("single address: %+v", one)
	}
	if off := parseWindows("0x0", "p:1", ""); off.HTTP != "" || off.HTTPS != "" {
		t.Fatalf("disabled: %+v", off)
	}
}

func TestRegValue(t *testing.T) {
	out := "\r\nHKEY_CURRENT_USER\\Software\\X\r\n    ProxyEnable    REG_DWORD    0x1\r\n"
	typ, v, ok := regValue(out, "ProxyEnable")
	if !ok || typ != "REG_DWORD" || v != "0x1" {
		t.Fatalf("regValue: %q %q %v", typ, v, ok)
	}
	out = "    ProxyOverride    REG_SZ    localhost;<local>\n"
	if _, v, _ := regValue(out, "ProxyOverride"); v != "localhost;<local>" {
		t.Fatalf("regValue string: %q", v)
	}
}

func TestTransportUsesProxy(t *testing.T) {
	setup(t, sysProxy{})
	setSetting(t, "http://10.0.0.1:8080")
	tr := Transport()
	if tr.Proxy == nil {
		t.Fatal("Transport has no Proxy")
	}
	p, err := tr.Proxy(&http.Request{URL: mustURL(t, "https://api.example.com")})
	if err != nil || p == nil || p.String() != "http://10.0.0.1:8080" {
		t.Fatalf("transport proxy: %v %v", p, err)
	}
	if Client().Transport == nil {
		t.Fatal("shared client has no transport")
	}
}

func TestInvalidEnvNotEchoed(t *testing.T) {
	setup(t, sysProxy{})
	t.Setenv("HTTPS_PROXY", "ftp://user:secret@h:1")
	_, err := For(mustURL(t, "https://api.example.com"))
	if err == nil {
		t.Fatal("invalid env accepted")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("credentials in error: %v", err)
	}
}
