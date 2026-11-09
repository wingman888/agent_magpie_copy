package netproxy

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// npTestSetup replaces the settings, environment and OS proxy readings for
// the duration of one test. The cached OS reading is dropped before and after.
func npTestSetup(t *testing.T, s settings.Settings, env map[string]string, sys sysProxy) *int {
	t.Helper()
	prevSettings, prevEnv, prevSys := loadSettings, envGet, readSystemProxy
	calls := 0
	loadSettings = func() settings.Settings { return s }
	envGet = func(k string) string { return env[k] }
	readSystemProxy = func() sysProxy { calls++; return sys }
	npTestResetCache()
	t.Cleanup(func() {
		loadSettings, envGet, readSystemProxy = prevSettings, prevEnv, prevSys
		npTestResetCache()
	})
	return &calls
}

func npTestResetCache() {
	sysMu.Lock()
	sysAt = time.Time{}
	sysMu.Unlock()
}

func npTestURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func npTestWant(t *testing.T, got *url.URL, want string) {
	t.Helper()
	if want == "" {
		if got != nil {
			t.Fatalf("expected direct, got %s", got)
		}
		return
	}
	if got == nil {
		t.Fatalf("expected proxy %s, got direct", want)
	}
	if got.String() != want {
		t.Fatalf("proxy = %s, want %s", got, want)
	}
}

func npTestFor(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	p, err := For(npTestURL(t, rawURL))
	if err != nil {
		t.Fatalf("For(%s) error: %v", rawURL, err)
	}
	return p
}

func npTestDescribe(t *testing.T, wantProxy, wantSource string) {
	t.Helper()
	p, src := Describe()
	if p != wantProxy || src != wantSource {
		t.Fatalf("Describe() = (%q, %q), want (%q, %q)", p, src, wantProxy, wantSource)
	}
}

// Acceptance 1: system proxy used when nothing else is set.
func TestNetproxySystemProxyUsed(t *testing.T) {
	npTestSetup(t, settings.Settings{}, nil, sysProxy{proxy: "http://127.0.0.1:7890"})
	npTestWant(t, npTestFor(t, "https://chatgpt.com/backend-api"), "http://127.0.0.1:7890")
	npTestDescribe(t, "http://127.0.0.1:7890", "system")
}

// Acceptance 2: system exception list.
func TestNetproxySystemExceptions(t *testing.T) {
	npTestSetup(t, settings.Settings{}, nil, sysProxy{
		proxy:      "http://127.0.0.1:7890",
		exceptions: []string{"*.corp"},
	})
	npTestWant(t, npTestFor(t, "https://git.corp/x"), "")
	npTestWant(t, npTestFor(t, "https://chatgpt.com/"), "http://127.0.0.1:7890")
}

// Acceptance 3: settings beat environment and system.
func TestNetproxySettingsWin(t *testing.T) {
	npTestSetup(t,
		settings.Settings{Proxy: "socks5://127.0.0.1:1080"},
		map[string]string{"HTTPS_PROXY": "http://10.0.0.9:3128"},
		sysProxy{proxy: "http://127.0.0.1:7890"})
	npTestWant(t, npTestFor(t, "https://api.openai.com/v1"), "socks5://127.0.0.1:1080")
	npTestDescribe(t, "socks5://127.0.0.1:1080", "settings")
}

// Bare host:port in settings is an HTTP proxy.
func TestNetproxySettingsBareHostPort(t *testing.T) {
	npTestSetup(t, settings.Settings{Proxy: "127.0.0.1:7890"}, nil, sysProxy{})
	npTestWant(t, npTestFor(t, "https://api.openai.com/"), "http://127.0.0.1:7890")
	npTestDescribe(t, "http://127.0.0.1:7890", "settings")
}

// Acceptance 4: direct beats everything.
func TestNetproxySettingsDirect(t *testing.T) {
	npTestSetup(t,
		settings.Settings{Proxy: "direct"},
		map[string]string{"HTTPS_PROXY": "http://10.0.0.9:3128"},
		sysProxy{proxy: "http://127.0.0.1:7890"})
	npTestWant(t, npTestFor(t, "https://api.openai.com/"), "")
	npTestDescribe(t, "", "off")
}

// Acceptance 5: environment beats system.
func TestNetproxyEnvironmentBeforeSystem(t *testing.T) {
	npTestSetup(t, settings.Settings{},
		map[string]string{"HTTPS_PROXY": "http://10.0.0.9:3128"},
		sysProxy{proxy: "http://127.0.0.1:7890"})
	npTestWant(t, npTestFor(t, "https://api.anthropic.com/"), "http://10.0.0.9:3128")
	npTestDescribe(t, "http://10.0.0.9:3128", "environment")
}

