package netproxy

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

func setup(t *testing.T, sys Proxy) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	old := readSys
	readSys = func() Proxy { return sys }
	mu.Lock()
	readAt = time.Time{}
	mu.Unlock()
	t.Cleanup(func() {
		readSys = old
		mu.Lock()
		readAt = time.Time{}
		mu.Unlock()
	})
}

func forStr(t *testing.T, raw string) string {
	t.Helper()
	u, _ := url.Parse(raw)
	p, err := For(u)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

func TestPriority(t *testing.T) {
	setup(t, Proxy{})
	if got := forStr(t, "https://chatgpt.com/x"); got != "" {
		t.Fatal(got)
	}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9999")
	t.Setenv("NO_PROXY", "skip.example")
	if got := forStr(t, "https://chatgpt.com/x"); got != "http://127.0.0.1:9999" {
		t.Fatal(got)
	}
	if got := forStr(t, "https://a.skip.example/"); got != "" {
		t.Fatal(got)
	}
	if err := settings.Save(settings.Settings{Proxy: " socks5://127.0.0.1:1080 "}); err != nil {
		t.Fatal(err)
	}
	if got := forStr(t, "https://chatgpt.com/x"); got != "socks5://127.0.0.1:1080" {
		t.Fatal(got)
	}
	if p, s := Describe(); p != "socks5://127.0.0.1:1080" || s != "settings" {
		t.Fatal(p, s)
	}
	settings.Save(settings.Settings{Proxy: "direct"})
	if got := forStr(t, "https://chatgpt.com/x"); got != "" {
		t.Fatal(got)
	}
	if p, s := Describe(); p != "" || s != "off" {
		t.Fatal(p, s)
	}
	settings.Save(settings.Settings{Proxy: "127.0.0.1:7890"})
	if got := forStr(t, "https://chatgpt.com/x"); got != "http://127.0.0.1:7890" {
		t.Fatal(got)
	}
}

func TestEnvAndSystemDescribe(t *testing.T) {
	setup(t, Proxy{})
	if p, s := Describe(); p != "" || s != "none" {
		t.Fatal(p, s)
	}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9999")
	if p, s := Describe(); p != "http://127.0.0.1:9999" || s != "environment" {
		t.Fatal(p, s)
	}
}

func TestSystem(t *testing.T) {
	setup(t, Proxy{URL: "http://10.0.0.1:3128", Bypass: []string{"*.corp", "<local>"}})
	if got := forStr(t, "https://chatgpt.com/"); got != "http://10.0.0.1:3128" {
		t.Fatal(got)
	}
	if got := forStr(t, "https://git.corp/"); got != "" {
		t.Fatal(got)
	}
	if got := forStr(t, "http://intranet/"); got != "" {
		t.Fatal(got)
	}
	if p, s := Describe(); p != "http://10.0.0.1:3128" || s != "system" {
		t.Fatal(p, s)
	}
}

func TestFuncLoopback(t *testing.T) {
	setup(t, Proxy{})
	settings.Save(settings.Settings{Proxy: "127.0.0.1:7890"})
	for _, u := range []string{"http://127.0.0.1:3425", "http://localhost:1/", "http://[::1]:5/", "http://foo.localhost/"} {
		req, _ := http.NewRequest("GET", u, nil)
		if p, err := Func(req); p != nil || err != nil {
			t.Fatal(u, p, err)
		}
	}
	req, _ := http.NewRequest("GET", "https://chatgpt.com/", nil)
	if p, _ := Func(req); p == nil || p.String() != "http://127.0.0.1:7890" {
		t.Fatal(p)
	}
}

func TestInstall(t *testing.T) {
	old := http.DefaultTransport.(*http.Transport).Proxy
	defer func() { http.DefaultTransport.(*http.Transport).Proxy = old }()
	Install()
	req, _ := http.NewRequest("GET", "http://localhost/", nil)
	if p, err := http.DefaultTransport.(*http.Transport).Proxy(req); p != nil || err != nil {
		t.Fatal(p, err)
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]string{"1.2.3.4:8080": "http://1.2.3.4:8080", " socks5://h:1 ": "socks5://h:1"} {
		if u, err := Parse(in); err != nil || u.String() != want {
			t.Fatal(in, u, err)
		}
	}
}

func TestScutil(t *testing.T) {
	out := `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
  }
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : h.example
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : s.example
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : k.example
}`
	p := parseScutil(out)
	if p.URL != "http://s.example:8443" || len(p.Bypass) != 2 || p.Bypass[0] != "*.local" {
		t.Fatalf("%+v", p)
	}
	p = parseScutil("<dictionary> {\n  HTTPEnable : 0\n  HTTPPort : 1\n  HTTPProxy : h\n  SOCKSEnable : 1\n  SOCKSPort : 1080\n  SOCKSProxy : k\n}")
	if p.URL != "socks5://k:1080" {
		t.Fatalf("%+v", p)
	}
	if p = parseScutil("HTTPEnable : 0\nHTTPProxy : h\nHTTPPort : 1"); p.URL != "" {
		t.Fatalf("%+v", p)
	}
}

func TestWindows(t *testing.T) {
	if p := parseWindows("h:80", "", true); p.URL != "http://h:80" {
		t.Fatal(p)
	}
	if p := parseWindows("h:80", "", false); p.URL != "" {
		t.Fatal(p)
	}
	p := parseWindows("http=a:1;https=b:2;socks=c:3", "*.corp;<local>;", true)
	if p.URL != "http://b:2" || len(p.Bypass) != 2 {
		t.Fatal(p)
	}
	if p = parseWindows("socks=c:3", "", true); p.URL != "socks5://c:3" {
		t.Fatal(p)
	}
	b := []string{"*.corp", "<local>"}
	if !bypassed("git.corp", b) || !bypassed("intranet", b) || bypassed("chatgpt.com", b) {
		t.Fatal("bypass")
	}
	if !bypassed("CORP", []string{".corp"}) || !bypassed("EXAMPLE.com", []string{"example.com"}) || bypassed("x", []string{"", " "}) {
		t.Fatal("bypass2")
	}
}
