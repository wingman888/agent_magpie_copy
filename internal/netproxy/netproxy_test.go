package netproxy

import (
	"net/http"
	"net/url"
	"runtime"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

var proxyEnvNames = []string{
	"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy",
	"ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy",
}

// setup installs fake settings and a fake system proxy, and clears every
// proxy environment variable. Everything is restored when the test ends.
func setup(t *testing.T, s settings.Settings, sp systemProxy) {
	t.Helper()
	for _, n := range proxyEnvNames {
		t.Setenv(n, "")
	}
	oldLoad, oldRead := loadSettings, readSystemProxy
	loadSettings = func() settings.Settings { return s }
	readSystemProxy = func() systemProxy { return sp }
	resetSystemCache()
	t.Cleanup(func() {
		loadSettings, readSystemProxy = oldLoad, oldRead
		resetSystemCache()
	})
}

// forString returns For(raw) as a string, or "DIRECT" for a direct connection.
func forString(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	pu, err := For(u)
	if err != nil {
		t.Fatalf("For(%s) error: %v", raw, err)
	}
	if pu == nil {
		return "DIRECT"
	}
	return pu.String()
}

var systemHTTP = systemProxy{URL: "http://127.0.0.1:7890"}

// Acceptance 1: settings empty, no env, system proxy set.
func TestSystemProxyUsedWhenNothingElse(t *testing.T) {
	setup(t, settings.Settings{}, systemHTTP)
	if got := forString(t, "https://chatgpt.com/backend-api/x"); got != "http://127.0.0.1:7890" {
		t.Fatalf("For = %s, want http://127.0.0.1:7890", got)
	}
	if p, src := Describe(); p != "http://127.0.0.1:7890" || src != sourceSystem {
		t.Fatalf("Describe = (%q, %q), want system proxy", p, src)
	}
}

// Acceptance 2: exception list "*.corp".
func TestSystemBypassList(t *testing.T) {
	setup(t, settings.Settings{}, systemProxy{URL: "http://127.0.0.1:7890", Bypass: []string{"*.corp"}})
	if got := forString(t, "https://git.corp/x"); got != "DIRECT" {
		t.Fatalf("git.corp: For = %s, want DIRECT", got)
	}
	if got := forString(t, "https://chatgpt.com/x"); got != "http://127.0.0.1:7890" {
		t.Fatalf("chatgpt.com: For = %s, want the system proxy", got)
	}
}

// Acceptance 3: settings proxy beats env and system.
func TestSettingsProxyWins(t *testing.T) {
	setup(t, settings.Settings{Proxy: "socks5://127.0.0.1:1080"}, systemHTTP)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9999")
	if got := forString(t, "https://api.openai.com/v1"); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("For = %s, want socks5://127.0.0.1:1080", got)
	}
	if p, src := Describe(); p != "socks5://127.0.0.1:1080" || src != sourceSettings {
		t.Fatalf("Describe = (%q, %q), want settings", p, src)
	}
}

// A bare host:port in settings is an HTTP proxy.
func TestSettingsBareHostPortIsHTTP(t *testing.T) {
	setup(t, settings.Settings{Proxy: "  127.0.0.1:7890  "}, systemProxy{})
	if got := forString(t, "https://api.anthropic.com/"); got != "http://127.0.0.1:7890" {
		t.Fatalf("For = %s, want http://127.0.0.1:7890", got)
	}
}

// Acceptance 4: "direct" ignores env and system.
func TestSettingsDirectIgnoresEverything(t *testing.T) {
	setup(t, settings.Settings{Proxy: "direct"}, systemHTTP)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9999")
	if got := forString(t, "https://api.openai.com/"); got != "DIRECT" {
		t.Fatalf("For = %s, want DIRECT", got)
	}
	if p, src := Describe(); p != "" || src != sourceOff {
		t.Fatalf("Describe = (%q, %q), want (\"\", off)", p, src)
	}
}

