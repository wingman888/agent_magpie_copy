package netproxy

import (
	"net/url"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// isolate points settings at an empty folder, clears every proxy variable,
// and replaces the system reader, so the host's real proxy never matters.
func isolate(t *testing.T, sys system) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, n := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(n, "")
	}
	old := readSystem
	readSystem = func() system { return sys }
	sysMu.Lock()
	sysAt = time.Time{}
	sysMu.Unlock()
	t.Cleanup(func() {
		readSystem = old
		sysMu.Lock()
		sysAt = time.Time{}
		sysMu.Unlock()
	})
}

func forURL(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := For(u)
	if err != nil {
		t.Fatalf("For(%s): %v", raw, err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

func TestLoopbackNeverProxied(t *testing.T) {
	isolate(t, system{https: "10.0.0.1:8080"})
	t.Setenv("HTTPS_PROXY", "http://10.0.0.2:3128")
	for _, raw := range []string{"https://127.0.0.1:3425/", "http://localhost:3425/", "http://[::1]:3425/", "http://127.4.5.6/"} {
		if got := forURL(t, raw); got != "" {
			t.Errorf("%s via %s, want direct", raw, got)
		}
	}
}

func TestSettingsBeatsEnvAndSystem(t *testing.T) {
	isolate(t, system{https: "10.0.0.1:8080"})
	t.Setenv("HTTPS_PROXY", "http://10.0.0.2:3128")
	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	if got := forURL(t, "https://api.example.com/v1"); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("got %q", got)
	}
	if err := settings.Save(settings.Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if got := forURL(t, "https://api.example.com/v1"); got != "http://127.0.0.1:7890" {
		t.Fatalf("bare host:port got %q", got)
	}
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if got := forURL(t, "https://api.example.com/v1"); got != "" {
		t.Fatalf("direct got %q", got)
	}
}

func TestEnvBeatsSystem(t *testing.T) {
	isolate(t, system{https: "10.0.0.1:8080"})
	t.Setenv("HTTPS_PROXY", "http://10.0.0.2:3128")
	if got := forURL(t, "https://api.example.com/"); got != "http://10.0.0.2:3128" {
		t.Fatalf("got %q", got)
	}
	// the env proxy is per scheme: an http request with only HTTPS_PROXY falls through
	if got := forURL(t, "http://api.example.com/"); got != "http://10.0.0.1:8080" {
		t.Fatalf("http request got %q", got)
	}
}

func TestNoProxyEnv(t *testing.T) {
	isolate(t, system{https: "10.0.0.1:8080"})
	t.Setenv("HTTPS_PROXY", "http://10.0.0.2:3128")
	t.Setenv("NO_PROXY", "example.org, .corp.example,10.1.0.0/16")
	for _, raw := range []string{"https://example.org/", "https://a.corp.example/", "https://corp.example/", "https://10.1.2.3/"} {
		if got := forURL(t, raw); got != "" {
			t.Errorf("%s via %s, want direct", raw, got)
		}
	}
	if got := forURL(t, "https://api.example.com/"); got != "http://10.0.0.2:3128" {
		t.Errorf("unlisted host got %q", got)
	}
	t.Setenv("NO_PROXY", "*")
	if got := forURL(t, "https://api.example.com/"); got != "" {
		t.Errorf("* got %q", got)
	}
}

func TestSystemOrderAndExceptions(t *testing.T) {
	isolate(t, system{http: "10.0.0.3:80", socks: "10.0.0.4:1080"})
	if got := forURL(t, "https://api.example.com/"); got != "http://10.0.0.3:80" {
		t.Fatalf("HTTP before SOCKS: got %q", got)
	}
	isolate(t, system{socks: "10.0.0.4:1080"})
	if got := forURL(t, "https://api.example.com/"); got != "socks5://10.0.0.4:1080" {
		t.Fatalf("SOCKS only: got %q", got)
	}
	isolate(t, system{https: "10.0.0.1:8080", http: "10.0.0.3:80", exceptions: []string{"*.corp.example", "192.168.*", "<local>"}, excludeLocal: false})
	for _, raw := range []string{"https://wiki.corp.example/", "http://192.168.1.9/", "https://intranet/"} {
		if got := forURL(t, raw); got != "" {
			t.Errorf("%s via %s, want direct", raw, got)
		}
	}
	if got := forURL(t, "https://api.example.com/"); got != "http://10.0.0.1:8080" {
		t.Errorf("public host got %q", got)
	}
	isolate(t, system{https: "10.0.0.1:8080", excludeLocal: true})
	if got := forURL(t, "https://intranet/"); got != "" {
		t.Errorf("ExcludeSimpleHostnames: got %q", got)
	}
}

func TestNoSettingsNoProxy(t *testing.T) {
	isolate(t, system{})
	if got := forURL(t, "https://api.example.com/"); got != "" {
		t.Fatalf("got %q", got)
	}
	if p, err := For(nil); p != nil || err != nil {
		t.Fatal("nil URL should be direct")
	}
}

func TestMatchHostAndPadCIDR(t *testing.T) {
	if !matchHost("a.corp.example", ".corp.example") || !matchHost("a.corp.example", "*.corp.example") {
		t.Fatal("suffix")
	}
	if matchHost("notcorp.example", "corp.example") {
		t.Fatal("suffix must respect the dot boundary")
	}
	if matchHost("example.org", "example.com") {
		t.Fatal("unrelated host matched")
	}
	if padCIDR("169.254/16") != "169.254.0.0/16" {
		t.Fatal(padCIDR("169.254/16"))
	}
}