// Lowercase variables are honoured, and ALL_PROXY covers https when HTTPS_PROXY is absent.
func TestNetproxyEnvironmentLowercaseAndAll(t *testing.T) {
	npTestSetup(t, settings.Settings{},
		map[string]string{"all_proxy": "socks5://127.0.0.1:1080"}, sysProxy{})
	npTestWant(t, npTestFor(t, "https://api.anthropic.com/"), "socks5://127.0.0.1:1080")
	npTestDescribe(t, "socks5://127.0.0.1:1080", "environment")
}

// NO_PROXY from the environment keeps the standard meaning.
func TestNetproxyEnvironmentNoProxy(t *testing.T) {
	npTestSetup(t, settings.Settings{},
		map[string]string{"HTTPS_PROXY": "http://10.0.0.9:3128", "NO_PROXY": ".internal.example, chatgpt.com"},
		sysProxy{proxy: "http://127.0.0.1:7890"})
	npTestWant(t, npTestFor(t, "https://chatgpt.com/"), "")
	npTestWant(t, npTestFor(t, "https://a.internal.example/"), "")
	npTestWant(t, npTestFor(t, "https://api.anthropic.com/"), "http://10.0.0.9:3128")
}

// Acceptance 6: loopback is always direct, whatever is configured.
func TestNetproxyLoopbackAlwaysDirect(t *testing.T) {
	env := map[string]string{"HTTPS_PROXY": "http://10.0.0.9:3128", "HTTP_PROXY": "http://10.0.0.9:3128"}
	sys := sysProxy{proxy: "http://127.0.0.1:7890"}
	for _, s := range []settings.Settings{{}, {Proxy: "socks5://127.0.0.1:1080"}, {Proxy: "direct"}} {
		npTestSetup(t, s, env, sys)
		for _, u := range []string{
			"http://127.0.0.1:3425/x",
			"https://localhost/x",
			"http://LOCALHOST:8080/",
			"http://foo.localhost/",
			"http://[::1]:3425/",
			"http://127.9.8.7/",
		} {
			npTestWant(t, npTestFor(t, u), "")
		}
	}
}

// Acceptance 10: non-macOS, non-Windows with nothing set is direct, source none.
func TestNetproxyNoneDirect(t *testing.T) {
	npTestSetup(t, settings.Settings{}, nil, sysProxy{})
	npTestWant(t, npTestFor(t, "https://chatgpt.com/"), "")
	npTestDescribe(t, "", "none")
}

// A broken configured proxy is an error, never a silent direct connection.
func TestNetproxyInvalidSettingsErrors(t *testing.T) {
	npTestSetup(t, settings.Settings{Proxy: "ftp://x"}, nil, sysProxy{proxy: "http://127.0.0.1:7890"})
	if _, err := For(npTestURL(t, "https://api.openai.com/")); err == nil {
		t.Fatal("For accepted an invalid settings proxy")
	}
	npTestDescribe(t, "", "invalid")
}

// A broken environment proxy is an error too.
func TestNetproxyInvalidEnvironmentErrors(t *testing.T) {
	npTestSetup(t, settings.Settings{}, map[string]string{"HTTPS_PROXY": "ftp://x"}, sysProxy{})
	if _, err := For(npTestURL(t, "https://api.openai.com/")); err == nil {
		t.Fatal("For accepted an invalid environment proxy")
	}
}

// A nil URL goes direct.
func TestNetproxyNilURL(t *testing.T) {
	npTestSetup(t, settings.Settings{Proxy: "socks5://127.0.0.1:1080"}, nil, sysProxy{})
	if p, err := For(nil); p != nil || err != nil {
		t.Fatalf("For(nil) = %v, %v", p, err)
	}
}

// Settings changes take effect on the next call, without a restart.
func TestNetproxySettingsReadEveryCall(t *testing.T) {
	npTestSetup(t, settings.Settings{}, nil, sysProxy{})
	cur := settings.Settings{}
	loadSettings = func() settings.Settings { return cur }
	npTestWant(t, npTestFor(t, "https://api.openai.com/"), "")
	cur = settings.Settings{Proxy: "socks5://127.0.0.1:1080"}
	npTestWant(t, npTestFor(t, "https://api.openai.com/"), "socks5://127.0.0.1:1080")
}

// The OS reading is cached: a second call inside the TTL does not re-read it.
func TestNetproxySystemReadCached(t *testing.T) {
	calls := npTestSetup(t, settings.Settings{}, nil, sysProxy{proxy: "http://127.0.0.1:7890"})
	npTestFor(t, "https://a.example/")
	npTestFor(t, "https://b.example/")
	if *calls != 1 {
		t.Fatalf("system proxy read %d times, want 1", *calls)
	}
}

// Install makes http.DefaultTransport follow For.
func TestNetproxyInstallSetsDefaultTransport(t *testing.T) {
	npTestSetup(t, settings.Settings{Proxy: "socks5://127.0.0.1:1080"}, nil, sysProxy{})
	Install()
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatal("DefaultTransport has no proxy function")
	}
	p, err := tr.Proxy(&http.Request{URL: npTestURL(t, "https://api.openai.com/")})
	if err != nil {
		t.Fatal(err)
	}
	npTestWant(t, p, "socks5://127.0.0.1:1080")
	p, err = tr.Proxy(&http.Request{URL: npTestURL(t, "http://127.0.0.1:3425/")})
	if err != nil || p != nil {
		t.Fatalf("loopback through DefaultTransport: %v, %v", p, err)
	}
}

