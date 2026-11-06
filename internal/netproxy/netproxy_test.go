package netproxy

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

var proxyEnvNames = []string{
	"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy",
	"ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy",
}

// setup isolates the settings file and environment, and replaces the OS
// proxy reading with sys. The cache is cleared before and after.
func setup(t *testing.T, sys sysProxy) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, n := range proxyEnvNames {
		t.Setenv(n, "")
	}
	old := readSystemProxy
	readSystemProxy = func() sysProxy { return sys }
	resetCache()
	t.Cleanup(func() {
		readSystemProxy = old
		resetCache()
	})
}

func resetCache() {
	sysMu.Lock()
	sysAt = time.Time{}
	sysMu.Unlock()
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

func setSetting(t *testing.T, v string) {
	t.Helper()
	if err := settings.Save(settings.Settings{Proxy: v}); err != nil {
		t.Fatal(err)
	}
}

var sysHTTP = sysProxy{URL: "http://127.0.0.1:7890"}

// 验收 1
func TestSystemProxyUsed(t *testing.T) {
	setup(t, sysHTTP)
	if got := forString(t, "https://chatgpt.com/backend"); got != "http://127.0.0.1:7890" {
		t.Fatalf("For = %q", got)
	}
	if p, s := Describe(); p != "http://127.0.0.1:7890" || s != "system" {
		t.Fatalf("Describe = %q, %q", p, s)
	}
}

// 验收 2
func TestSystemExceptions(t *testing.T) {
	setup(t, sysProxy{URL: "http://127.0.0.1:7890", Except: []string{"*.corp"}})
	if got := forString(t, "https://git.corp/x"); got != "" {
		t.Fatalf("git.corp should be direct, got %q", got)
	}
	if got := forString(t, "https://chatgpt.com/"); got != "http://127.0.0.1:7890" {
		t.Fatalf("chatgpt.com For = %q", got)
	}
}

// 验收 3
func TestSettingsSocksWins(t *testing.T) {
	setup(t, sysHTTP)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9999")
	setSetting(t, "socks5://127.0.0.1:1080")
	if got := forString(t, "https://api.openai.com/"); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("For = %q", got)
	}
	if p, s := Describe(); p != "socks5://127.0.0.1:1080" || s != "settings" {
		t.Fatalf("Describe = %q, %q", p, s)
	}
}

// 验收 4
func TestSettingsDirect(t *testing.T) {
	setup(t, sysHTTP)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9999")
	setSetting(t, "direct")
	if got := forString(t, "https://api.openai.com/"); got != "" {
		t.Fatalf("For = %q, want direct", got)
	}
	if p, s := Describe(); p != "" || s != "off" {
		t.Fatalf("Describe = %q, %q", p, s)
	}
}

// 验收 5
func TestEnvironmentBeforeSystem(t *testing.T) {
	setup(t, sysHTTP)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:8888")
	if got := forString(t, "https://api.openai.com/"); got != "http://127.0.0.1:8888" {
		t.Fatalf("For = %q", got)
	}
	if p, s := Describe(); p != "http://127.0.0.1:8888" || s != "environment" {
		t.Fatalf("Describe = %q, %q", p, s)
	}
}

func TestEnvironmentNoProxy(t *testing.T) {
	setup(t, sysHTTP)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:8888")
	t.Setenv("NO_PROXY", "example.com,.corp")
	if got := forString(t, "https://api.example.com/"); got != "" {
		t.Fatalf("NO_PROXY should exclude api.example.com, got %q", got)
	}
	if got := forString(t, "https://api.other.net/"); got != "http://127.0.0.1:8888" {
		t.Fatalf("other host For = %q", got)
	}
}

func TestEnvironmentSchemeSelection(t *testing.T) {
	setup(t, sysProxy{})
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:8001")
	if got := forString(t, "http://api.example.net/"); got != "http://127.0.0.1:8001" {
		t.Fatalf("http For = %q", got)
	}
	// Like net/http, HTTP_PROXY does not apply to https.
	if got := forString(t, "https://api.example.net/"); got != "" {
		t.Fatalf("https For = %q, want direct", got)
	}
}

// 验收 6
func TestLoopbackAlwaysDirect(t *testing.T) {
	setup(t, sysHTTP)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:8888")
	setSetting(t, "socks5://127.0.0.1:1080")
	for _, target := range []string{
		"http://127.0.0.1:3425/x", "http://localhost:3425", "http://api.localhost/",
		"http://[::1]:3425/", "http://127.9.9.9/",
	} {
		if got := forString(t, target); got != "" {
			t.Fatalf("%s For = %q, want direct", target, got)
		}
	}
}

// 验收 10
func TestNoProxyAnywhere(t *testing.T) {
	setup(t, sysProxy{})
	if got := forString(t, "https://chatgpt.com/"); got != "" {
		t.Fatalf("For = %q, want direct", got)
	}
	if p, s := Describe(); p != "" || s != "none" {
		t.Fatalf("Describe = %q, %q", p, s)
	}
}

