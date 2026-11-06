package netproxy

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

var envNames = []string{
	"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy",
	"ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy",
}

// setup clears every proxy variable, points settings at a temporary folder,
// and makes the operating system report no proxy.
func setup(t *testing.T) {
	t.Helper()
	for _, n := range envNames {
		t.Setenv(n, "")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	setSystem(t, sysConfig{})
	t.Cleanup(func() {
		readSystemProxy = readSystem
		resetSystemCache()
	})
}

// setSystem replaces the operating system proxy read.
func setSystem(t *testing.T, cfg sysConfig) {
	t.Helper()
	readSystemProxy = func() sysConfig { return cfg }
	resetSystemCache()
}

func writeSettings(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.Path(), []byte(body), 0o644); err != nil {
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

// expectProxy checks For(target) returns want ("" means direct).
func expectProxy(t *testing.T, target, want string) {
	t.Helper()
	got, err := For(mustURL(t, target))
	if err != nil {
		t.Fatalf("For(%s): %v", target, err)
	}
	gotS := ""
	if got != nil {
		gotS = got.String()
	}
	if gotS != want {
		t.Fatalf("For(%s) = %q, want %q", target, gotS, want)
	}
}

func expectDescribe(t *testing.T, wantProxy, wantSource string) {
	t.Helper()
	p, s := Describe()
	if p != wantProxy || s != wantSource {
		t.Fatalf("Describe() = (%q, %q), want (%q, %q)", p, s, wantProxy, wantSource)
	}
}

// Acceptance 1: empty settings, no env, system proxy.
func TestSystemProxyUsed(t *testing.T) {
	setup(t)
	setSystem(t, sysConfig{proxy: "http://127.0.0.1:7890"})
	expectProxy(t, "https://chatgpt.com/backend", "http://127.0.0.1:7890")
	expectDescribe(t, "http://127.0.0.1:7890", "system")
}

// Acceptance 2: system exception list.
func TestSystemExceptions(t *testing.T) {
	setup(t)
	setSystem(t, sysConfig{proxy: "http://127.0.0.1:7890", exceptions: []string{"*.corp"}})
	expectProxy(t, "https://git.corp/x", "")
	expectProxy(t, "https://corp/x", "")
	expectProxy(t, "https://chatgpt.com/x", "http://127.0.0.1:7890")
	expectDescribe(t, "http://127.0.0.1:7890", "system")
}

// Acceptance 3: settings beat environment and system.
func TestSettingsWins(t *testing.T) {
	setup(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	setSystem(t, sysConfig{proxy: "http://127.0.0.1:7890"})
	writeSettings(t, `{"proxy":"socks5://127.0.0.1:1080"}`)
	expectProxy(t, "https://api.openai.com/v1", "socks5://127.0.0.1:1080")
	expectDescribe(t, "socks5://127.0.0.1:1080", "settings")
}

// Acceptance 4: "direct" turns every proxy off.
func TestSettingsDirect(t *testing.T) {
	setup(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	setSystem(t, sysConfig{proxy: "http://127.0.0.1:7890"})
	writeSettings(t, `{"proxy":"direct"}`)
	expectProxy(t, "https://api.openai.com/v1", "")
	expectDescribe(t, "", "off")
}

// Acceptance 5: environment beats system.
func TestEnvironmentBeforeSystem(t *testing.T) {
	setup(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	setSystem(t, sysConfig{proxy: "http://127.0.0.1:7890"})
	expectProxy(t, "https://api.openai.com/v1", "http://env.example:3128")
	expectDescribe(t, "http://env.example:3128", "environment")
}

// NO_PROXY from the environment keeps working with the environment proxy.
func TestEnvironmentNoProxy(t *testing.T) {
	setup(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	t.Setenv("NO_PROXY", "intranet.lan,.internal")
	setSystem(t, sysConfig{proxy: "http://127.0.0.1:7890"})
	expectProxy(t, "https://intranet.lan/x", "")
	expectProxy(t, "https://a.internal/x", "")
	expectProxy(t, "https://api.openai.com/x", "http://env.example:3128")
}

// Acceptance 6: loopback is always direct, at every level.
func TestLoopbackAlwaysDirect(t *testing.T) {
	setup(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	t.Setenv("HTTP_PROXY", "http://env.example:3128")
	setSystem(t, sysConfig{proxy: "http://127.0.0.1:7890"})
	writeSettings(t, `{"proxy":"socks5://127.0.0.1:1080"}`)
	for _, target := range []string{
		"http://127.0.0.1:3425/x",
		"http://localhost:3425/x",
		"http://foo.localhost/x",
		"http://[::1]:3425/x",
		"http://127.1.2.3/x",
	} {
		expectProxy(t, target, "")
	}
}

// Acceptance 10: nothing configured means direct, with source none.
func TestNoProxy(t *testing.T) {
	setup(t)
	expectProxy(t, "https://chatgpt.com/x", "")
	expectDescribe(t, "", "none")
}

// A bare host:port in settings is HTTP.
func TestBareHostPortIsHTTP(t *testing.T) {
	setup(t)
	if err := settings.Save(settings.Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	expectProxy(t, "https://chatgpt.com/x", "http://127.0.0.1:7890")
	expectDescribe(t, "http://127.0.0.1:7890", "settings")
}

// An unusable settings value is an error, never a silent direct connection.
func TestInvalidSettingsIsError(t *testing.T) {
	setup(t)
	writeSettings(t, `{"proxy":"ftp://x"}`)
	if _, err := For(mustURL(t, "https://chatgpt.com/x")); err == nil {
		t.Fatal("For returned no error for invalid settings proxy")
	}
}

// Settings changes apply to the next request without a restart.
func TestSettingsChangeTakesEffect(t *testing.T) {
	setup(t)
	writeSettings(t, `{"proxy":"http://a.example:1"}`)
	expectProxy(t, "https://chatgpt.com/x", "http://a.example:1")
	writeSettings(t, `{"proxy":"direct"}`)
	expectProxy(t, "https://chatgpt.com/x", "")
}

// The system proxy read is cached for a short time.
func TestSystemCacheTTL(t *testing.T) {
	setup(t)
	calls := 0
	readSystemProxy = func() sysConfig {
		calls++
		return sysConfig{proxy: "http://127.0.0.1:7890"}
	}
	resetSystemCache()
	expectProxy(t, "https://chatgpt.com/x", "http://127.0.0.1:7890")
	expectProxy(t, "https://chatgpt.com/x", "http://127.0.0.1:7890")
	if calls != 1 {
		t.Fatalf("system read %d times, want 1 within TTL", calls)
	}
	sysMu.Lock()
	sysAt = sysAt.Add(-sysTTL - time.Second)
	sysMu.Unlock()
	expectProxy(t, "https://chatgpt.com/x", "http://127.0.0.1:7890")
	if calls != 2 {
		t.Fatalf("system read %d times, want 2 after TTL", calls)
	}
}

// Install sets the default transport's proxy to the same rules.
func TestInstall(t *testing.T) {
	setup(t)
	setSystem(t, sysConfig{proxy: "http://127.0.0.1:7890"})
	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })
	Install()
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatal("default transport has no proxy func")
	}
	p, err := tr.Proxy(&http.Request{URL: mustURL(t, "https://chatgpt.com/x")})
	if err != nil || p == nil || p.String() != "http://127.0.0.1:7890" {
		t.Fatalf("transport proxy = %v, %v", p, err)
	}
}

func TestParseScutil(t *testing.T) {
	all := `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
  }
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 7080
  HTTPProxy : 10.0.0.2
  HTTPSEnable : 1
  HTTPSPort : 7443
  HTTPSProxy : 10.0.0.3
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	cfg := parseScutil(all)
	if cfg.proxy != "http://10.0.0.3:7443" {
		t.Errorf("HTTPS and HTTP enabled: proxy = %q", cfg.proxy)
	}
	if len(cfg.exceptions) != 2 || cfg.exceptions[0] != "*.local" {
		t.Errorf("exceptions = %v", cfg.exceptions)
	}

	httpOnly := strings.Replace(all, "HTTPSEnable : 1", "HTTPSEnable : 0", 1)
	if got := parseScutil(httpOnly).proxy; got != "http://10.0.0.2:7080" {
		t.Errorf("HTTP only: proxy = %q", got)
	}

	socksOnly := `<dictionary> {
  HTTPEnable : 0
  HTTPProxy : 10.0.0.2
  HTTPPort : 7080
  HTTPSEnable : 0
  HTTPSProxy : 10.0.0.3
  HTTPSPort : 7443
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	if got := parseScutil(socksOnly).proxy; got != "socks5://127.0.0.1:1080" {
		t.Errorf("SOCKS only: proxy = %q", got)
	}

	none := `<dictionary> {
  HTTPEnable : 0
  HTTPProxy : 10.0.0.2
  HTTPPort : 7080
  HTTPSEnable : 0
  HTTPSProxy : 10.0.0.3
  HTTPSPort : 7443
  SOCKSEnable : 0
  SOCKSProxy : 127.0.0.1
  SOCKSPort : 1080
}`
	if got := parseScutil(none); got.proxy != "" {
		t.Errorf("nothing enabled: proxy = %q", got.proxy)
	}
}

func TestParseWindowsProxy(t *testing.T) {
	cases := []struct {
		name, enable, server, override, want string
	}{
		{"single host:port", "1", "127.0.0.1:7890", "", "http://127.0.0.1:7890"},
		{"https preferred", "0x1", "http=10.0.0.2:80;https=10.0.0.3:443;socks=127.0.0.1:1080", "", "http://10.0.0.3:443"},
		{"http when no https", "1", "http=10.0.0.2:80;socks=127.0.0.1:1080", "", "http://10.0.0.2:80"},
		{"socks only", "1", "socks=127.0.0.1:1080", "", "socks5://127.0.0.1:1080"},
		{"scheme kept", "1", "http=http://10.0.0.2:80", "", "http://10.0.0.2:80"},
		{"disabled", "0", "127.0.0.1:7890", "", ""},
		{"enable missing", "", "127.0.0.1:7890", "", ""},
		{"empty server", "1", "", "", ""},
	}
	for _, c := range cases {
		got := parseWindowsProxy(c.enable, c.server, c.override)
		if got.proxy != c.want {
			t.Errorf("%s: proxy = %q, want %q", c.name, got.proxy, c.want)
		}
	}
}

func TestWindowsOverrideMatching(t *testing.T) {
	cfg := parseWindowsProxy("1", "127.0.0.1:7890", "*.corp;<local>")
	if !matchException("git.corp", cfg.exceptions) {
		t.Error("git.corp should be direct")
	}
	if !matchException("intranet", cfg.exceptions) {
		t.Error("intranet should be direct")
	}
	if matchException("chatgpt.com", cfg.exceptions) {
		t.Error("chatgpt.com should use the proxy")
	}
}

func TestMatchException(t *testing.T) {
	list := []string{"", "  ", "*.example.com", ".other.org", "Exact.Host", "<local>"}
	yes := []string{"a.example.com", "example.com", "x.other.org", "other.org", "exact.host", "EXACT.host", "intranet"}
	no := []string{"notexample.com", "a.example.org", "sub.exact.host", "other.org.evil", "a.intranet"}
	for _, h := range yes {
		if !matchException(h, list) {
			t.Errorf("%s should match", h)
		}
	}
	for _, h := range no {
		if matchException(h, list) {
			t.Errorf("%s should not match", h)
		}
	}
}

func TestParseRegValue(t *testing.T) {
	out := "\r\nHKEY_CURRENT_USER\\Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings\r\n" +
		"    ProxyEnable    REG_DWORD    0x1\r\n" +
		"    ProxyServer    REG_SZ    127.0.0.1:7890\r\n" +
		"    ProxyOverride    REG_SZ    *.corp;<local>\r\n"
	if got := parseRegValue(out, "ProxyEnable"); got != "0x1" {
		t.Errorf("ProxyEnable = %q", got)
	}
	if got := parseRegValue(out, "ProxyServer"); got != "127.0.0.1:7890" {
		t.Errorf("ProxyServer = %q", got)
	}
	if got := parseRegValue(out, "ProxyOverride"); got != "*.corp;<local>" {
		t.Errorf("ProxyOverride = %q", got)
	}
	if got := parseRegValue(out, "AutoConfigURL"); got != "" {
		t.Errorf("missing key = %q", got)
	}
}
