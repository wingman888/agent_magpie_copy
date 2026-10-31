package netproxy

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func setup(t *testing.T, proxy string, sys *sysProxy) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	if proxy != "" {
		if err := settings.Save(settings.Settings{Proxy: proxy}); err != nil {
			t.Fatal(err)
		}
	}
	old := systemProxyReader
	systemProxyReader = func() (sysProxy, bool) {
		if sys == nil {
			return sysProxy{}, false
		}
		return *sys, true
	}
	ResetSystemCache()
	t.Cleanup(func() { systemProxyReader = old; ResetSystemCache() })
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

func forStr(t *testing.T, target string) string {
	t.Helper()
	p, err := For(mustURL(target))
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

func sysWith(p string, exc ...string) *sysProxy {
	return &sysProxy{URL: mustURL(p), Exceptions: exc}
}

func TestSystemProxy(t *testing.T) {
	setup(t, "", sysWith("http://127.0.0.1:7890", "*.corp"))
	if got := forStr(t, "https://chatgpt.com/x"); got != "http://127.0.0.1:7890" {
		t.Fatal(got)
	}
	if p, s := Describe(); s != "system" || p != "http://127.0.0.1:7890" {
		t.Fatal(p, s)
	}
	if got := forStr(t, "https://git.corp/x"); got != "" {
		t.Fatal(got)
	}
}

func TestSettings(t *testing.T) {
	setup(t, "socks5://127.0.0.1:1080", sysWith("http://sys:1"))
	t.Setenv("HTTPS_PROXY", "http://env:1")
	if got := forStr(t, "https://api.openai.com/v1"); got != "socks5://127.0.0.1:1080" {
		t.Fatal(got)
	}
	if p, s := Describe(); s != "settings" || p != "socks5://127.0.0.1:1080" {
		t.Fatal(p, s)
	}
	if got := forStr(t, "http://127.0.0.1:3425/"); got != "" {
		t.Fatal(got)
	}
}

func TestBareHostPort(t *testing.T) {
	setup(t, "127.0.0.1:7890", nil)
	if got := forStr(t, "https://a.com"); got != "http://127.0.0.1:7890" {
		t.Fatal(got)
	}
}

func TestDirect(t *testing.T) {
	setup(t, "direct", sysWith("http://sys:1"))
	t.Setenv("HTTPS_PROXY", "http://env:1")
	if got := forStr(t, "https://a.com"); got != "" {
		t.Fatal(got)
	}
	if p, s := Describe(); s != "off" || p != "" {
		t.Fatal(p, s)
	}
}

func TestEnvironment(t *testing.T) {
	setup(t, "", sysWith("http://sys:1"))
	t.Setenv("HTTPS_PROXY", "http://env:2")
	if got := forStr(t, "https://a.com"); got != "http://env:2" {
		t.Fatal(got)
	}
	if p, s := Describe(); s != "environment" || p != "http://env:2" {
		t.Fatal(p, s)
	}
	t.Setenv("NO_PROXY", "a.com")
	if got := forStr(t, "https://x.a.com"); got != "" {
		t.Fatal(got)
	}
	if got := forStr(t, "https://b.com"); got != "http://env:2" {
		t.Fatal(got)
	}
}

func TestLoopbackNeverProxied(t *testing.T) {
	setup(t, "http://p:1", sysWith("http://sys:1"))
	t.Setenv("HTTPS_PROXY", "http://env:2")
	for _, u := range []string{"http://127.0.0.1:3425/", "http://localhost/", "https://x.localhost/", "http://[::1]:80/", "http://127.5.5.5/"} {
		if got := forStr(t, u); got != "" {
			t.Fatal(u, got)
		}
	}
}

func TestNone(t *testing.T) {
	setup(t, "", nil)
	if got := forStr(t, "https://a.com"); got != "" {
		t.Fatal(got)
	}
	if p, s := Describe(); s != "none" || p != "" {
		t.Fatal(p, s)
	}
}

func TestBrokenSettingsErrors(t *testing.T) {
	setup(t, "", nil)
	path := settings.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"proxy":"ftp://x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := For(mustURL("https://a.com")); err == nil {
		t.Fatal("expected error")
	}
}