func TestBadSettingsIsError(t *testing.T) {
	setup(t, sysHTTP)
	dir := filepath.Dir(settings.Path())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.Path(), []byte(`{"proxy":"ftp://x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := For(mustURL(t, "https://chatgpt.com/")); err == nil {
		t.Fatal("bad settings proxy must be an error, not direct")
	}
}

func TestSettingsChangeTakesEffectWithoutRestart(t *testing.T) {
	setup(t, sysHTTP)
	if got := forString(t, "https://chatgpt.com/"); got != "http://127.0.0.1:7890" {
		t.Fatalf("before: %q", got)
	}
	setSetting(t, "direct")
	if got := forString(t, "https://chatgpt.com/"); got != "" {
		t.Fatalf("after direct: %q", got)
	}
	setSetting(t, "127.0.0.1:7891")
	if got := forString(t, "https://chatgpt.com/"); got != "http://127.0.0.1:7891" {
		t.Fatalf("after address: %q", got)
	}
}

func TestSystemCacheExpires(t *testing.T) {
	setup(t, sysHTTP)
	calls := 0
	readSystemProxy = func() sysProxy { calls++; return sysHTTP }
	forString(t, "https://a.example/")
	forString(t, "https://a.example/")
	if calls != 1 {
		t.Fatalf("expected cache hit, read %d times", calls)
	}
	sysMu.Lock()
	sysAt = time.Now().Add(-sysTTL - time.Second)
	sysMu.Unlock()
	forString(t, "https://a.example/")
	if calls != 2 {
		t.Fatalf("expected re-read after TTL, read %d times", calls)
	}
}

func TestInstallSetsDefaultTransport(t *testing.T) {
	setup(t, sysHTTP)
	Install()
	// The transport is stored globally by net/http; this only checks that
	// Install does not panic and that For is consistent with it.
	if got := forString(t, "https://chatgpt.com/"); got != "http://127.0.0.1:7890" {
		t.Fatalf("For = %q", got)
	}
}

func TestParseScutil(t *testing.T) {
	const all = `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
  }
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : 10.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	p, ex := parseScutil(all)
	if p != "http://127.0.0.1:7890" {
		t.Fatalf("https first: %q", p)
	}
	if len(ex) != 2 || ex[0] != "*.local" {
		t.Fatalf("exceptions: %v", ex)
	}

	onlySocks := `<dictionary> {
  HTTPEnable : 0
  HTTPSEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	if p, _ := parseScutil(onlySocks); p != "socks5://127.0.0.1:1080" {
		t.Fatalf("socks only: %q", p)
	}

	none := `<dictionary> {
  HTTPEnable : 0
  HTTPProxy : 10.0.0.1
  HTTPPort : 8080
  HTTPSEnable : 0
  HTTPSProxy : 10.0.0.1
  HTTPSPort : 8080
  SOCKSEnable : 0
  SOCKSProxy : 10.0.0.1
  SOCKSPort : 1080
}`
	if p, _ := parseScutil(none); p != "" {
		t.Fatalf("none enabled: %q", p)
	}
}

func TestParseWinProxyServer(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:7890":                        "http://127.0.0.1:7890",
		"http=10.0.0.1:80;https=127.0.0.1:7890": "http://127.0.0.1:7890",
		"http=10.0.0.1:80":                      "http://10.0.0.1:80",
		"socks=127.0.0.1:1080":                  "socks5://127.0.0.1:1080",
		"ftp=10.0.0.1:21;socks=127.0.0.1:1080":  "socks5://127.0.0.1:1080",
		"":                                      "",
		"https=;http=;socks=":                   "",
	}
	for in, want := range cases {
		if got := parseWinProxyServer(in); got != want {
			t.Errorf("parseWinProxyServer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWindowsOverrideBypass(t *testing.T) {
	list := splitList("*.corp;<local>", ";")
	if !matchBypass("git.corp", list) {
		t.Fatal("git.corp should bypass")
	}
	if !matchBypass("intranet", list) {
		t.Fatal("intranet should bypass")
	}
	if matchBypass("chatgpt.com", list) {
		t.Fatal("chatgpt.com should not bypass")
	}
}

func TestMatchBypass(t *testing.T) {
	cases := []struct {
		host string
		list []string
		want bool
	}{
		{"Example.COM", []string{"example.com"}, true},
		{"example.com", []string{"EXAMPLE.com"}, true},
		{"a.example.com", []string{"*.example.com"}, true},
		{"example.com", []string{"*.example.com"}, true},
		{"a.example.com", []string{".example.com"}, true},
		{"badexample.com", []string{"*.example.com"}, false},
		{"intranet", []string{"<local>"}, true},
		{"intranet.corp", []string{"<local>"}, false},
		{"x.com", []string{"", "  "}, false},
	}
	for _, c := range cases {
		if got := matchBypass(c.host, c.list); got != c.want {
			t.Errorf("matchBypass(%q, %v) = %v, want %v", c.host, c.list, got, c.want)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	for _, h := range []string{"localhost", "a.localhost", "127.0.0.1", "127.1.2.3", "::1"} {
		if !isLoopback(h) {
			t.Errorf("%s should be loopback", h)
		}
	}
	for _, h := range []string{"example.com", "10.0.0.1", "notlocalhost"} {
		if isLoopback(h) {
			t.Errorf("%s should not be loopback", h)
		}
	}
}
