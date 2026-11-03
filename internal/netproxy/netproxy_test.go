package netproxy

import (
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

var allProxyEnv = []string{
	"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy",
	"ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy",
}

// isolate gives the test its own settings file, no proxy variables, and a
// fixed system proxy reading.
func isolate(t *testing.T, sys systemSettings) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clearEnv(t)
	old := readSystem
	readSystem = func() systemSettings { return sys }
	resetSystemCache()
	t.Cleanup(func() {
		readSystem = old
		resetSystemCache()
	})
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, n := range allProxyEnv {
		t.Setenv(n, "")
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// forString returns For's proxy as a string, or "" for direct.
func forString(t *testing.T, raw string) string {
	t.Helper()
	p, err := For(mustURL(t, raw))
	if err != nil {
		t.Fatalf("For(%s): %v", raw, err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

func expectDescribe(t *testing.T, wantProxy, wantSource string) {
	t.Helper()
	p, s := Describe()
	if p != wantProxy || s != wantSource {
		t.Fatalf("Describe() = (%q, %q), want (%q, %q)", p, s, wantProxy, wantSource)
	}
}

// Acceptance 1: no setting, no environment, system proxy is used.
func TestSystemProxyUsedWhenNothingElse(t *testing.T) {
	isolate(t, systemSettings{Proxy: "http://127.0.0.1:7890"})
	if got := forString(t, "https://chatgpt.com/backend-api/x"); got != "http://127.0.0.1:7890" {
		t.Fatalf("For = %q", got)
	}
	expectDescribe(t, "http://127.0.0.1:7890", SourceSystem)
}

// Acceptance 2: exception list makes matching hosts direct.
func TestSystemExceptions(t *testing.T) {
	isolate(t, systemSettings{Proxy: "http://127.0.0.1:7890", Exceptions: []string{"*.corp"}})
	if got := forString(t, "https://git.corp/x"); got != "" {
		t.Fatalf("git.corp should be direct, got %q", got)
	}
	if got := forString(t, "https://corp/x"); got != "" {
		t.Fatalf("corp should be direct, got %q", got)
	}
	if got := forString(t, "https://chatgpt.com/x"); got != "http://127.0.0.1:7890" {
		t.Fatalf("chatgpt.com should use proxy, got %q", got)
	}
}

// Acceptance 3: a settings proxy wins over environment and system proxy.
func TestSettingsProxyWins(t *testing.T) {
	isolate(t, systemSettings{Proxy: "http://127.0.0.1:7890"})
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:8080")
	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	if got := forString(t, "https://chatgpt.com/x"); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("For = %q", got)
	}
	expectDescribe(t, "socks5://127.0.0.1:1080", SourceSettings)
}

// Acceptance 4: "direct" turns all proxies off.
func TestSettingsDirect(t *testing.T) {
	isolate(t, systemSettings{Proxy: "http://127.0.0.1:7890"})
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:8080")
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if got := forString(t, "https://chatgpt.com/x"); got != "" {
		t.Fatalf("direct but For = %q", got)
	}
	expectDescribe(t, "", SourceOff)
}

// Acceptance 5: the environment comes before the system proxy.
func TestEnvironmentBeforeSystem(t *testing.T) {
	isolate(t, systemSettings{Proxy: "http://127.0.0.1:7890"})
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:8080")
	if got := forString(t, "https://chatgpt.com/x"); got != "http://10.0.0.1:8080" {
		t.Fatalf("For = %q", got)
	}
	expectDescribe(t, "http://10.0.0.1:8080", SourceEnvironment)
}

// Lowercase variables are read too, and NO_PROXY keeps its usual meaning.
func TestEnvironmentLowercaseAndNoProxy(t *testing.T) {
	isolate(t, systemSettings{Proxy: "http://127.0.0.1:7890"})
	t.Setenv("https_proxy", "10.0.0.1:8080")
	t.Setenv("no_proxy", ".internal.example")
	if got := forString(t, "https://chatgpt.com/x"); got != "http://10.0.0.1:8080" {
		t.Fatalf("lowercase env: For = %q", got)
	}
	if got := forString(t, "https://api.internal.example/x"); got != "" {
		t.Fatalf("NO_PROXY should make it direct, got %q", got)
	}
}

// A variable that does not cover the scheme leaves the decision to the system.
func TestEnvironmentNotCoveringSchemeFallsThrough(t *testing.T) {
	isolate(t, systemSettings{Proxy: "http://127.0.0.1:7890"})
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:8080")
	if got := forString(t, "http://plain.example/x"); got != "http://127.0.0.1:7890" {
		t.Fatalf("For = %q", got)
	}
}

// Acceptance 6: loopback is direct under every configuration.
func TestLoopbackAlwaysDirect(t *testing.T) {
	isolate(t, systemSettings{Proxy: "http://127.0.0.1:7890"})
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:8080")
	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"http://127.0.0.1:3425/x",
		"http://localhost:3425/x",
		"http://api.localhost:8080/x",
		"http://[::1]:3425/x",
		"http://127.1.2.3/x",
	} {
		if got := forString(t, raw); got != "" {
			t.Errorf("%s should be direct, got %q", raw, got)
		}
	}
}

// Acceptance 7: a bare host:port is an HTTP proxy.
func TestBareHostPortIsHTTP(t *testing.T) {
	isolate(t, systemSettings{})
	if err := settings.Save(settings.Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if got := forString(t, "https://chatgpt.com/x"); got != "http://127.0.0.1:7890" {
		t.Fatalf("For = %q", got)
	}
	err := settings.Save(settings.Settings{Proxy: "ftp://x"})
	if err == nil || !strings.Contains(err.Error(), settings.ProxyExample) {
		t.Fatalf("Save(ftp://x) error = %v, want one with an example", err)
	}
}

// A stored value that cannot be used is an error, never a silent direct connection.
func TestInvalidStoredSettingIsError(t *testing.T) {
	isolate(t, systemSettings{Proxy: "http://127.0.0.1:7890"})
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.Path(), []byte(`{"proxy":"ftp://x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := For(mustURL(t, "https://chatgpt.com/x"))
	if err == nil {
		t.Fatalf("For accepted invalid setting, returned %v", p)
	}
	if p != nil {
		t.Fatalf("For returned proxy %v with error", p)
	}
}

// Acceptance 10: on other platforms with no environment proxy, requests are direct.
func TestNoSystemProxyElsewhere(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("system proxy is read on this platform")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clearEnv(t)
	resetSystemCache()
	t.Cleanup(resetSystemCache)
	if got := forString(t, "https://chatgpt.com/x"); got != "" {
		t.Fatalf("For = %q, want direct", got)
	}
	expectDescribe(t, "", SourceNone)
}

// A failing system read is treated as no system proxy.
func TestSystemReadFailureIsNoProxy(t *testing.T) {
	isolate(t, systemSettings{})
	if got := forString(t, "https://chatgpt.com/x"); got != "" {
		t.Fatalf("For = %q", got)
	}
	expectDescribe(t, "", SourceNone)
}

// An invalid stored setting makes Describe report no proxy, still from "settings".
func TestDescribeInvalidSettingHasNoProxy(t *testing.T) {
	isolate(t, systemSettings{Proxy: "http://127.0.0.1:7890"})
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.Path(), []byte(`{"proxy":"ftp://x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	expectDescribe(t, "", SourceSettings)
}

// Install makes the default transport use For.
func TestInstallSetsDefaultTransport(t *testing.T) {
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("DefaultTransport is not *http.Transport")
	}
	old := tr.Proxy
	oldOnce := installOnce
	t.Cleanup(func() {
		tr.Proxy = old
		installOnce = oldOnce
	})
	// Install runs at most once per process; reset it so this test does not
	// depend on, or consume, that one call (matters with -count=2).
	installOnce = sync.Once{}
	isolate(t, systemSettings{Proxy: "http://127.0.0.1:7890"})

	Install()
	if tr.Proxy == nil {
		t.Fatal("Install did not set Proxy")
	}
	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tr.Proxy(req)
	if err != nil || p == nil || p.String() != "http://127.0.0.1:7890" {
		t.Fatalf("transport proxy = %v, %v", p, err)
	}
	loop, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:3425/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := tr.Proxy(loop); err != nil || p != nil {
		t.Fatalf("loopback transport proxy = %v, %v", p, err)
	}
}

// Acceptance 8: macOS scutil output.
func TestParseDarwinProxy(t *testing.T) {
	all := `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
    2 : corp.example
  }
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : 10.0.0.2
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	sys := parseDarwinProxy(all)
	if sys.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("all enabled: proxy = %q, want HTTPS", sys.Proxy)
	}
	wantExc := []string{"*.local", "169.254/16", "corp.example"}
	if strings.Join(sys.Exceptions, ",") != strings.Join(wantExc, ",") {
		t.Fatalf("exceptions = %v", sys.Exceptions)
	}

	socksOnly := `<dictionary> {
  HTTPEnable : 0
  HTTPProxy : 10.0.0.2
  HTTPPort : 8080
  HTTPSEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	if got := parseDarwinProxy(socksOnly).Proxy; got != "socks5://127.0.0.1:1080" {
		t.Fatalf("socks only: proxy = %q", got)
	}

	httpOnly := `<dictionary> {
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : 10.0.0.2
  HTTPSEnable : 0
  SOCKSEnable : 0
}`
	if got := parseDarwinProxy(httpOnly).Proxy; got != "http://10.0.0.2:8080" {
		t.Fatalf("http only: proxy = %q", got)
	}

	none := `<dictionary> {
  HTTPEnable : 0
  HTTPProxy : 10.0.0.2
  HTTPPort : 8080
  HTTPSEnable : 0
  HTTPSProxy : 127.0.0.1
  HTTPSPort : 7890
  SOCKSEnable : 0
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	if got := parseDarwinProxy(none).Proxy; got != "" {
		t.Fatalf("none enabled: proxy = %q", got)
	}
	if got := parseDarwinProxy("").Proxy; got != "" {
		t.Fatalf("empty output: proxy = %q", got)
	}
}

// Acceptance 9: Windows ProxyServer and ProxyOverride.
func TestParseWindowsProxy(t *testing.T) {
	cases := []struct {
		name, server, want string
	}{
		{"single host:port is HTTP", "127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"https preferred, as HTTP proxy", "http=10.0.0.2:8080;https=127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"https alone, as HTTP proxy", "https=127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"http before socks", "http=10.0.0.2:8080;socks=127.0.0.1:1080", "http://10.0.0.2:8080"},
		{"socks only", "socks=127.0.0.1:1080", "socks5://127.0.0.1:1080"},
		{"keeps scheme given", "http=http://10.0.0.2:8080", "http://10.0.0.2:8080"},
		{"empty", "", ""},
		{"empty list values", "http=;https=", ""},
	}
	for _, c := range cases {
		if got := parseWindowsProxy(c.server, "").Proxy; got != c.want {
			t.Errorf("%s: parseWindowsProxy(%q) = %q, want %q", c.name, c.server, got, c.want)
		}
	}
}

func TestParseWindowsOverride(t *testing.T) {
	sys := parseWindowsProxy("127.0.0.1:7890", "*.corp; <local> ;;")
	want := []string{"*.corp", "<local>"}
	if strings.Join(sys.Exceptions, "|") != strings.Join(want, "|") {
		t.Fatalf("exceptions = %q", sys.Exceptions)
	}

	isolate(t, sys)
	if got := forString(t, "https://git.corp/x"); got != "" {
		t.Fatalf("git.corp should be direct, got %q", got)
	}
	if got := forString(t, "https://intranet/x"); got != "" {
		t.Fatalf("intranet should be direct, got %q", got)
	}
	if got := forString(t, "https://chatgpt.com/x"); got != "http://127.0.0.1:7890" {
		t.Fatalf("chatgpt.com should use proxy, got %q", got)
	}
}

func TestExceptionMatching(t *testing.T) {
	cases := []struct {
		host string
		list []string
		want bool
	}{
		{"Git.Corp", []string{"git.corp"}, true},
		{"git.corp.evil", []string{"git.corp"}, false},
		{"a.example.com", []string{"*.example.com"}, true},
		{"example.com", []string{"*.example.com"}, true},
		{"notexample.com", []string{"*.example.com"}, false},
		{"a.example.com", []string{".example.com"}, true},
		{"example.com", []string{".example.com"}, true},
		{"intranet", []string{"<local>"}, true},
		{"intranet.corp", []string{"<local>"}, false},
		{"chatgpt.com", []string{"", "  ", "*.corp"}, false},
	}
	for _, c := range cases {
		if got := excepted(c.host, c.list); got != c.want {
			t.Errorf("excepted(%q, %v) = %v, want %v", c.host, c.list, got, c.want)
		}
	}
}
