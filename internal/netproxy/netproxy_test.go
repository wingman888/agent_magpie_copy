package netproxy

import (
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// isolate clears every proxy variable and points settings at a temp folder.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	old := systemProxyFunc
	systemProxyFunc = func() sysProxy { return sysProxy{} }
	t.Cleanup(func() { systemProxyFunc = old })
}

func setProxy(t *testing.T, p string) {
	t.Helper()
	if err := settings.Save(settings.Settings{Proxy: p}); err != nil {
		t.Fatal(err)
	}
}

func mustFor(t *testing.T, raw string) string {
	t.Helper()
	u, _ := url.Parse(raw)
	p, err := For(u)
	if err != nil {
		t.Fatalf("For(%s): %v", raw, err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

func TestDirectWithoutAnything(t *testing.T) {
	isolate(t)
	if got := mustFor(t, "https://api.example.com/v1"); got != "" {
		t.Fatalf("got %q, want direct", got)
	}
}

func TestSettingsProxyWins(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	setProxy(t, "socks5://127.0.0.1:1080")
	if got := mustFor(t, "https://api.example.com"); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("got %q", got)
	}
	setProxy(t, "127.0.0.1:7890")
	if got := mustFor(t, "https://api.example.com"); got != "http://127.0.0.1:7890" {
		t.Fatalf("bare host:port: got %q", got)
	}
}

func TestDirectSettingIgnoresEnvAndSystem(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	systemProxyFunc = func() sysProxy { return sysProxy{HTTPS: "sys.example:8443"} }
	setProxy(t, "direct")
	if got := mustFor(t, "https://api.example.com"); got != "" {
		t.Fatalf("got %q, want direct", got)
	}
}

func TestEnvironment(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	t.Setenv("HTTP_PROXY", "http://plain.example:3128")
	if got := mustFor(t, "https://api.example.com"); got != "http://env.example:3128" {
		t.Fatalf("https: got %q", got)
	}
	if got := mustFor(t, "http://api.example.com"); got != "http://plain.example:3128" {
		t.Fatalf("http: got %q", got)
	}
	t.Setenv("ALL_PROXY", "socks5://all.example:1080")
	t.Setenv("HTTPS_PROXY", "")
	if got := mustFor(t, "https://api.example.com"); got != "socks5://all.example:1080" {
		t.Fatalf("ALL_PROXY: got %q", got)
	}
}

func TestEnvironmentBeatsSystem(t *testing.T) {
	isolate(t)
	systemProxyFunc = func() sysProxy { return sysProxy{HTTPS: "sys.example:8443"} }
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	if got := mustFor(t, "https://api.example.com"); got != "http://env.example:3128" {
		t.Fatalf("got %q", got)
	}
}

func TestNoProxy(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	t.Setenv("NO_PROXY", "localhost, .corp.example,10.0.0.0/8")
	for _, raw := range []string{"https://a.corp.example", "https://corp.example", "https://10.1.2.3", "https://localhost:8080"} {
		if got := mustFor(t, raw); got != "" {
			t.Fatalf("%s: got %q, want direct", raw, got)
		}
	}
	if got := mustFor(t, "https://api.example.com"); got != "http://env.example:3128" {
		t.Fatalf("not excluded: got %q", got)
	}
	t.Setenv("NO_PROXY", "*")
	if got := mustFor(t, "https://api.example.com"); got != "" {
		t.Fatalf("*: got %q", got)
	}
}

func TestNoProxyAppliesToSystemProxy(t *testing.T) {
	isolate(t)
	systemProxyFunc = func() sysProxy { return sysProxy{HTTP: "sys.example:8080"} }
	t.Setenv("NO_PROXY", "intranet.lan")
	if got := mustFor(t, "http://intranet.lan/x"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestLoopbackNeverProxied(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	setProxy(t, "socks5://127.0.0.1:1080")
	systemProxyFunc = func() sysProxy { return sysProxy{HTTP: "sys.example:8080"} }
	for _, raw := range []string{"http://127.0.0.1:3425/", "http://127.0.0.2/", "http://[::1]:3425/", "http://localhost:3425/", "http://foo.localhost/"} {
		if got := mustFor(t, raw); got != "" {
			t.Fatalf("%s: got %q, want direct", raw, got)
		}
	}
}

func TestSystemProxyPriority(t *testing.T) {
	isolate(t)
	systemProxyFunc = func() sysProxy {
		return sysProxy{HTTPS: "https-proxy.example:443", HTTP: "http-proxy.example:80", SOCKS: "socks.example:1080"}
	}
	if got := mustFor(t, "https://api.example.com"); got != "http://https-proxy.example:443" {
		t.Fatalf("HTTPS first: got %q", got)
	}
	systemProxyFunc = func() sysProxy { return sysProxy{HTTP: "http-proxy.example:80", SOCKS: "socks.example:1080"} }
	if got := mustFor(t, "https://api.example.com"); got != "http://http-proxy.example:80" {
		t.Fatalf("HTTP second: got %q", got)
	}
	systemProxyFunc = func() sysProxy { return sysProxy{SOCKS: "socks.example:1080"} }
	if got := mustFor(t, "https://api.example.com"); got != "socks5://socks.example:1080" {
		t.Fatalf("SOCKS last: got %q", got)
	}
}

func TestSystemExceptions(t *testing.T) {
	isolate(t)
	systemProxyFunc = func() sysProxy {
		return sysProxy{HTTPS: "sys.example:8443", Exceptions: []string{"*.corp.example", "192.168.*", "<local>"}}
	}
	for _, raw := range []string{"https://a.corp.example", "https://192.168.1.5", "https://intranet"} {
		if got := mustFor(t, raw); got != "" {
			t.Fatalf("%s: got %q, want direct", raw, got)
		}
	}
	if got := mustFor(t, "https://api.example.com"); got != "http://sys.example:8443" {
		t.Fatalf("outside exceptions: got %q", got)
	}
	if got := mustFor(t, "https://intranet.lan"); got != "http://sys.example:8443" {
		t.Fatalf("dotted name is not local: got %q", got)
	}
}

func TestMatchList(t *testing.T) {
	cases := []struct {
		host string
		list string
		want bool
	}{
		{"example.com", "example.com", true},
		{"a.example.com", "example.com", false},
		{"a.example.com", ".example.com", true},
		{"example.com", ".example.com", true},
		{"a.example.com", "*.example.com", true},
		{"badexample.com", ".example.com", false},
		{"10.1.2.3", "10.0.0.0/8", true},
		{"11.1.2.3", "10.0.0.0/8", false},
		{"192.168.9.9", "192.168.*", true},
		{"host", "<local>", true},
		{"host.lan", "<local>", false},
		{"example.com", "example.com:443", true},
		{"example.com", "", false},
		{"EXAMPLE.com", "example.COM", true},
	}
	for _, c := range cases {
		if got := matchList(c.host, []string{c.list}); got != c.want {
			t.Errorf("matchList(%q, %q) = %v, want %v", c.host, c.list, got, c.want)
		}
	}
}

func TestProxyFuncAndClient(t *testing.T) {
	isolate(t)
	setProxy(t, "socks5://127.0.0.1:1080")
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/v1", nil)
	p, err := ProxyFunc(req)
	if err != nil || p == nil || p.String() != "socks5://127.0.0.1:1080" {
		t.Fatalf("ProxyFunc: %v %v", p, err)
	}
	if Client().Transport == nil {
		t.Fatal("client has no transport")
	}
	if tr := Transport(); tr.Proxy == nil {
		t.Fatal("transport without proxy")
	}
}

func TestInvalidSettingProxy(t *testing.T) {
	isolate(t)
	// Save refuses ftp://, but a hand-edited file can hold it: For reports it
	// instead of quietly going direct.
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.Path(), []byte(`{"proxy":"ftp://x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://api.example.com")
	if _, err := For(u); err == nil {
		t.Fatal("ftp proxy in settings was not reported")
	}
}

func TestParseScutil(t *testing.T) {
	out := `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
  }
  ExcludeSimpleHostnames : 1
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 0
  HTTPSProxy : 10.0.0.9
  HTTPSPort : 8443
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	sp := parseScutil(out)
	if sp.HTTP != "127.0.0.1:7890" || sp.HTTPS != "" || sp.SOCKS != "127.0.0.1:1080" {
		t.Fatalf("hosts: %+v", sp)
	}
	want := []string{"*.local", "169.254/16", "<local>"}
	if len(sp.Exceptions) != len(want) {
		t.Fatalf("exceptions: %v", sp.Exceptions)
	}
	for i := range want {
		if sp.Exceptions[i] != want[i] {
			t.Fatalf("exceptions: %v", sp.Exceptions)
		}
	}
}

func TestParseWindowsProxy(t *testing.T) {
	sp := parseWindowsProxy(false, "127.0.0.1:7890", "localhost;<local>")
	if sp.HTTP != "" || len(sp.Exceptions) != 0 {
		t.Fatalf("disabled: %+v", sp)
	}
	sp = parseWindowsProxy(true, "http=h1:80;https=h2:443;socks=h3:1080", "*.corp.example;<local>")
	if sp.HTTP != "h1:80" || sp.HTTPS != "h2:443" || sp.SOCKS != "h3:1080" {
		t.Fatalf("per protocol: %+v", sp)
	}
	if len(sp.Exceptions) != 2 || sp.Exceptions[1] != "<local>" {
		t.Fatalf("override: %v", sp.Exceptions)
	}
	sp = parseWindowsProxy(true, "proxy.example:8080", "")
	if sp.HTTP != "proxy.example:8080" || len(sp.Exceptions) != 0 {
		t.Fatalf("single: %+v", sp)
	}
}