// Acceptance 8: macOS scutil output.
const npTestScutilAll = `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
  }
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : 10.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : 10.0.0.2
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}
`

func TestNetproxyScutilPrefersHTTPS(t *testing.T) {
	sp := parseScutil(npTestScutilAll)
	if sp.proxy != "http://10.0.0.2:8443" {
		t.Fatalf("proxy = %q, want https entry", sp.proxy)
	}
	if len(sp.exceptions) != 2 || sp.exceptions[0] != "*.local" {
		t.Fatalf("exceptions = %v", sp.exceptions)
	}
}

func TestNetproxyScutilSocksOnly(t *testing.T) {
	out := `<dictionary> {
  HTTPEnable : 0
  HTTPProxy : 10.0.0.1
  HTTPPort : 8080
  HTTPSEnable : 0
  HTTPSProxy : 10.0.0.2
  HTTPSPort : 8443
  SOCKSEnable : 1
  SOCKSProxy : 127.0.0.1
  SOCKSPort : 1080
}`
	if sp := parseScutil(out); sp.proxy != "socks5://127.0.0.1:1080" {
		t.Fatalf("proxy = %q", sp.proxy)
	}
}

func TestNetproxyScutilAllDisabled(t *testing.T) {
	out := `<dictionary> {
  HTTPEnable : 0
  HTTPProxy : 10.0.0.1
  HTTPPort : 8080
  SOCKSEnable : 0
  SOCKSProxy : 127.0.0.1
  SOCKSPort : 1080
}`
	if sp := parseScutil(out); sp.proxy != "" {
		t.Fatalf("disabled proxies used: %q", sp.proxy)
	}
}

// Acceptance 9: Windows ProxyServer and ProxyOverride.
func TestNetproxyWindowsSingleHostPort(t *testing.T) {
	if sp := parseWindowsProxy("127.0.0.1:7890", ""); sp.proxy != "http://127.0.0.1:7890" {
		t.Fatalf("proxy = %q", sp.proxy)
	}
}

func TestNetproxyWindowsPerProtocol(t *testing.T) {
	if sp := parseWindowsProxy("http=10.0.0.1:80;https=10.0.0.2:443", ""); sp.proxy != "http://10.0.0.2:443" {
		t.Fatalf("https not preferred: %q", sp.proxy)
	}
	if sp := parseWindowsProxy("http=10.0.0.1:80;socks=127.0.0.1:1080", ""); sp.proxy != "http://10.0.0.1:80" {
		t.Fatalf("http not preferred over socks: %q", sp.proxy)
	}
	if sp := parseWindowsProxy("socks=127.0.0.1:1080", ""); sp.proxy != "socks5://127.0.0.1:1080" {
		t.Fatalf("socks = %q", sp.proxy)
	}
}

func TestNetproxyWindowsOverrideMatching(t *testing.T) {
	sp := parseWindowsProxy("127.0.0.1:7890", "*.corp;<local>")
	if !matchExceptions("git.corp", sp.exceptions) {
		t.Fatal("git.corp should be direct")
	}
	if !matchExceptions("intranet", sp.exceptions) {
		t.Fatal("intranet should be direct")
	}
	if matchExceptions("chatgpt.com", sp.exceptions) {
		t.Fatal("chatgpt.com should go through the proxy")
	}
}

func TestNetproxyExceptionRules(t *testing.T) {
	cases := []struct {
		host string
		list []string
		want bool
	}{
		{"Git.Corp", []string{"git.corp"}, true},
		{"corp", []string{"*.corp"}, true},
		{"a.b.corp", []string{"*.corp"}, true},
		{"a.b.corp", []string{".corp"}, true},
		{"notcorp", []string{"*.corp"}, false},
		{"intranet", []string{"<local>"}, true},
		{"a.intranet", []string{"<local>"}, false},
		{"x.com", []string{"", "  "}, false},
	}
	for _, c := range cases {
		if got := matchExceptions(c.host, c.list); got != c.want {
			t.Errorf("matchExceptions(%q, %v) = %v, want %v", c.host, c.list, got, c.want)
		}
	}
}

// A system value that cannot be used counts as no system proxy.
func TestNetproxyUnusableSystemValueIgnored(t *testing.T) {
	if sp := parseWindowsProxy("ftp://bad", ""); sp.proxy != "" {
		t.Fatalf("unusable system value kept: %q", sp.proxy)
	}
	npTestSetup(t, settings.Settings{}, nil, sysProxy{})
	npTestWant(t, npTestFor(t, "https://chatgpt.com/"), "")
}
