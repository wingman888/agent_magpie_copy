package netproxy

import (
	"net/url"
	"testing"
)

func setup(t *testing.T, setting, sysProxy string, exc ...string) {
	t.Helper()
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	oldS, oldR, oldG := SettingsProxy, readSystem, goos
	SettingsProxy = func() string { return setting }
	readSystem = func() (string, []string) { return sysProxy, exc }
	ResetCache()
	t.Cleanup(func() { SettingsProxy, readSystem, goos = oldS, oldR, oldG; ResetCache() })
}

func forS(t *testing.T, s string) string {
	t.Helper()
	u, _ := url.Parse(s)
	p, err := For(u)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

func TestSystem(t *testing.T) {
	setup(t, "", "http://127.0.0.1:7890", "*.corp")
	if g := forS(t, "https://chatgpt.com/x"); g != "http://127.0.0.1:7890" {
		t.Fatal(g)
	}
	if g := forS(t, "https://git.corp/x"); g != "" {
		t.Fatal(g)
	}
	if p, s := Describe(); s != "system" || p == "" {
		t.Fatal(p, s)
	}
}

func TestSettingsAndDirect(t *testing.T) {
	setup(t, "socks5://127.0.0.1:1080", "http://1.1.1.1:1")
	t.Setenv("HTTPS_PROXY", "http://2.2.2.2:2")
	if g := forS(t, "https://api.openai.com"); g != "socks5://127.0.0.1:1080" {
		t.Fatal(g)
	}
	if _, s := Describe(); s != "settings" {
		t.Fatal(s)
	}
	for _, h := range []string{"http://127.0.0.1:3425/x", "http://localhost/", "http://a.localhost/", "http://[::1]:80/"} {
		if g := forS(t, h); g != "" {
			t.Fatal(h, g)
		}
	}
	SettingsProxy = func() string { return "Direct" }
	if g := forS(t, "https://api.openai.com"); g != "" {
		t.Fatal(g)
	}
	if p, s := Describe(); p != "" || s != "off" {
		t.Fatal(p, s)
	}
	SettingsProxy = func() string { return "ftp://x" }
	u, _ := url.Parse("https://a.com")
	if _, err := For(u); err == nil {
		t.Fatal("want error")
	}
}

func TestEnv(t *testing.T) {
	setup(t, "", "http://1.1.1.1:1")
	t.Setenv("HTTPS_PROXY", "10.0.0.1:8080")
	t.Setenv("NO_PROXY", "internal.example.com,.foo.org")
	if g := forS(t, "https://a.com"); g != "http://10.0.0.1:8080" {
		t.Fatal(g)
	}
	if g := forS(t, "https://internal.example.com"); g != "" {
		t.Fatal(g)
	}
	if g := forS(t, "https://x.foo.org"); g != "" {
		t.Fatal(g)
	}
	if _, s := Describe(); s != "environment" {
		t.Fatal(s)
	}
}

func TestNone(t *testing.T) {
	setup(t, "", "")
	goos = "linux"
	readSystem = defaultReadSystem
	ResetCache()
	if g := forS(t, "https://a.com"); g != "" {
		t.Fatal(g)
	}
	if p, s := Describe(); p != "" || s != "none" {
		t.Fatal(p, s)
	}
}

func TestNormalize(t *testing.T) {
	ok := map[string]string{
		"127.0.0.1:7890":         "http://127.0.0.1:7890",
		"  socks5h://h:1 ":       "socks5h://h:1",
		"https://p.example.com/": "https://p.example.com",
	}
	for in, want := range ok {
		if g, err := Normalize(in); err != nil || g != want {
			t.Fatal(in, g, err)
		}
	}
	for _, in := range []string{"ftp://x", "http://", "not a proxy", "http://h:abc", ""} {
		if _, err := Normalize(in); err == nil {
			t.Fatal("accepted", in)
		}
	}
}

func TestParseScutil(t *testing.T) {
	out := `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
    2 : *.corp
  }
  HTTPEnable : 1
  HTTPPort : 8001
  HTTPProxy : 10.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 8002
  HTTPSProxy : 10.0.0.2
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 10.0.0.3
}`
	p, exc := ParseScutil(out)
	if p != "http://10.0.0.2:8002" || len(exc) != 3 || exc[2] != "*.corp" {
		t.Fatal(p, exc)
	}
	p, _ = ParseScutil("  HTTPEnable : 0\n  HTTPProxy : 1.1.1.1\n  HTTPPort : 1\n  SOCKSEnable : 1\n  SOCKSProxy : 127.0.0.1\n  SOCKSPort : 1080\n")
	if p != "socks5://127.0.0.1:1080" {
		t.Fatal(p)
	}
	if p, _ = ParseScutil("  HTTPEnable : 0\n  HTTPProxy : 1.1.1.1\n  HTTPPort : 1\n"); p != "" {
		t.Fatal(p)
	}
}

func TestParseWindows(t *testing.T) {
	mk := func(en, server, over string) string {
		return "HKEY_CURRENT_USER\\Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings\r\n    ProxyEnable    REG_DWORD    " + en +
			"\r\n    ProxyServer    REG_SZ    " + server + "\r\n    ProxyOverride    REG_SZ    " + over + "\r\n"
	}
	p, exc := ParseWindowsReg(mk("0x1", "127.0.0.1:7890", "*.corp;<local>"))
	if p != "http://127.0.0.1:7890" || len(exc) != 2 {
		t.Fatal(p, exc)
	}
	if p, _ = ParseWindowsReg(mk("0x0", "127.0.0.1:7890", "")); p != "" {
		t.Fatal(p)
	}
	if p, _ = ParseWindowsReg(mk("0x1", "http=1.1.1.1:1;https=2.2.2.2:2", "")); p != "http://2.2.2.2:2" {
		t.Fatal(p)
	}
	if p, _ = ParseWindowsReg(mk("0x1", "socks=127.0.0.1:1080", "")); p != "socks5://127.0.0.1:1080" {
		t.Fatal(p)
	}
	if !matchExceptions("git.corp", exc) || !matchExceptions("intranet", exc) || matchExceptions("chatgpt.com", exc) {
		t.Fatal("exceptions")
	}
	if !matchExceptions("EXAMPLE.com", []string{"", ".example.com"}) {
		t.Fatal("dot domain")
	}
}