func TestDynamicSettings(t *testing.T) {
	setup(t, "", nil)
	if forStr(t, "https://a.com") != "" {
		t.Fatal("direct expected")
	}
	if err := settings.Save(settings.Settings{Proxy: "http://p:9"}); err != nil {
		t.Fatal(err)
	}
	if got := forStr(t, "https://a.com"); got != "http://p:9" {
		t.Fatal(got)
	}
}

const scutilAll = `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : intranet.example.com
  }
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : 10.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : 10.0.0.2
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 10.0.0.3
}`

func TestParseScutil(t *testing.T) {
	sp, ok := parseScutil(scutilAll)
	if !ok || sp.URL.String() != "http://10.0.0.2:8443" || len(sp.Exceptions) != 2 || sp.Exceptions[0] != "*.local" {
		t.Fatalf("%v %+v", ok, sp)
	}
	sp, ok = parseScutil("<dictionary> {\n  HTTPEnable : 0\n  HTTPPort : 8080\n  HTTPProxy : 10.0.0.1\n  SOCKSEnable : 1\n  SOCKSPort : 1080\n  SOCKSProxy : 10.0.0.3\n}")
	if !ok || sp.URL.String() != "socks5://10.0.0.3:1080" {
		t.Fatalf("%v %+v", ok, sp)
	}
	if _, ok = parseScutil("<dictionary> {\n  HTTPEnable : 0\n  HTTPProxy : 1.1.1.1\n  HTTPPort : 1\n  HTTPSEnable : 0\n  SOCKSEnable : 0\n}"); ok {
		t.Fatal("disabled proxies used")
	}
}

func TestParseWindows(t *testing.T) {
	sp, ok := parseWindows("127.0.0.1:7890", "", true)
	if !ok || sp.URL.String() != "http://127.0.0.1:7890" {
		t.Fatal(sp)
	}
	sp, _ = parseWindows("http=1.1.1.1:1;https=2.2.2.2:2", "", true)
	if sp.URL.String() != "http://2.2.2.2:2" {
		t.Fatal(sp)
	}
	sp, _ = parseWindows("socks=127.0.0.1:1080", "", true)
	if sp.URL.String() != "socks5://127.0.0.1:1080" {
		t.Fatal(sp)
	}
	if _, ok := parseWindows("127.0.0.1:7890", "", false); ok {
		t.Fatal("disabled")
	}
	setup(t, "", nil)
	sp, _ = parseWindows("127.0.0.1:7890", "*.corp;<local>", true)
	systemProxyReader = func() (sysProxy, bool) { return sp, true }
	ResetSystemCache()
	if forStr(t, "https://git.corp") != "" || forStr(t, "https://intranet") != "" {
		t.Fatal("override ignored")
	}
	if forStr(t, "https://chatgpt.com") != "http://127.0.0.1:7890" {
		t.Fatal("expected proxy")
	}
}

func TestBypass(t *testing.T) {
	l := []string{"", "Example.com", ".corp", "*.lan"}
	for host, want := range map[string]bool{"example.com": true, "EXAMPLE.com": true, "a.example.com": false, "corp": true, "x.corp": true, "lan": true, "a.b.lan": true, "other.com": false} {
		if bypass(host, l) != want {
			t.Fatal(host)
		}
	}
	if !bypass("intranet", []string{"<local>"}) || bypass("a.b", []string{"<local>"}) {
		t.Fatal("<local>")
	}
}

func TestOtherOSNone(t *testing.T) {
	old := goos
	goos = "linux"
	t.Cleanup(func() { goos = old })
	if _, ok := readSystemProxy(); ok {
		t.Fatal("linux has no system proxy")
	}
}
