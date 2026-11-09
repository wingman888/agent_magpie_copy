package netproxy

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

var netproxyEnvNames = []string{
	"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy",
	"ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD",
}

// netproxySetup isolates the settings file, the environment, and the system
// proxy reading. settingsProxy "" leaves the settings file absent.
func netproxySetup(t *testing.T, settingsProxy string, env map[string]string, sys systemConfig) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, n := range netproxyEnvNames {
		t.Setenv(n, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	if settingsProxy != "" {
		if err := settings.Save(settings.Settings{Proxy: settingsProxy}); err != nil {
			t.Fatal(err)
		}
	}
	old := loadSystem
	loadSystem = func() systemConfig { return sys }
	resetSystemCache()
	t.Cleanup(func() {
		loadSystem = old
		resetSystemCache()
	})
}

func netproxyMust(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func netproxyWantProxy(t *testing.T, target, want string) {
	t.Helper()
	got, err := For(netproxyMust(t, target))
	if err != nil {
		t.Fatalf("For(%s) error: %v", target, err)
	}
	if want == "" {
		if got != nil {
			t.Fatalf("For(%s) = %s, want direct", target, got)
		}
		return
	}
	if got == nil || got.String() != want {
		t.Fatalf("For(%s) = %v, want %s", target, got, want)
	}
}

func netproxyWantSource(t *testing.T, proxy, source, wantProxy, wantSource string) {
	t.Helper()
	if source != wantSource || proxy != wantProxy {
		t.Fatalf("Describe() = (%q, %q), want (%q, %q)", proxy, source, wantProxy, wantSource)
	}
}

var netproxySystem7890 = systemConfig{proxy: &url.URL{Scheme: "http", Host: "127.0.0.1:7890"}}

// Acceptance 1: system proxy is used when settings and env are empty.
func TestNetproxySystemProxyUsed(t *testing.T) {
	netproxySetup(t, "", nil, netproxySystem7890)
	netproxyWantProxy(t, "https://chatgpt.com/backend-api", "http://127.0.0.1:7890")
	p, s := Describe()
	netproxyWantSource(t, p, s, "http://127.0.0.1:7890", "system")
}

// Acceptance 2: system exception list makes matching hosts direct.
func TestNetproxySystemExceptions(t *testing.T) {
	sys := netproxySystem7890
	sys.exceptions = []string{"*.corp"}
	netproxySetup(t, "", nil, sys)
	netproxyWantProxy(t, "https://git.corp/x", "")
	netproxyWantProxy(t, "https://chatgpt.com/", "http://127.0.0.1:7890")
}

// Acceptance 3: settings proxy beats env and system.
func TestNetproxySettingsProxyWins(t *testing.T) {
	netproxySetup(t, "socks5://127.0.0.1:1080",
		map[string]string{"HTTPS_PROXY": "http://env:3128"}, netproxySystem7890)
	netproxyWantProxy(t, "https://api.openai.com/", "socks5://127.0.0.1:1080")
	p, s := Describe()
	netproxyWantSource(t, p, s, "socks5://127.0.0.1:1080", "settings")
}

// Acceptance 4: "direct" disables every proxy.
func TestNetproxyDirectDisables(t *testing.T) {
	netproxySetup(t, "direct",
		map[string]string{"HTTPS_PROXY": "http://env:3128"}, netproxySystem7890)
	netproxyWantProxy(t, "https://api.openai.com/", "")
	p, s := Describe()
	netproxyWantSource(t, p, s, "", "off")
}

// Acceptance 5: environment beats system.
func TestNetproxyEnvironmentBeatsSystem(t *testing.T) {
	netproxySetup(t, "", map[string]string{"HTTPS_PROXY": "http://env:3128"}, netproxySystem7890)
	netproxyWantProxy(t, "https://api.openai.com/", "http://env:3128")
	p, s := Describe()
	netproxyWantSource(t, p, s, "http://env:3128", "environment")
}

// Scenario D: NO_PROXY from the environment still applies.
func TestNetproxyEnvironmentNoProxy(t *testing.T) {
	netproxySetup(t, "", map[string]string{
		"HTTPS_PROXY": "http://env:3128",
		"NO_PROXY":    ".corp,example.org",
	}, netproxySystem7890)
	netproxyWantProxy(t, "https://git.corp/x", "")
	netproxyWantProxy(t, "https://example.org/", "")
	netproxyWantProxy(t, "https://api.openai.com/", "http://env:3128")
}

// Acceptance 6: loopback is always direct, whatever is configured.
func TestNetproxyLoopbackAlwaysDirect(t *testing.T) {
	netproxySetup(t, "socks5://127.0.0.1:1080",
		map[string]string{"HTTPS_PROXY": "http://env:3128", "HTTP_PROXY": "http://env:3128"},
		netproxySystem7890)
	for _, target := range []string{
		"http://127.0.0.1:3425/x", "http://localhost:3425", "http://a.localhost/",
		"http://[::1]:3425/", "http://127.9.9.9/",
	} {
		netproxyWantProxy(t, target, "")
	}
}

// Acceptance 7: a bare host:port in settings is an HTTP proxy.
func TestNetproxyBareHostPortIsHTTP(t *testing.T) {
	netproxySetup(t, "127.0.0.1:7890", nil, systemConfig{})
	netproxyWantProxy(t, "https://api.openai.com/", "http://127.0.0.1:7890")
}

// A value that was edited into the file by hand must fail, not go direct.
func TestNetproxyInvalidSettingsErrors(t *testing.T) {
	netproxySetup(t, "", nil, netproxySystem7890)
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Clean(settings.Path()), []byte(`{"proxy":"ftp://x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := For(netproxyMust(t, "https://api.openai.com/")); err == nil {
		t.Fatal("invalid settings proxy did not return an error")
	}
	if _, err := For(netproxyMust(t, "http://127.0.0.1:3425/")); err != nil {
		t.Fatalf("loopback should stay direct without error: %v", err)
	}
}

// Acceptance 8 (macOS scutil parsing).
func TestNetproxyParseScutilPriority(t *testing.T) {
	all := `<dictionary> {
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
  SOCKSProxy : 127.0.0.2
}
`
	cfg := parseScutil(all)
	if cfg.proxy == nil || cfg.proxy.String() != "http://127.0.0.1:7890" {
		t.Fatalf("all enabled: proxy = %v, want https one", cfg.proxy)
	}
	if len(cfg.exceptions) != 2 || cfg.exceptions[0] != "*.local" {
		t.Fatalf("exceptions = %v", cfg.exceptions)
	}
	onlySOCKS := `<dictionary> {
  HTTPEnable : 0
  HTTPProxy : 10.0.0.1
  HTTPPort : 8080
  HTTPSEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	if cfg := parseScutil(onlySOCKS); cfg.proxy == nil || cfg.proxy.String() != "socks5://127.0.0.1:1080" {
		t.Fatalf("only SOCKS: proxy = %v", cfg.proxy)
	}
	none := `<dictionary> {
  HTTPEnable : 0
  HTTPProxy : 10.0.0.1
  HTTPSEnable : 0
  HTTPSProxy : 10.0.0.2
  SOCKSEnable : 0
  SOCKSProxy : 10.0.0.3
}`
	if cfg := parseScutil(none); cfg.proxy != nil {
		t.Fatalf("nothing enabled: proxy = %v, want nil", cfg.proxy)
	}
}

// Acceptance 9 (Windows ProxyServer forms and ProxyOverride).
func TestNetproxyParseWindowsForms(t *testing.T) {
	cases := []struct{ server, want string }{
		{"127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"http=10.0.0.1:80;https=127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"http=10.0.0.1:80;socks=127.0.0.1:1080", "http://10.0.0.1:80"},
		{"socks=127.0.0.1:1080", "socks5://127.0.0.1:1080"},
	}
	for _, c := range cases {
		cfg := parseWindowsProxy("0x1", c.server, "")
		if cfg.proxy == nil || cfg.proxy.String() != c.want {
			t.Errorf("ProxyServer %q: proxy = %v, want %s", c.server, cfg.proxy, c.want)
		}
	}
	if cfg := parseWindowsProxy("0", "127.0.0.1:7890", ""); cfg.proxy != nil {
		t.Fatal("disabled ProxyEnable still gave a proxy")
	}
}

func TestNetproxyWindowsOverride(t *testing.T) {
	sys := parseWindowsProxy("0x1", "127.0.0.1:7890", "*.corp;<local>")
	netproxySetup(t, "", nil, sys)
	netproxyWantProxy(t, "https://git.corp/x", "")
	netproxyWantProxy(t, "http://intranet/", "")
	netproxyWantProxy(t, "https://chatgpt.com/", "http://127.0.0.1:7890")
}

func TestNetproxyParseRegQuery(t *testing.T) {
	out := "\r\nHKEY_CURRENT_USER\\Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings\r\n" +
		"    ProxyEnable    REG_DWORD    0x1\r\n" +
		"    ProxyServer    REG_SZ    127.0.0.1:7890\r\n" +
		"    ProxyOverride    REG_SZ    *.corp;<local>\r\n"
	m := parseRegQuery(out)
	if m["ProxyEnable"] != "0x1" || m["ProxyServer"] != "127.0.0.1:7890" || m["ProxyOverride"] != "*.corp;<local>" {
		t.Fatalf("parseRegQuery = %v", m)
	}
}

// Acceptance 10: on other systems with no env, requests are direct.
func TestNetproxyNoSystemProxyOffPlatform(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("system proxy is read on this platform")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, n := range netproxyEnvNames {
		t.Setenv(n, "")
	}
	resetSystemCache()
	netproxyWantProxy(t, "https://chatgpt.com/", "")
	p, s := Describe()
	netproxyWantSource(t, p, s, "", "none")
}

// The system reading is cached, but a change must show up within the TTL.
func TestNetproxySystemCacheExpires(t *testing.T) {
	netproxySetup(t, "", nil, systemConfig{})
	netproxyWantProxy(t, "https://chatgpt.com/", "")
	sysCache.mu.Lock()
	sysCache.at = sysCache.at.Add(-systemTTL - 1)
	sysCache.mu.Unlock()
	loadSystem = func() systemConfig { return netproxySystem7890 }
	netproxyWantProxy(t, "https://chatgpt.com/", "http://127.0.0.1:7890")
}

func TestNetproxyInstallWiresDefaultTransport(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = &http.Transport{}
	Install()
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatal("Install did not set a Proxy on DefaultTransport")
	}
	netproxySetup(t, "", nil, netproxySystem7890)
	req, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/", nil)
	p, err := tr.Proxy(req)
	if err != nil || p == nil || p.String() != "http://127.0.0.1:7890" {
		t.Fatalf("Proxy(req) = %v, %v", p, err)
	}
}
