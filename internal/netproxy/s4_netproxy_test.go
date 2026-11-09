package netproxy

import (
	"net/http"
	"net/url"
	"testing"
)

// s4Env clears every proxy variable and fixes the settings and system layers,
// so each test sees only what it sets.
func s4Env(t *testing.T, proxy string, sys System) {
	t.Helper()
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	oldS, oldSys := settingsProxy, systemProxy
	t.Cleanup(func() { settingsProxy, systemProxy = oldS, oldSys })
	settingsProxy = func() string { return proxy }
	systemProxy = func() System { return sys }
}

func TestS4ForOrder(t *testing.T) {
	sys := System{HTTPS: "http://sys-https:1", HTTP: "http://sys-http:2", SOCKS: "socks5://sys-socks:3"}
	u, _ := url.Parse("https://api.example.com/v1")

	s4Env(t, "", sys)
	got, err := For(u)
	if err != nil || got == nil || got.String() != "http://sys-https:1" {
		t.Fatalf("system HTTPS first: %v %v", got, err)
	}

	s4Env(t, "", System{SOCKS: "socks5://sys-socks:3"})
	if got, _ := For(u); got == nil || got.String() != "socks5://sys-socks:3" {
		t.Fatalf("system SOCKS last resort: %v", got)
	}

	t.Setenv("HTTPS_PROXY", "127.0.0.1:9999")
	if got, _ := For(u); got == nil || got.String() != "http://127.0.0.1:9999" {
		t.Fatalf("env beats system: %v", got)
	}

	settingsProxy = func() string { return "socks5://127.0.0.1:1080" }
	if got, _ := For(u); got == nil || got.String() != "socks5://127.0.0.1:1080" {
		t.Fatalf("settings beats env: %v", got)
	}

	settingsProxy = func() string { return "direct" }
	if got, err := For(u); got != nil || err != nil {
		t.Fatalf("direct: %v %v", got, err)
	}

	settingsProxy = func() string { return "ftp://x" }
	if _, err := For(u); err == nil {
		t.Fatal("invalid settings proxy must be an error")
	}
}

func TestS4ForLoopbackNeverProxied(t *testing.T) {
	s4Env(t, "socks5://127.0.0.1:1080", System{HTTPS: "http://sys:1"})
	t.Setenv("ALL_PROXY", "http://env:2")
	for _, raw := range []string{"http://127.0.0.1:3425/", "http://localhost:3425/", "http://[::1]:3425/", "http://127.9.9.9/"} {
		u, _ := url.Parse(raw)
		if got, err := For(u); got != nil || err != nil {
			t.Fatalf("%s proxied: %v %v", raw, got, err)
		}
	}
}

func TestS4ForNoProxyWildcards(t *testing.T) {
	s4Env(t, "", System{HTTPS: "http://sys:1"})
	t.Setenv("NO_PROXY", ".corp.example, *.internal, 10.0.0.0/8, example.org:8443")
	t.Setenv("HTTPS_PROXY", "http://env:2")
	cases := map[string]bool{
		"https://a.corp.example/":    false, // subdomain of .corp.example: direct
		"https://corp.example/":      false,
		"https://api.internal/":      false,
		"https://10.1.2.3/":          false,
		"https://example.org:8443/":  false,
		"https://example.org/":       true,
		"https://public.example.com": true,
	}
	for raw, proxied := range cases {
		u, _ := url.Parse(raw)
		got, err := For(u)
		if err != nil {
			t.Fatal(err)
		}
		if (got != nil) != proxied {
			t.Fatalf("%s: proxied=%v, want %v (%v)", raw, got != nil, proxied, got)
		}
	}
}

func TestS4SystemExceptions(t *testing.T) {
	sys := System{
		HTTPS:        "http://sys:7890",
		Exceptions:   []string{"*.local", "169.254/16", "192.168.*", "*.intranet.example"},
		ExcludePlain: true,
	}
	s4Env(t, "", sys)
	cases := map[string]bool{
		"https://printer.local/":         false,
		"https://intra.example.com/":     true,
		"https://169.254.3.4/":           false,
		"https://192.168.1.9/":           false,
		"https://wiki.intranet.example/": false,
		"https://nodot/":                 false, // <local> / simple hostname
		"https://api.openai.com/":        true,
	}
	for raw, proxied := range cases {
		u, _ := url.Parse(raw)
		got, err := For(u)
		if err != nil {
			t.Fatal(err)
		}
		if (got != nil) != proxied {
			t.Fatalf("%s: proxied=%v, want %v", raw, got != nil, proxied)
		}
	}
}

func TestS4ProxyFuncSocks5h(t *testing.T) {
	s4Env(t, "socks5h://127.0.0.1:1080", System{})
	r, _ := http.NewRequest("GET", "https://api.example.com/", nil)
	got, err := ProxyFunc(r)
	if err != nil || got == nil || got.Scheme != "socks5" || got.Host != "127.0.0.1:1080" {
		t.Fatalf("socks5h handed to transport as %v %v", got, err)
	}
}

func TestS4ParseScutil(t *testing.T) {
	out := `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
  }
  ExcludeSimpleHostnames : 1
  HTTPEnable : 0
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	sys := parseScutil(out)
	if sys.HTTPS != "http://127.0.0.1:7890" || sys.HTTP != "" || sys.SOCKS != "socks5://127.0.0.1:1080" {
		t.Fatalf("protocols: %+v", sys)
	}
	if len(sys.Exceptions) != 2 || sys.Exceptions[1] != "169.254/16" || !sys.ExcludePlain {
		t.Fatalf("exceptions: %+v", sys)
	}
}

func TestS4ParseRegistry(t *testing.T) {
	out := `
HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Internet Settings
    ProxyEnable    REG_DWORD    0x1
    ProxyServer    REG_SZ    http=10.0.0.2:8080;https=10.0.0.3:8443;socks=10.0.0.4:1080
    ProxyOverride    REG_SZ    *.local;192.168.*;<local>
`
	sys := parseRegistry(out)
	if sys.HTTP != "http://10.0.0.2:8080" || sys.HTTPS != "http://10.0.0.3:8443" || sys.SOCKS != "socks5://10.0.0.4:1080" {
		t.Fatalf("servers: %+v", sys)
	}
	if !sys.ExcludePlain || len(sys.Exceptions) != 2 {
		t.Fatalf("exceptions: %+v", sys)
	}
	if off := parseRegistry("    ProxyEnable    REG_DWORD    0x0\n    ProxyServer    REG_SZ    1.2.3.4:1\n"); off.HTTPS != "" {
		t.Fatal("disabled system proxy used")
	}
}
