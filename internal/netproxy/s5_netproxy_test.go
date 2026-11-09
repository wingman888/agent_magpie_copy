package netproxy

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// s5Env clears every proxy variable and points settings at a temp dir.
func s5Env(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
}

// s5System stubs what the operating system reports.
func s5System(t *testing.T, sys sysProxy) {
	t.Helper()
	old := systemSettings
	systemSettings = func() sysProxy { return sys }
	t.Cleanup(func() { systemSettings = old })
}

func s5For(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := For(u)
	if err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

func TestS5OrderSettingsThenEnvThenSystem(t *testing.T) {
	s5Env(t)
	s5System(t, sysProxy{HTTPS: "sys.example:443"})
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")

	if got := s5For(t, "https://api.example.com/v1"); got != "http://env.example:3128" {
		t.Fatalf("env should beat system: %q", got)
	}
	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	if got := s5For(t, "https://api.example.com/v1"); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("settings should beat env: %q", got)
	}
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if got := s5For(t, "https://api.example.com/v1"); got != "" {
		t.Fatalf("direct should connect directly: %q", got)
	}
}

func TestS5BareHostPortSettingIsHTTP(t *testing.T) {
	s5Env(t)
	if err := settings.Save(settings.Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if got := s5For(t, "https://api.example.com"); got != "http://127.0.0.1:7890" {
		t.Fatalf("bare host:port: %q", got)
	}
}

func TestS5SystemOrderHTTPSHTTPSocks(t *testing.T) {
	s5Env(t)
	s5System(t, sysProxy{HTTP: "h.example:80", SOCKS: "s.example:1080"})
	if got := s5For(t, "https://api.example.com"); got != "http://h.example:80" {
		t.Fatalf("HTTP before SOCKS: %q", got)
	}
	s5System(t, sysProxy{SOCKS: "s.example:1080"})
	if got := s5For(t, "https://api.example.com"); got != "socks5://s.example:1080" {
		t.Fatalf("SOCKS last resort: %q", got)
	}
}

func TestS5NoProxyAndSystemExceptions(t *testing.T) {
	s5Env(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	t.Setenv("NO_PROXY", ".corp.example, intranet.example")
	if got := s5For(t, "https://x.corp.example"); got != "" {
		t.Fatalf("NO_PROXY suffix: %q", got)
	}
	if got := s5For(t, "https://intranet.example"); got != "" {
		t.Fatalf("NO_PROXY host: %q", got)
	}
	if got := s5For(t, "https://api.example.com"); got != "http://env.example:3128" {
		t.Fatalf("not excluded: %q", got)
	}

	t.Setenv("HTTPS_PROXY", "")
	s5System(t, sysProxy{HTTPS: "sys.example:443", Except: []string{"*.local", "169.254/16", "10.0.0.5"}, Local: true})
	for _, raw := range []string{"https://printer.local", "https://169.254.3.4", "https://10.0.0.5", "https://nas"} {
		if got := s5For(t, raw); got != "" {
			t.Fatalf("%s should bypass the system proxy: %q", raw, got)
		}
	}
	if got := s5For(t, "https://api.example.com"); got != "http://sys.example:443" {
		t.Fatalf("system proxy: %q", got)
	}
}

func TestS5LoopbackIsAlwaysDirect(t *testing.T) {
	s5Env(t)
	s5System(t, sysProxy{HTTPS: "sys.example:443", Local: true})
	t.Setenv("HTTP_PROXY", "http://env.example:3128")
	t.Setenv("ALL_PROXY", "http://env.example:3128")
	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://127.0.0.1:3425/", "http://localhost:3425", "http://[::1]:3425", "http://127.5.6.7/x"} {
		if got := s5For(t, raw); got != "" {
			t.Fatalf("%s went through %q", raw, got)
		}
	}
	if got := s5For(t, "http://api.example.com"); got == "" {
		t.Fatal("remote host should still use the proxy")
	}
}

func TestS5ForRequestAndNil(t *testing.T) {
	s5Env(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	req, _ := http.NewRequest("GET", "https://api.example.com/v1/models", nil)
	p, err := ForRequest(req)
	if err != nil || p == nil || p.Host != "env.example:3128" {
		t.Fatalf("ForRequest: %v %v", p, err)
	}
	if p, err := For(nil); p != nil || err != nil {
		t.Fatalf("nil url: %v %v", p, err)
	}
}
