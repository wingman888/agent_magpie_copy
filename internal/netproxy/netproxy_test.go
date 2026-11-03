package netproxy

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// isolate points settings at an empty folder, clears every proxy variable
// and replaces the OS reader with sys.
func isolate(t *testing.T, sys system) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy",
		"ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	old := systemProxy
	systemProxy = func() system { return sys }
	t.Cleanup(func() { systemProxy = old })
}

// forOf returns the proxy For picks for raw, "DIRECT" for nil.
func forOf(t *testing.T, raw string) string {
	t.Helper()
	u, _ := url.Parse(raw)
	p, err := For(u)
	if err != nil {
		t.Fatalf("For(%s): %v", raw, err)
	}
	if p == nil {
		return "DIRECT"
	}
	return p.String()
}

func TestSettingsProxyWins(t *testing.T) {
	isolate(t, system{HTTPS: "sys.example:1"})
	t.Setenv("HTTPS_PROXY", "http://env.example:2")
	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	if got := forOf(t, "https://api.example.com"); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("settings should win: %s", got)
	}
}

func TestDirectIgnoresEverything(t *testing.T) {
	isolate(t, system{HTTPS: "sys.example:1"})
	t.Setenv("HTTPS_PROXY", "http://env.example:2")
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if got := forOf(t, "https://api.example.com"); got != "DIRECT" {
		t.Fatalf("direct must connect directly: %s", got)
	}
}

func TestEnvBeforeSystem(t *testing.T) {
	isolate(t, system{HTTPS: "sys.example:1"})
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	if got := forOf(t, "https://api.example.com"); got != "http://env.example:3128" {
		t.Fatalf("env should beat system: %s", got)
	}
}

func TestEnvBareHostPortAndLowerCase(t *testing.T) {
	isolate(t, system{})
	t.Setenv("https_proxy", "127.0.0.1:7890")
	if got := forOf(t, "https://api.example.com"); got != "http://127.0.0.1:7890" {
		t.Fatalf("lower-case bare host:port: %s", got)
	}
}

func TestEnvSchemeSpecific(t *testing.T) {
	isolate(t, system{})
	t.Setenv("HTTP_PROXY", "http://plain.example:8080")
	if got := forOf(t, "http://api.example.com"); got != "http://plain.example:8080" {
		t.Fatalf("http should use HTTP_PROXY: %s", got)
	}
	if got := forOf(t, "https://api.example.com"); got != "DIRECT" {
		t.Fatalf("https must not use HTTP_PROXY: %s", got)
	}
	t.Setenv("ALL_PROXY", "socks5://all.example:1080")
	if got := forOf(t, "https://api.example.com"); got != "socks5://all.example:1080" {
		t.Fatalf("ALL_PROXY fallback: %s", got)
	}
}

func TestNoProxyExcludesEnv(t *testing.T) {
	isolate(t, system{})
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	t.Setenv("NO_PROXY", ".corp.example, 10.0.0.0/8, internal.test:443")
	for _, host := range []string{"https://a.corp.example", "https://10.1.2.3", "https://internal.test"} {
		if got := forOf(t, host); got != "DIRECT" {
			t.Errorf("%s should be excluded by NO_PROXY: %s", host, got)
		}
	}
	if got := forOf(t, "https://other.example"); got != "http://env.example:3128" {
		t.Errorf("other host should use the proxy: %s", got)
	}
}

func TestLoopbackNeverProxied(t *testing.T) {
	isolate(t, system{HTTP: "sys.example:1", NoLocal: true})
	t.Setenv("HTTP_PROXY", "http://env.example:2")
	t.Setenv("ALL_PROXY", "http://env.example:2")
	for _, host := range []string{"http://127.0.0.1:3425/", "http://localhost:3425", "http://[::1]:3425", "http://127.9.9.9"} {
		if got := forOf(t, host); got != "DIRECT" {
			t.Errorf("%s must be direct: %s", host, got)
		}
	}
}

