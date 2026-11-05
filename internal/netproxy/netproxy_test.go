package netproxy

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func mustParse(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func noSys() sysConf { return sysConf{} }

func sysWith(proxy string, exc ...string) func() sysConf {
	return func() sysConf { return sysConf{proxy: mustURL(proxy), exceptions: exc} }
}

// check runs decide and compares the proxy and source.
func check(t *testing.T, raw, set string, env map[string]string, sys func() sysConf, wantProxy, wantSource string) {
	t.Helper()
	p, src, err := decide(mustParse(t, raw), set, envOf(env), sys)
	if err != nil {
		t.Fatalf("decide(%s): unexpected error %v", raw, err)
	}
	got := ""
	if p != nil {
		got = p.String()
	}
	if got != wantProxy || src != wantSource {
		t.Fatalf("decide(%s, set=%q) = (%q, %q), want (%q, %q)", raw, set, got, src, wantProxy, wantSource)
	}
}

func TestSystemProxyUsed(t *testing.T) {
	check(t, "https://chatgpt.com/backend", "", nil,
		sysWith("http://127.0.0.1:7890"), "http://127.0.0.1:7890", "system")
}

func TestSystemExceptionDirect(t *testing.T) {
	sys := sysWith("http://127.0.0.1:7890", "*.corp")
	check(t, "https://git.corp/x", "", nil, sys, "", "none")
	check(t, "https://chatgpt.com/", "", nil, sys, "http://127.0.0.1:7890", "system")
}

func TestSettingsSocksWins(t *testing.T) {
	env := map[string]string{"HTTPS_PROXY": "http://10.0.0.1:8080"}
	check(t, "https://chatgpt.com/", "socks5://127.0.0.1:1080", env,
		sysWith("http://127.0.0.1:7890"), "socks5://127.0.0.1:1080", "settings")
}

func TestSettingsBareHostPortIsHTTP(t *testing.T) {
	check(t, "https://chatgpt.com/", "127.0.0.1:7890", nil, noSys,
		"http://127.0.0.1:7890", "settings")
}

func TestSettingsDirect(t *testing.T) {
	env := map[string]string{"HTTPS_PROXY": "http://10.0.0.1:8080"}
	check(t, "https://chatgpt.com/", "direct", env,
		sysWith("http://127.0.0.1:7890"), "", "off")
	check(t, "https://chatgpt.com/", " DIRECT ", env,
		sysWith("http://127.0.0.1:7890"), "", "off")
}

func TestEnvironmentBeforeSystem(t *testing.T) {
	env := map[string]string{"HTTPS_PROXY": "http://10.0.0.1:8080"}
	check(t, "https://chatgpt.com/", "", env,
		sysWith("http://127.0.0.1:7890"), "http://10.0.0.1:8080", "environment")
}

func TestEnvironmentLowercaseAndAll(t *testing.T) {
	check(t, "https://chatgpt.com/", "", map[string]string{"https_proxy": "http://10.0.0.2:8080"},
		noSys, "http://10.0.0.2:8080", "environment")
	check(t, "https://chatgpt.com/", "", map[string]string{"ALL_PROXY": "socks5://10.0.0.3:1080"},
		noSys, "socks5://10.0.0.3:1080", "environment")
}

func TestEnvironmentNoProxyDirect(t *testing.T) {
	env := map[string]string{"HTTPS_PROXY": "http://10.0.0.1:8080", "NO_PROXY": ".example.org, chatgpt.com"}
	check(t, "https://chatgpt.com/", "", env, sysWith("http://127.0.0.1:7890"), "", "environment")
	check(t, "https://api.example.org/", "", env, noSys, "", "environment")
	check(t, "https://other.net/", "", env, noSys, "http://10.0.0.1:8080", "environment")
}

func TestLoopbackAlwaysDirect(t *testing.T) {
	env := map[string]string{"HTTPS_PROXY": "http://10.0.0.1:8080", "HTTP_PROXY": "http://10.0.0.1:8080"}
	sys := sysWith("http://127.0.0.1:7890")
	for _, raw := range []string{
		"http://127.0.0.1:3425/x", "http://localhost/", "http://foo.localhost:9/",
		"http://[::1]:3425/", "http://127.9.8.7/",
	} {
		for _, set := range []string{"", "socks5://127.0.0.1:1080", "127.0.0.1:7890"} {
			p, _, err := decide(mustParse(t, raw), set, envOf(env), sys)
			if err != nil || p != nil {
				t.Fatalf("decide(%s, set=%q) = (%v, %v), want direct", raw, set, p, err)
			}
		}
	}
}

func TestInvalidSettingIsError(t *testing.T) {
	_, _, err := decide(mustParse(t, "https://chatgpt.com/"), "ftp://x", envOf(nil), sysWith("http://127.0.0.1:7890"))
	if err == nil {
		t.Fatal("ftp://x accepted; want error, not a quiet direct connection")
	}
	if !strings.Contains(err.Error(), settings.ProxyExample) {
		t.Fatalf("error lacks example: %v", err)
	}
}

func TestMacParseHTTPSBeforeHTTPBeforeSocks(t *testing.T) {
	const all = `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
  }
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
	sc := parseScutil(all)
	if sc.proxy == nil || sc.proxy.String() != "http://127.0.0.1:7890" {
		t.Fatalf("HTTPS not preferred: %v", sc.proxy)
	}
	if !matchException("host.local", sc.exceptions) || !matchException("169.254.3.4", sc.exceptions) {
		t.Fatalf("exceptions not parsed: %v", sc.exceptions)
	}
}

func TestMacParseHTTPWhenNoHTTPS(t *testing.T) {
	sc := parseScutil("<dictionary> {\n  HTTPEnable : 1\n  HTTPPort : 8080\n  HTTPProxy : 10.0.0.1\n  HTTPSEnable : 0\n  HTTPSProxy : 1.2.3.4\n  HTTPSPort : 1\n}")
	if sc.proxy == nil || sc.proxy.String() != "http://10.0.0.1:8080" {
		t.Fatalf("got %v", sc.proxy)
	}
}

func TestMacParseSocksOnly(t *testing.T) {
	sc := parseScutil("<dictionary> {\n  SOCKSEnable : 1\n  SOCKSPort : 1080\n  SOCKSProxy : 127.0.0.1\n}")
	if sc.proxy == nil || sc.proxy.String() != "socks5://127.0.0.1:1080" {
		t.Fatalf("got %v", sc.proxy)
	}
}

func TestMacParseAllDisabled(t *testing.T) {
	sc := parseScutil("<dictionary> {\n  HTTPEnable : 0\n  HTTPProxy : 10.0.0.1\n  HTTPPort : 8080\n  HTTPSEnable : 0\n  SOCKSEnable : 0\n}")
	if sc.proxy != nil {
		t.Fatalf("disabled proxy used: %v", sc.proxy)
	}
}

func TestWindowsServerForms(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:7890": "http://127.0.0.1:7890",
		"http=10.0.0.1:8080;https=127.0.0.1:7890": "http://127.0.0.1:7890",
		"http=10.0.0.1:8080":                      "http://10.0.0.1:8080",
		"socks=127.0.0.1:1080":                    "socks5://127.0.0.1:1080",
		"http=10.0.0.1:8080;socks=127.0.0.1:1080": "http://10.0.0.1:8080",
		"": "",
	}
	for in, want := range cases {
		got := winServer(in)
		if got != want {
			t.Errorf("winServer(%q) = %q, want %q", in, got, want)
		}
	}
	sc := winSystem("127.0.0.1:7890", "*.corp;<local>")
	if sc.proxy == nil || sc.proxy.String() != "http://127.0.0.1:7890" {
		t.Fatalf("winSystem proxy = %v", sc.proxy)
	}
	for host, want := range map[string]bool{"git.corp": true, "intranet": true, "chatgpt.com": false} {
		if got := matchException(host, sc.exceptions); got != want {
			t.Errorf("matchException(%q) = %v, want %v", host, got, want)
		}
	}
	check(t, "https://git.corp/x", "", nil, func() sysConf { return sc }, "", "none")
	check(t, "https://chatgpt.com/", "", nil, func() sysConf { return sc }, "http://127.0.0.1:7890", "system")
}

func TestExceptionMatching(t *testing.T) {
	list := []string{"", "Example.COM", "*.corp", ".internal", "<local>"}
	for host, want := range map[string]bool{
		"example.com":     true,
		"EXAMPLE.com":     true,
		"a.corp":          true,
		"corp":            true,
		"x.internal":      true,
		"internal":        true,
		"intranet":        true,
		"notexample.com":  false,
		"sub.example.com": false,
		"chatgpt.com":     false,
	} {
		if got := matchException(host, list); got != want {
			t.Errorf("matchException(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestNoSystemOrEnvironmentIsNone(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("reads the real system proxy")
	}
	check(t, "https://chatgpt.com/", "", map[string]string{}, func() sysConf { return readSystem() }, "", "none")
}

func TestDescribe(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		p, src := Describe()
		if p != "" || src != "none" {
			t.Fatalf("Describe() = (%q, %q), want (\"\", none)", p, src)
		}
	}

	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	if p, src := Describe(); p != "socks5://127.0.0.1:1080" || src != "settings" {
		t.Fatalf("Describe() = (%q, %q)", p, src)
	}

	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:8080")
	if p, src := Describe(); p != "" || src != "off" {
		t.Fatalf("Describe() = (%q, %q), want (\"\", off)", p, src)
	}

	if err := settings.Save(settings.Settings{}); err != nil {
		t.Fatal(err)
	}
	if p, src := Describe(); p != "http://10.0.0.1:8080" || src != "environment" {
		t.Fatalf("Describe() = (%q, %q)", p, src)
	}
}

func TestForReadsSettingsEachCall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	if err := settings.Save(settings.Settings{Proxy: "127.0.0.1:7001"}); err != nil {
		t.Fatal(err)
	}
	p, err := For(mustParse(t, "https://chatgpt.com/"))
	if err != nil || p == nil || p.String() != "http://127.0.0.1:7001" {
		t.Fatalf("For = (%v, %v)", p, err)
	}
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	p, err = For(mustParse(t, "https://chatgpt.com/"))
	if err != nil || p != nil {
		t.Fatalf("after direct, For = (%v, %v)", p, err)
	}
}

func TestInvalidSettingsFileFailsRequests(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "magpie", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"proxy":"ftp://x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := For(mustParse(t, "https://chatgpt.com/")); err == nil {
		t.Fatal("hand-edited invalid proxy went direct")
	}
}

func TestInstallIdempotent(t *testing.T) {
	Install()
	Install()
	Install()
	// Install only sets Proxy on *http.Transport defaults; the function itself
	// must delegate to For.
	p, err := proxyFunc(&http.Request{URL: mustParse(t, "http://localhost/")})
	if err != nil || p != nil {
		t.Fatalf("proxyFunc(localhost) = (%v, %v)", p, err)
	}
}
