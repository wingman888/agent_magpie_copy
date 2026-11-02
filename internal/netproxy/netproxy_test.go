package netproxy

import (
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

func setup(t *testing.T, sys *sysConfig) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir()))
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	readSystem = func() *sysConfig { return sys }
	sysAt = time.Time{}
	t.Cleanup(func() { readSystem = platformSystem; sysAt = time.Time{} })
}

func target(s string) *url.URL { u, _ := url.Parse(s); return u }

func got(t *testing.T, s string) string {
	t.Helper()
	p, err := For(target(s))
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

func TestSettingsWin(t *testing.T) {
	setup(t, &sysConfig{HTTPS: target("http://sys:1")})
	t.Setenv("HTTPS_PROXY", "http://env:2")
	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	if g := got(t, "https://api.example.com/v1"); g != "socks5://127.0.0.1:1080" {
		t.Fatal(g)
	}
	if g := got(t, "http://127.0.0.1:3425/"); g != "" {
		t.Fatal("loopback proxied: " + g)
	}
	if g := got(t, "http://localhost/"); g != "" {
		t.Fatal(g)
	}
	if err := settings.Save(settings.Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if g := got(t, "https://api.example.com"); g != "http://127.0.0.1:7890" {
		t.Fatal(g)
	}
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if g := got(t, "https://api.example.com"); g != "" {
		t.Fatal(g)
	}
	if settings.Save(settings.Settings{Proxy: "ftp://x"}) == nil {
		t.Fatal("ftp accepted")
	}
	if settings.Save(settings.Settings{Tray: "bogus"}) == nil {
		t.Fatal("tray accepted")
	}
	if err := settings.Save(settings.Settings{Tray: "window"}); err != nil || settings.Load().Tray != "window" {
		t.Fatal("tray")
	}
	if settings.Load().Proxy != "" {
		t.Fatal("proxy should be cleared")
	}
}

func TestEnvThenSystem(t *testing.T) {
	setup(t, &sysConfig{HTTP: target("http://sys:1"), Exceptions: []string{"*.corp.example", "10.0.0.0/8"}, ExcludeSimple: true})
	if g := got(t, "https://api.example.com"); g != "http://sys:1" {
		t.Fatal("system:", g)
	}
	for _, h := range []string{"https://x.corp.example", "https://corp.example", "https://10.1.2.3", "https://intranet"} {
		if g := got(t, h); g != "" {
			t.Fatal(h, g)
		}
	}
	t.Setenv("HTTPS_PROXY", "http://env:2")
	t.Setenv("NO_PROXY", "skip.example,.dev.test")
	if g := got(t, "https://api.example.com"); g != "http://env:2" {
		t.Fatal("env:", g)
	}
	for _, h := range []string{"https://skip.example", "https://a.dev.test"} {
		if g := got(t, h); g != "" {
			t.Fatal(h, g)
		}
	}
}

func TestSystemPriority(t *testing.T) {
	c := &sysConfig{HTTP: target("http://h:1"), HTTPS: target("http://s:2"), SOCKS: target("socks5://k:3")}
	if c.pick(target("http://a.com")).Host != "s:2" {
		t.Fatal("https first")
	}
	c.HTTPS = nil
	if c.pick(target("https://a.com")).Host != "h:1" {
		t.Fatal("http second")
	}
	c.HTTP = nil
	if c.pick(target("https://a.com")).Scheme != "socks5" {
		t.Fatal("socks last")
	}
}

func TestParsers(t *testing.T) {
	c := parseScutil(`<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
  }
  ExcludeSimpleHostnames : 1
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : 10.0.0.1
  HTTPSEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 10.0.0.2
}`)
	if c == nil || c.HTTP.Host != "10.0.0.1:8080" || c.HTTPS != nil || c.SOCKS.Scheme != "socks5" || len(c.Exceptions) != 2 || !c.ExcludeSimple {
		t.Fatalf("%+v", c)
	}
	if !c.bypass("foo.local") || c.bypass("example.com") {
		t.Fatal("bypass")
	}
	w := parseWindows("http=a:1;https=b:2;socks=c:3", "*.lan;<local>")
	if w.HTTPS.Host != "b:2" || w.SOCKS.Host != "c:3" || !w.bypass("x.lan") || !w.bypass("intranet") || w.bypass("a.com") {
		t.Fatalf("%+v", w)
	}
	if w := parseWindows("p:9", ""); w.HTTP.Host != "p:9" {
		t.Fatal("bare")
	}
}
