package netproxy

import (
	"net/url"
	"strings"
	"testing"
)

func setup(t *testing.T, setting string, env map[string]string, sys *sysProxy) {
	t.Helper()
	oldS, oldE, oldO, oldG := settingValue, getenv, systemOverride, goos
	t.Cleanup(func() { settingValue, getenv, systemOverride, goos = oldS, oldE, oldO, oldG; ResetSystemCache() })
	settingValue = func() string { return setting }
	getenv = func(k string) string { return env[k] }
	systemOverride = func() *sysProxy { return sys }
}

func forStr(t *testing.T, s string) string {
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

func sysP(exc ...string) *sysProxy {
	u, _ := url.Parse("http://127.0.0.1:7890")
	return &sysProxy{URL: u, Exceptions: exc}
}

func TestSystem(t *testing.T) {
	setup(t, "", nil, sysP("*.corp"))
	if g := forStr(t, "https://chatgpt.com/x"); g != "http://127.0.0.1:7890" {
		t.Fatal(g)
	}
	if g := forStr(t, "https://git.corp/x"); g != "" {
		t.Fatal(g)
	}
	if p, s := Describe(); s != "system" || p != "http://127.0.0.1:7890" {
		t.Fatal(p, s)
	}
}

func TestSettingsWinAndDirect(t *testing.T) {
	env := map[string]string{"HTTPS_PROXY": "http://1.2.3.4:1"}
	setup(t, "socks5://127.0.0.1:1080", env, sysP())
	if g := forStr(t, "https://api.openai.com"); g != "socks5://127.0.0.1:1080" {
		t.Fatal(g)
	}
	if _, s := Describe(); s != "settings" {
		t.Fatal(s)
	}
	if g := forStr(t, "http://localhost:3425/x"); g != "" {
		t.Fatal(g)
	}
	for _, h := range []string{"http://127.0.0.1:3425", "http://[::1]:80", "http://a.localhost"} {
		if g := forStr(t, h); g != "" {
			t.Fatal(h, g)
		}
	}
	setup(t, "direct", env, sysP())
	if g := forStr(t, "https://api.openai.com"); g != "" {
		t.Fatal(g)
	}
	if p, s := Describe(); s != "off" || p != "" {
		t.Fatal(p, s)
	}
	setup(t, "127.0.0.1:7890", nil, nil)
	if g := forStr(t, "https://x.com"); g != "http://127.0.0.1:7890" {
		t.Fatal(g)
	}
}

func TestBadSettingErrors(t *testing.T) {
	setup(t, "ftp://x", nil, nil)
	u, _ := url.Parse("https://x.com")
	if p, err := For(u); err == nil || p != nil {
		t.Fatal("want error")
	}
}

func TestEnvBeatsSystem(t *testing.T) {
	setup(t, "", map[string]string{"https_proxy": "http://9.9.9.9:8"}, sysP())
	if g := forStr(t, "https://x.com"); g != "http://9.9.9.9:8" {
		t.Fatal(g)
	}
	if p, s := Describe(); s != "environment" || p != "http://9.9.9.9:8" {
		t.Fatal(p, s)
	}
	setup(t, "", map[string]string{"ALL_PROXY": "socks5://h:1", "NO_PROXY": "x.com"}, sysP())
	if g := forStr(t, "https://y.com"); g != "socks5://h:1" {
		t.Fatal(g)
	}
	if g := forStr(t, "https://a.x.com"); g != "" {
		t.Fatal(g)
	}
}

func TestNone(t *testing.T) {
	setup(t, "", nil, nil)
	if p, s := Describe(); s != "none" || p != "" {
		t.Fatal(p, s)
	}
	// unsupported OS
	systemOverride = nil
	goos = "linux"
	ResetSystemCache()
	if g := forStr(t, "https://x.com"); g != "" {
		t.Fatal(g)
	}
	if _, s := Describe(); s != "none" {
		t.Fatal(s)
	}
}

func TestReadSystemViaCommand(t *testing.T) {
	setup(t, "", nil, nil)
	systemOverride = nil
	old := runCommand
	defer func() { runCommand = old }()
	goos = "darwin"
	runCommand = func(string, ...string) ([]byte, error) {
		return []byte("<dictionary> {\n  HTTPEnable : 1\n  HTTPPort : 8080\n  HTTPProxy : p.example\n}\n"), nil
	}
	ResetSystemCache()
	if p, s := Describe(); s != "system" || p != "http://p.example:8080" {
		t.Fatal(p, s)
	}
	runCommand = func(string, ...string) ([]byte, error) { return nil, errTest }
	ResetSystemCache()
	if _, s := Describe(); s != "none" {
		t.Fatal(s)
	}
}

type tErr string

func (e tErr) Error() string { return string(e) }

const errTest = tErr("boom")

func TestScutil(t *testing.T) {
	all := `<dictionary> {
  ExceptionsList : <array> {
    0 : *.corp
    1 : <local>
  }
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : h.example
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : hs.example
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : s.example
}`
	p := parseScutil(all)
	if p == nil || p.URL.String() != "http://hs.example:8443" || len(p.Exceptions) != 2 {
		t.Fatalf("%+v", p)
	}
	only := strings.Replace(strings.Replace(all, "HTTPEnable : 1", "HTTPEnable : 0", 1), "HTTPSEnable : 1", "HTTPSEnable : 0", 1)
	if p := parseScutil(only); p == nil || p.URL.String() != "socks5://s.example:1080" {
		t.Fatalf("%+v", p)
	}
	none := strings.ReplaceAll(all, "Enable : 1", "Enable : 0")
	if parseScutil(none) != nil {
		t.Fatal("want nil")
	}
	if parseScutil("") != nil {
		t.Fatal("want nil")
	}
}

func reg(server, override string) string {
	return "HKEY_CURRENT_USER\\Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings\r\n" +
		"    ProxyEnable    REG_DWORD    0x1\r\n" +
		"    ProxyServer    REG_SZ    " + server + "\r\n" +
		"    ProxyOverride    REG_SZ    " + override + "\r\n"
}

func TestWindows(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:7890":                   "http://127.0.0.1:7890",
		"http=a:1;https=b:2":               "http://b:2",
		"http=a:1":                         "http://a:1",
		"socks=127.0.0.1:1080":             "socks5://127.0.0.1:1080",
		"http=;socks=127.0.0.1:1080":       "socks5://127.0.0.1:1080",
		"https=https://secure.example:443": "https://secure.example:443",
	}
	for in, want := range cases {
		p := parseWindowsRegistry(reg(in, "*.corp;<local>"))
		if p == nil || p.URL.String() != want {
			t.Fatalf("%s: %+v", in, p)
		}
	}
	p := parseWindowsRegistry(reg("127.0.0.1:7890", "*.corp;<local>;;"))
	if !p.bypass("git.corp") || !p.bypass("intranet") || p.bypass("chatgpt.com") || !p.bypass("corp") {
		t.Fatal("exceptions")
	}
	off := strings.Replace(reg("1.1.1.1:1", ""), "0x1", "0x0", 1)
	if parseWindowsRegistry(off) != nil {
		t.Fatal("want nil")
	}
}

func TestMatchException(t *testing.T) {
	l := []string{"Example.COM", ".foo.org", ""}
	for h, want := range map[string]bool{"example.com": true, "a.example.com": false, "foo.org": true, "x.foo.org": true, "bar.org": false} {
		if matchException(l, h) != want {
			t.Fatal(h)
		}
	}
}
