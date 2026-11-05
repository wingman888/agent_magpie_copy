package netproxy

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// isolate clears every proxy variable and points settings at an empty folder.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, n := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(n, "")
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

func TestLoopbackIsNeverProxied(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://proxy.example:8080")
	t.Setenv("ALL_PROXY", "socks5://proxy.example:1080")
	if err := settings.Save(settings.Settings{Proxy: "http://proxy.example:9999"}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"http://127.0.0.1:3425/v1", "http://localhost:3425", "http://[::1]:3425", "http://127.0.0.2/", "http://app.localhost/"} {
		if got := forString(t, u); got != "" {
			t.Fatalf("%s went through %s", u, got)
		}
	}
}

func TestSettingsBeatEnvironment(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:8080")
	if got := forString(t, "https://api.example.com"); got != "http://env.example:8080" {
		t.Fatalf("env proxy: %q", got)
	}
	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	if got := forString(t, "https://api.example.com"); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("settings proxy: %q", got)
	}
}

func TestDirectSettingIgnoresEnvironment(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:8080")
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if got := forString(t, "https://api.example.com"); got != "" {
		t.Fatalf("direct still proxied via %s", got)
	}
}

func TestEnvironmentSchemes(t *testing.T) {
	isolate(t)
	t.Setenv("HTTP_PROXY", "http://plain.example:3128")
	if got := forString(t, "http://api.example.com"); got != "http://plain.example:3128" {
		t.Fatalf("http via HTTP_PROXY: %q", got)
	}
	if got := forString(t, "https://api.example.com"); got != "" {
		t.Fatalf("https must not use HTTP_PROXY: %q", got)
	}
	t.Setenv("https_proxy", "127.0.0.1:7890")
	if got := forString(t, "https://api.example.com"); got != "http://127.0.0.1:7890" {
		t.Fatalf("lower-case bare host:port: %q", got)
	}
	t.Setenv("https_proxy", "")
	t.Setenv("ALL_PROXY", "socks5h://all.example:1080")
	if got := forString(t, "https://api.example.com"); got != "socks5h://all.example:1080" {
		t.Fatalf("ALL_PROXY: %q", got)
	}
	t.Setenv("ALL_PROXY", "ftp://bad.example")
	if _, err := For(mustURL(t, "https://api.example.com")); err == nil {
		t.Fatal("unsupported env proxy scheme accepted")
	}
}

func TestNoProxy(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://proxy.example:8080")
	t.Setenv("NO_PROXY", ".corp.example, api.example.com:443, 10.0.0.0/8, *.lan")
	direct := []string{"https://a.corp.example", "https://corp.example", "https://api.example.com", "https://10.1.2.3", "https://box.lan"}
	for _, u := range direct {
		if got := forString(t, u); got != "" {
			t.Fatalf("%s should be direct, went through %s", u, got)
		}
	}
	if got := forString(t, "https://other.example.org"); got != "http://proxy.example:8080" {
		t.Fatalf("unlisted host: %q", got)
	}
	if got := forString(t, "https://11.0.0.1"); got != "http://proxy.example:8080" {
		t.Fatalf("outside 10/8: %q", got)
	}
}

func TestSystemListSyntax(t *testing.T) {
	cases := []struct {
		host string
		list []string
		want bool
	}{
		{"anything.example", []string{"*"}, true},
		{"intranet", []string{"<local>"}, true},
		{"a.b", []string{"<local>"}, false},
		{"printer.local", []string{"*.local"}, true},
		{"local", []string{"*.local"}, true},
		{"notlocal", []string{"*.local"}, false},
		{"10.1.2.3", []string{"10.*"}, true},
		{"11.1.2.3", []string{"10.*"}, false},
		{"169.254.9.9", []string{"169.254/16"}, true},
		{"169.255.9.9", []string{"169.254/16"}, false},
		{"172.16.5.5", []string{"172.16.0.0/12"}, true},
		{"example.com", []string{"example.com"}, true},
		{"a.example.com", []string{"example.com"}, true},
		{"notexample.com", []string{"example.com"}, false},
		{"fe80::1", []string{"fe80::/10"}, true},
		{"8.8.8.8", []string{"8.8.8.8"}, true},
		{"8.8.8.9", []string{"8.8.8.8"}, false},
	}
	for _, c := range cases {
		if got := listMatch(c.host, c.list); got != c.want {
			t.Fatalf("listMatch(%q, %v) = %v", c.host, c.list, got)
		}
	}
}

func TestSystemExceptionsAreHonoured(t *testing.T) {
	isolate(t)
	old := sysCache.val
	sysCache.val = sysProxy{HTTPS: "http://sys.example:443", Exceptions: []string{"*.internal"}}
	sysCache.at = time.Now()
	t.Cleanup(func() { sysCache.val = old; sysCache.at = time.Time{} })
	if got := forString(t, "https://db.internal"); got != "" {
		t.Fatalf("system exception ignored: %s", got)
	}
	if got := forString(t, "https://api.example.com"); got != "http://sys.example:443" {
		t.Fatalf("system proxy not used: %q", got)
	}
}

func TestParseScutil(t *testing.T) {
	out := []byte(`<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
  }
  ExcludeSimpleHostnames : 1
  HTTPEnable : 0
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : secure.example
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}
`)
	s := parseScutil(out)
	if s.HTTPS != "http://secure.example:8443" || s.HTTP != "" || s.SOCKS != "socks5://127.0.0.1:1080" {
		t.Fatalf("parsed %+v", s)
	}
	if s.url() != "http://secure.example:8443" {
		t.Fatalf("HTTPS should win: %q", s.url())
	}
	if !listMatch("box.local", s.Exceptions) || !listMatch("laptop", s.Exceptions) || listMatch("example.com", s.Exceptions) {
		t.Fatalf("exceptions %v", s.Exceptions)
	}
}

func TestParseWindowsProxy(t *testing.T) {
	s := parseWinProxy(true, "http=h1:8080;https=h2:8443;socks=h3:1080", "localhost;*.corp;<local>")
	if s.url() != "http://h2:8443" || s.SOCKS != "socks5://h3:1080" {
		t.Fatalf("parsed %+v", s)
	}
	if !listMatch("app.corp", s.Exceptions) || !listMatch("intranet", s.Exceptions) {
		t.Fatalf("override %v", s.Exceptions)
	}
	if one := parseWinProxy(true, "10.0.0.1:3128", ""); one.url() != "http://10.0.0.1:3128" {
		t.Fatalf("single server: %+v", one)
	}
	if off := parseWinProxy(false, "10.0.0.1:3128", ""); off.url() != "" {
		t.Fatalf("disabled proxy used: %+v", off)
	}
}

func TestSystemPriority(t *testing.T) {
	if got := (sysProxy{HTTP: "http://h:1", SOCKS: "socks5://s:2"}).url(); got != "http://h:1" {
		t.Fatalf("HTTP before SOCKS: %q", got)
	}
	if got := (sysProxy{SOCKS: "socks5://s:2"}).url(); got != "socks5://s:2" {
		t.Fatalf("SOCKS last: %q", got)
	}
}

func TestProxyFuncAndNil(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://proxy.example:8080")
	r, _ := http.NewRequest(http.MethodGet, "https://api.example.com/v1", nil)
	if p, err := ProxyFunc(r); err != nil || p == nil || p.Host != "proxy.example:8080" {
		t.Fatalf("ProxyFunc: %v %v", p, err)
	}
	if _, err := For(nil); err == nil {
		t.Fatal("nil URL accepted")
	}
}