func TestSystemPriorityHTTPSThenHTTPThenSOCKS(t *testing.T) {
	cases := []struct {
		sys  system
		want string
	}{
		{system{HTTPS: "a.example:1", HTTP: "b.example:2", SOCKS: "c.example:3"}, "http://a.example:1"},
		{system{HTTP: "b.example:2", SOCKS: "c.example:3"}, "http://b.example:2"},
		{system{SOCKS: "c.example:3"}, "socks5://c.example:3"},
		{system{}, "DIRECT"},
	}
	for _, c := range cases {
		isolate(t, c.sys)
		if got := forOf(t, "https://api.example.com"); got != c.want {
			t.Errorf("system %+v: got %s want %s", c.sys, got, c.want)
		}
	}
}

func TestSystemExceptionList(t *testing.T) {
	isolate(t, system{HTTPS: "a.example:1", Exceptions: []string{"*.local", "169.254/16", "192.168.*", "example.org"}})
	for _, host := range []string{"https://printer.local", "https://169.254.3.4", "https://192.168.1.2", "https://example.org"} {
		if got := forOf(t, host); got != "DIRECT" {
			t.Errorf("%s should match the exception list: %s", host, got)
		}
	}
	if got := forOf(t, "https://api.example.com"); got != "http://a.example:1" {
		t.Errorf("non-excepted host: %s", got)
	}
}

func TestSystemLocalNames(t *testing.T) {
	isolate(t, system{HTTP: "a.example:1", NoLocal: true})
	if got := forOf(t, "https://intranet"); got != "DIRECT" {
		t.Errorf("dotless name should be direct: %s", got)
	}
	if got := forOf(t, "https://api.example.com"); got != "http://a.example:1" {
		t.Errorf("dotted name should use proxy: %s", got)
	}
}

func TestInvalidSettingIsAnError(t *testing.T) {
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
		t.Fatal("ftp proxy in settings must be an error")
	}
}

func TestForRequestAndClient(t *testing.T) {
	isolate(t, system{})
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/v1", nil)
	p, err := ForRequest(req)
	if err != nil || p == nil || p.Host != "env.example:3128" {
		t.Fatalf("ForRequest: %v %v", p, err)
	}
	tr, ok := Client().Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatal("shared client must follow For")
	}
	if tr2 := Transport(); tr2.Proxy == nil {
		t.Fatal("Transport must follow For")
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
  HTTPProxy : 10.0.0.5
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : 10.0.0.6
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	sys := parseScutil(out)
	if sys.HTTPS != "10.0.0.6:8443" || sys.HTTP != "10.0.0.5:8080" || sys.SOCKS != "127.0.0.1:1080" {
		t.Fatalf("scutil proxies: %+v", sys)
	}
	if !sys.NoLocal || len(sys.Exceptions) != 2 || sys.Exceptions[0] != "*.local" || sys.Exceptions[1] != "169.254/16" {
		t.Fatalf("scutil exceptions: %+v", sys)
	}
	off := parseScutil("HTTPSEnable : 0\nHTTPSProxy : x\nHTTPSPort : 1\n")
	if off.HTTPS != "" {
		t.Fatalf("disabled proxy must be empty: %+v", off)
	}
}

func TestParseWindows(t *testing.T) {
	sys := parseWindows("0x1", "http=h1:80;https=h2:443;socks=h3:1080", "<local>;*.corp;10.*")
	if sys.HTTPS != "h2:443" || sys.HTTP != "h1:80" || sys.SOCKS != "h3:1080" {
		t.Fatalf("windows list: %+v", sys)
	}
	if !sys.NoLocal || len(sys.Exceptions) != 2 || sys.Exceptions[0] != "*.corp" || sys.Exceptions[1] != "10.*" {
		t.Fatalf("windows overrides: %+v", sys)
	}
	one := parseWindows("1", "127.0.0.1:9", "")
	if one.HTTP != "127.0.0.1:9" || one.HTTPS != "" {
		t.Fatalf("single server: %+v", one)
	}
	if off := parseWindows("0", "h:1", "*"); off.HTTP != "" || len(off.Exceptions) != 0 {
		t.Fatalf("disabled windows proxy: %+v", off)
	}
	if v := regValue("\nHKEY_CURRENT_USER\\X\n    ProxyOverride    REG_SZ    <local>;*.corp\n"); v != "<local>;*.corp" {
		t.Fatalf("regValue: %q", v)
	}
}
