package netproxy

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

var envNames = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"}

// setup gives the test empty settings, an empty environment and the given
// system proxy.
func setup(t *testing.T, sys sysProxy) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, n := range envNames {
		t.Setenv(n, "")
		os.Unsetenv(n)
	}
	old := systemProxyFunc
	systemProxyFunc = func() sysProxy { return sys }
	t.Cleanup(func() { systemProxyFunc = old })
}

func setProxy(t *testing.T, v string) {
	t.Helper()
	if err := settings.Save(settings.Settings{Proxy: v}); err != nil {
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

func proxyOf(t *testing.T, target string) string {
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

func sys(t *testing.T, proxy string, exceptions ...string) sysProxy {
	return sysProxy{URL: mustURL(t, proxy), Exceptions: exceptions}
}

func TestSystemProxy(t *testing.T) {
	setup(t, sys(t, "http://127.0.0.1:7890", "*.corp"))
	if got := proxyOf(t, "https://chatgpt.com/backend-api/x"); got != "http://127.0.0.1:7890" {
		t.Fatalf("got %q", got)
	}
	if p, s := Describe(); p != "http://127.0.0.1:7890" || s != "system" {
		t.Fatalf("Describe = %q %q", p, s)
	}
	if got := proxyOf(t, "https://git.corp/x"); got != "" {
		t.Fatalf("exception not honoured: %q", got)
	}
	if got := proxyOf(t, "https://corp/x"); got != "" {
		t.Fatalf("*.corp covers corp: %q", got)
	}
	if got := proxyOf(t, "https://api.anthropic.com/v1"); got == "" {
		t.Fatal("non-excepted host went direct")
	}
}

func TestSettingsWin(t *testing.T) {
	setup(t, sys(t, "http://127.0.0.1:7890"))
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:3128")
	setProxy(t, "  socks5://127.0.0.1:1080 ")
	if got := proxyOf(t, "https://chatgpt.com/x"); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("got %q", got)
	}
	if p, s := Describe(); p != "socks5://127.0.0.1:1080" || s != "settings" {
		t.Fatalf("Describe = %q %q", p, s)
	}
	// a change counts for the next request, no restart
	setProxy(t, "127.0.0.1:7891")
	if got := proxyOf(t, "https://chatgpt.com/x"); got != "http://127.0.0.1:7891" {
		t.Fatalf("bare host:port: %q", got)
	}
}

func TestDirect(t *testing.T) {
	setup(t, sys(t, "http://127.0.0.1:7890"))
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:3128")
	t.Setenv("ALL_PROXY", "socks5://10.0.0.1:1080")
	setProxy(t, "direct")
	if got := proxyOf(t, "https://chatgpt.com/x"); got != "" {
		t.Fatalf("got %q", got)
	}
	if p, s := Describe(); p != "" || s != "off" {
		t.Fatalf("Describe = %q %q", p, s)
	}
}

func TestEnvironmentBeforeSystem(t *testing.T) {
	setup(t, sys(t, "http://127.0.0.1:7890"))
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:3128")
	if got := proxyOf(t, "https://chatgpt.com/x"); got != "http://10.0.0.1:3128" {
		t.Fatalf("got %q", got)
	}
	if p, s := Describe(); p != "http://10.0.0.1:3128" || s != "environment" {
		t.Fatalf("Describe = %q %q", p, s)
	}
	t.Setenv("NO_PROXY", "example.com,.internal")
	if got := proxyOf(t, "https://api.example.com/x"); got != "" {
		t.Fatalf("NO_PROXY ignored: %q", got)
	}
	if got := proxyOf(t, "https://example.com/x"); got != "" {
		t.Fatalf("NO_PROXY ignored: %q", got)
	}
	if got := proxyOf(t, "https://chatgpt.com/x"); got != "http://10.0.0.1:3128" {
		t.Fatalf("got %q", got)
	}
	// lower case, and no scheme means http
	os.Unsetenv("HTTPS_PROXY")
	t.Setenv("https_proxy", "10.0.0.2:3128")
	if got := proxyOf(t, "https://chatgpt.com/x"); got != "http://10.0.0.2:3128" {
		t.Fatalf("got %q", got)
	}
	// ALL_PROXY fills in
	os.Unsetenv("https_proxy")
	t.Setenv("ALL_PROXY", "socks5h://10.0.0.3:1080")
	if got := proxyOf(t, "https://chatgpt.com/x"); got != "socks5h://10.0.0.3:1080" {
		t.Fatalf("got %q", got)
	}
}

func TestLoopbackNeverProxied(t *testing.T) {
	targets := []string{"http://127.0.0.1:3425/v1", "http://localhost/x", "http://LOCALHOST:8080/", "http://app.localhost/", "http://[::1]:3425/", "http://127.1.2.3/"}
	check := func(t *testing.T) {
		t.Helper()
		for _, target := range targets {
			if got := proxyOf(t, target); got != "" {
				t.Errorf("%s went through %q", target, got)
			}
		}
	}
	t.Run("system", func(t *testing.T) {
		setup(t, sys(t, "http://127.0.0.1:7890"))
		check(t)
	})
	t.Run("environment", func(t *testing.T) {
		setup(t, sysProxy{})
		t.Setenv("HTTPS_PROXY", "http://10.0.0.1:3128")
		t.Setenv("HTTP_PROXY", "http://10.0.0.1:3128")
		check(t)
	})
	t.Run("settings", func(t *testing.T) {
		setup(t, sysProxy{})
		setProxy(t, "socks5://127.0.0.1:1080")
		check(t)
	})
	t.Run("broken settings", func(t *testing.T) {
		setup(t, sysProxy{})
		writeRaw(t, `{"proxy":"ftp://x"}`)
		check(t)
	})
}

func TestNothing(t *testing.T) {
	setup(t, sysProxy{})
	if got := proxyOf(t, "https://chatgpt.com/x"); got != "" {
		t.Fatalf("got %q", got)
	}
	if p, s := Describe(); p != "" || s != "none" {
		t.Fatalf("Describe = %q %q", p, s)
	}
}

func TestNoSystemProxyElsewhere(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("reads the real system proxy")
	}
	setup(t, sysProxy{})
	systemProxyFunc = cachedSystemProxy
	if p, err := readSystemProxy(); err != nil || p.URL != nil {
		t.Fatalf("%v %v", p.URL, err)
	}
	{
		if got := proxyOf(t, "https://chatgpt.com/x"); got != "" {
			t.Fatalf("got %q", got)
		}
		if _, s := Describe(); s != "none" {
			t.Fatalf("source %q", s)
		}
	}
}

func writeRaw(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.Dir(), "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBrokenSettingIsAnError(t *testing.T) {
	setup(t, sys(t, "http://127.0.0.1:7890"))
	writeRaw(t, `{"proxy":"ftp://x"}`)
	if p, err := For(mustURL(t, "https://chatgpt.com/x")); err == nil {
		t.Fatalf("broken setting gave %v and no error", p)
	}
	if _, s := Describe(); s != "settings" {
		t.Fatalf("source %q", s)
	}
}

func TestInstall(t *testing.T) {
	setup(t, sys(t, "http://127.0.0.1:7890"))
	Install()
	Install()
	tr := http.DefaultTransport.(*http.Transport)
	req, _ := http.NewRequest("GET", "https://chatgpt.com/", nil)
	p, err := tr.Proxy(req)
	if err != nil || p == nil || p.String() != "http://127.0.0.1:7890" {
		t.Fatalf("default transport: %v %v", p, err)
	}
	req, _ = http.NewRequest("GET", "http://127.0.0.1:3425/", nil)
	if p, _ := tr.Proxy(req); p != nil {
		t.Fatalf("loopback proxied: %v", p)
	}
}

func TestBypassed(t *testing.T) {
	ex := []string{"", " ", "*.corp", ".example.com", "<local>", "Exact.Host", "192.168.*"}
	for host, want := range map[string]bool{
		"git.corp":        true,
		"a.b.corp":        true,
		"corp":            true,
		"example.com":     true,
		"www.example.com": true,
		"notexample.com":  false,
		"intranet":        true,
		"exact.host":      true,
		"EXACT.HOST":      true,
		"sub.exact.host":  false,
		"192.168.1.2":     true,
		"chatgpt.com":     false,
	} {
		if got := bypassed(host, ex); got != want {
			t.Errorf("bypassed(%q) = %v", host, got)
		}
	}
	if bypassed("chatgpt.com", nil) {
		t.Error("empty list bypasses")
	}
}

const scutilAll = `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
    2 : *.corp
  }
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7891
  HTTPSProxy : 127.0.0.2
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.3
}
`

func TestParseScutil(t *testing.T) {
	p := parseScutil(scutilAll)
	if p.URL == nil || p.URL.String() != "http://127.0.0.2:7891" {
		t.Fatalf("HTTPS first: %v", p.URL)
	}
	if len(p.Exceptions) != 3 || p.Exceptions[2] != "*.corp" {
		t.Fatalf("exceptions %q", p.Exceptions)
	}
	p = parseScutil(`<dictionary> {
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 0
  HTTPSPort : 7891
  HTTPSProxy : 127.0.0.2
}`)
	if p.URL == nil || p.URL.String() != "http://127.0.0.1:7890" {
		t.Fatalf("HTTP next: %v", p.URL)
	}
	p = parseScutil(`<dictionary> {
  HTTPEnable : 0
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.3
}`)
	if p.URL == nil || p.URL.String() != "socks5://127.0.0.3:1080" {
		t.Fatalf("SOCKS only: %v", p.URL)
	}
	p = parseScutil(`<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
  }
  HTTPEnable : 0
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 0
  HTTPSProxy : 127.0.0.2
  SOCKSEnable : 0
  SOCKSProxy : 127.0.0.3
}`)
	if p.URL != nil {
		t.Fatalf("nothing enabled: %v", p.URL)
	}
	if p := parseScutil(""); p.URL != nil {
		t.Fatal("empty output")
	}
	p = parseScutil(`<dictionary> {
  ExcludeSimpleHostnames : 1
  HTTPSEnable : 1
  HTTPSPort : 8080
  HTTPSProxy : proxy.lan
}`)
	if p.URL.String() != "http://proxy.lan:8080" || !bypassed("intranet", p.Exceptions) {
		t.Fatalf("%v %q", p.URL, p.Exceptions)
	}
}

func TestParseWindows(t *testing.T) {
	for _, c := range []struct {
		enabled bool
		server  string
		want    string
	}{
		{true, "127.0.0.1:7890", "http://127.0.0.1:7890"},
		{true, "http=127.0.0.1:7890;https=127.0.0.1:7891", "http://127.0.0.1:7891"},
		{true, "http=127.0.0.1:7890;https=", "http://127.0.0.1:7890"},
		{true, "socks=127.0.0.1:1080", "socks5://127.0.0.1:1080"},
		{true, "ftp=1.2.3.4:21;socks=127.0.0.1:1080;http=127.0.0.1:7890", "http://127.0.0.1:7890"},
		{true, "", ""},
		{false, "127.0.0.1:7890", ""},
	} {
		got := ""
		if p := parseWindows(c.enabled, c.server, ""); p.URL != nil {
			got = p.URL.String()
		}
		if got != c.want {
			t.Errorf("parseWindows(%v, %q) = %q, want %q", c.enabled, c.server, got, c.want)
		}
	}

	p := parseWindows(true, "127.0.0.1:7890", "*.corp;<local>")
	setup(t, p)
	for target, want := range map[string]string{
		"https://git.corp/x":    "",
		"https://intranet/x":    "",
		"https://chatgpt.com/x": "http://127.0.0.1:7890",
	} {
		if got := proxyOf(t, target); got != want {
			t.Errorf("%s: %q, want %q", target, got, want)
		}
	}
}

func TestSystemCache(t *testing.T) {
	sysCache.Lock()
	sysCache.at, sysCache.val = time.Now(), sysProxy{URL: mustURL(t, "http://cached:1")}
	sysCache.Unlock()
	t.Cleanup(func() {
		sysCache.Lock()
		sysCache.at = sysCache.at.AddDate(-1, 0, 0)
		sysCache.Unlock()
	})
	if got := cachedSystemProxy(); got.URL == nil || got.URL.Host != "cached:1" {
		t.Fatalf("cache not used: %v", got.URL)
	}
	if sysTTL > 15e9 {
		t.Fatal("system proxy changes must count within 15 seconds")
	}
}