// Acceptance 5: env beats system.
func TestEnvironmentBeforeSystem(t *testing.T) {
	setup(t, settings.Settings{}, systemHTTP)
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:3128")
	if got := forString(t, "https://api.openai.com/"); got != "http://10.0.0.1:3128" {
		t.Fatalf("For = %s, want the environment proxy", got)
	}
	if p, src := Describe(); p != "http://10.0.0.1:3128" || src != sourceEnvironment {
		t.Fatalf("Describe = (%q, %q), want environment", p, src)
	}
}

// Lowercase variables are read too, and ALL_PROXY is the fallback for https.
func TestEnvironmentLowercaseAndAllProxy(t *testing.T) {
	setup(t, settings.Settings{}, systemProxy{})
	t.Setenv("all_proxy", "socks5://127.0.0.1:1080")
	if got := forString(t, "https://api.openai.com/"); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("For = %s, want socks5://127.0.0.1:1080", got)
	}
}

// NO_PROXY that matches stops the env proxy and does not fall back to the
// system proxy.
func TestNoProxyMatchSkipsSystem(t *testing.T) {
	setup(t, settings.Settings{}, systemHTTP)
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:3128")
	t.Setenv("NO_PROXY", ".chatgpt.com")
	if got := forString(t, "https://chatgpt.com/x"); got != "DIRECT" {
		t.Fatalf("For = %s, want DIRECT", got)
	}
	// A host NO_PROXY does not match still uses the env proxy.
	if got := forString(t, "https://api.openai.com/"); got != "http://10.0.0.1:3128" {
		t.Fatalf("api.openai.com: For = %s, want the env proxy", got)
	}
	// Describe decides for api.example.com, which NO_PROXY does not match.
	if p, src := Describe(); p != "http://10.0.0.1:3128" || src != sourceEnvironment {
		t.Fatalf("Describe = (%q, %q), want environment", p, src)
	}
}

func TestNoProxyEntryForms(t *testing.T) {
	cases := map[string]bool{
		"*":                   true,
		"api.openai.com":      true,
		"*.openai.com":        true,
		".openai.com":         true,
		"openai.com":          true,
		"api.openai.com:443":  true,
		"api.openai.com:8080": false,
		"10.0.0.0/8":          false,
		"other.example":       false,
		"xopenai.com":         false,
	}
	for entry, want := range cases {
		u, _ := url.Parse("https://api.openai.com/")
		t.Setenv("NO_PROXY", entry)
		if got := envNoProxy(u); got != want {
			t.Errorf("NO_PROXY=%q: envNoProxy = %v, want %v", entry, got, want)
		}
	}
	u, _ := url.Parse("https://10.1.2.3/")
	t.Setenv("NO_PROXY", "10.0.0.0/8")
	if !envNoProxy(u) {
		t.Error("CIDR entry should match 10.1.2.3")
	}
}

// Acceptance 6: loopback is direct whatever is configured.
func TestLoopbackAlwaysDirect(t *testing.T) {
	setup(t, settings.Settings{Proxy: "socks5://127.0.0.1:1080"}, systemHTTP)
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:3128")
	t.Setenv("HTTP_PROXY", "http://10.0.0.1:3128")
	for _, raw := range []string{
		"http://127.0.0.1:3425/x",
		"http://localhost/x",
		"http://localhost:8080/x",
		"http://api.localhost/x",
		"http://127.1.2.3/x",
		"http://[::1]:3425/x",
	} {
		if got := forString(t, raw); got != "DIRECT" {
			t.Errorf("For(%s) = %s, want DIRECT", raw, got)
		}
	}
}

func TestInvalidSettingsIsErrorNotDirect(t *testing.T) {
	for _, bad := range []string{"ftp://x", "http://", "socks5://:1080", "http://h:99999", "::::"} {
		setup(t, settings.Settings{Proxy: bad}, systemHTTP)
		u, _ := url.Parse("https://api.openai.com/")
		pu, err := For(u)
		if err == nil {
			t.Errorf("proxy %q: For returned %v, want an error", bad, pu)
		}
		if p, src := Describe(); p != "" || src != sourceNone {
			t.Errorf("proxy %q: Describe = (%q, %q), want (\"\", none)", bad, p, src)
		}
	}
}

