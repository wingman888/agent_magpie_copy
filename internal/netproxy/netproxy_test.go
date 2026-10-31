package netproxy

import (
	"net/url"
	"testing"
)

func u(s string) *url.URL { x, _ := url.Parse(s); return x }

func setup(t *testing.T, setting string, sys *SystemConfig) {
	oldS, oldY := Setting, System
	Setting = func() string { return setting }
	System = func() *SystemConfig { return sys }
	t.Cleanup(func() { Setting, System = oldS, oldY })
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
}

func str(p *url.URL) string {
	if p == nil {
		return ""
	}
	return p.String()
}

func TestSettingWins(t *testing.T) {
	setup(t, "127.0.0.1:7890", &SystemConfig{HTTPS: u("http://sys:1")})
	t.Setenv("HTTPS_PROXY", "http://env:1")
	p, err := For(u("https://api.x.com/v1"))
	if err != nil || str(p) != "http://127.0.0.1:7890" {
		t.Fatal(p, err)
	}
	setup(t, "socks5://127.0.0.1:1080", nil)
	if p, _ := For(u("https://api.x.com")); str(p) != "socks5://127.0.0.1:1080" {
		t.Fatal(p)
	}
	setup(t, "direct", &SystemConfig{HTTPS: u("http://sys:1")})
	t.Setenv("HTTPS_PROXY", "http://env:1")
	if p, _ := For(u("https://api.x.com")); p != nil {
		t.Fatal(p)
	}
}

func TestLoopbackNever(t *testing.T) {
	setup(t, "127.0.0.1:7890", nil)
	for _, h := range []string{"http://127.0.0.1:3425", "http://localhost:1", "http://[::1]:2"} {
		if p, _ := For(u(h)); p != nil {
			t.Fatal(h, p)
		}
	}
}

func TestEnvThenSystem(t *testing.T) {
	sys := &SystemConfig{HTTP: u("http://sys:1")}
	setup(t, "", sys)
	if p, _ := For(u("https://a.com")); str(p) != "http://sys:1" {
		t.Fatal("system", p)
	}
	t.Setenv("HTTPS_PROXY", "http://env:2")
	t.Setenv("NO_PROXY", "internal.corp,.skip.com,10.0.0.0/8")
	if p, _ := For(u("https://a.com")); str(p) != "http://env:2" {
		t.Fatal("env", p)
	}
	for _, h := range []string{"https://internal.corp", "https://x.skip.com", "https://10.1.2.3"} {
		if p, _ := For(u(h)); p != nil {
			t.Fatal(h, p)
		}
	}
}

func TestSystemPreferenceAndExceptions(t *testing.T) {
	c := ParseScutil(`<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
    2 : 192.168.*
  }
  ExcludeSimpleHostnames : 1
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : h.example
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : hs.example
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : s.example
}`)
	setup(t, "", c)
	if p, _ := For(u("https://a.com")); str(p) != "http://hs.example:8443" {
		t.Fatal(p)
	}
	for _, h := range []string{"https://foo.local", "https://169.254.3.4", "https://192.168.1.1", "https://intranet"} {
		if p, _ := For(u(h)); p != nil {
			t.Fatal(h, p)
		}
	}
	c.HTTPS = nil
	if p, _ := For(u("https://a.com")); str(p) != "http://h.example:8080" {
		t.Fatal(p)
	}
	c.HTTP = nil
	if p, _ := For(u("https://a.com")); str(p) != "socks5://s.example:1080" {
		t.Fatal(p)
	}
}

func TestWinReg(t *testing.T) {
	c := ParseWinReg(`
HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Internet Settings
    ProxyEnable    REG_DWORD    0x1
    ProxyServer    REG_SZ    http=h:80;https=hs:443;socks=s:1080
    ProxyOverride    REG_SZ    *.corp.com;10.*;<local>
`)
	setup(t, "", c)
	if p, _ := For(u("https://a.com")); str(p) != "http://hs:443" {
		t.Fatal(p)
	}
	for _, h := range []string{"https://x.corp.com", "https://10.2.2.2", "https://box"} {
		if p, _ := For(u(h)); p != nil {
			t.Fatal(h, p)
		}
	}
	if ParseWinReg("ProxyEnable    REG_DWORD    0x0\n") != nil {
		t.Fatal("disabled")
	}
}