// An environment proxy that is unusable is never parsed when NO_PROXY excludes
// the host: the request goes direct, as with any NO_PROXY match.
func TestNoProxyExcludesBeforeParsingEnvironment(t *testing.T) {
	setup(t, settings.Settings{}, systemProxy{})
	t.Setenv("HTTPS_PROXY", "ftp://x")
	t.Setenv("NO_PROXY", "api.openai.com")
	u, _ := url.Parse("https://api.openai.com/")
	pu, err := For(u)
	if err != nil || pu != nil {
		t.Fatalf("For = (%v, %v), want (nil, nil)", pu, err)
	}
}

func TestInvalidEnvironmentIsError(t *testing.T) {
	setup(t, settings.Settings{}, systemProxy{})
	t.Setenv("HTTPS_PROXY", "ftp://10.0.0.1:21")
	u, _ := url.Parse("https://api.openai.com/")
	if _, err := For(u); err == nil {
		t.Fatal("For with an ftp:// environment proxy should return an error")
	}
}

// Acceptance 10: no settings, no env, no system proxy is direct with source none.
func TestNoProxyAnywhereIsNone(t *testing.T) {
	setup(t, settings.Settings{}, systemProxy{})
	if got := forString(t, "https://api.openai.com/"); got != "DIRECT" {
		t.Fatalf("For = %s, want DIRECT", got)
	}
	if p, src := Describe(); p != "" || src != sourceNone {
		t.Fatalf("Describe = (%q, %q), want (\"\", none)", p, src)
	}
}

// Reading the system proxy on platforms without support yields nothing.
func TestOtherPlatformsReadNoSystemProxy(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("system proxy is read on this platform")
	}
	if sp := readPlatformProxy(); sp.URL != "" || len(sp.Bypass) != 0 {
		t.Fatalf("readPlatformProxy = %+v, want empty", sp)
	}
}

func TestSystemProxyCache(t *testing.T) {
	calls := 0
	oldRead := readSystemProxy
	readSystemProxy = func() systemProxy {
		calls++
		return systemHTTP
	}
	resetSystemCache()
	t.Cleanup(func() { readSystemProxy = oldRead; resetSystemCache() })

	currentSystemProxy()
	currentSystemProxy()
	if calls != 1 {
		t.Fatalf("reads = %d, want 1 within the TTL", calls)
	}
	sysMu.Lock()
	sysCached = time.Now().Add(-systemCacheTTL - time.Second)
	sysMu.Unlock()
	currentSystemProxy()
	if calls != 2 {
		t.Fatalf("reads = %d, want a re-read after the TTL", calls)
	}
}

func TestSystemCacheTTLWithinFifteenSeconds(t *testing.T) {
	if systemCacheTTL > 15*time.Second {
		t.Fatalf("systemCacheTTL = %v, must not exceed 15s", systemCacheTTL)
	}
}

func TestDescribeOffAndNoneHaveEmptyProxy(t *testing.T) {
	setup(t, settings.Settings{}, systemProxy{})
	if p, src := Describe(); p != "" || src != sourceNone {
		t.Fatalf("Describe = (%q, %q), want (\"\", none)", p, src)
	}
}

func TestInstallSetsDefaultTransport(t *testing.T) {
	setup(t, settings.Settings{Proxy: "http://127.0.0.1:7890"}, systemProxy{})
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("http.DefaultTransport is not an *http.Transport")
	}
	oldProxy := tr.Proxy
	t.Cleanup(func() { tr.Proxy = oldProxy })
	Install()
	Install() // second call is a no-op
	if tr.Proxy == nil {
		t.Fatal("Install did not set Transport.Proxy")
	}
	req, _ := http.NewRequest("GET", "https://api.openai.com/", nil)
	pu, err := tr.Proxy(req)
	if err != nil || pu == nil || pu.String() != "http://127.0.0.1:7890" {
		t.Fatalf("Transport.Proxy = (%v, %v), want http://127.0.0.1:7890", pu, err)
	}
	req, _ = http.NewRequest("GET", "http://127.0.0.1:3425/", nil)
	if pu, err := tr.Proxy(req); err != nil || pu != nil {
		t.Fatalf("loopback Transport.Proxy = (%v, %v), want (nil, nil)", pu, err)
	}
}
