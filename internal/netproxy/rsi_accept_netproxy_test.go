package netproxy_test

import (
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
)

// Go reads the proxy environment once per process, so it is fixed here.
func TestMain(m *testing.M) {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"} {
		os.Unsetenv(k)
	}
	os.Setenv("HTTPS_PROXY", "http://127.0.0.1:9999")
	os.Setenv("HTTP_PROXY", "http://127.0.0.1:9999")
	os.Setenv("NO_PROXY", "skip.example.org")
	os.Exit(m.Run())
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func str(u *url.URL) string {
	if u == nil {
		return "<nil>"
	}
	return u.String()
}

func TestAcceptParse(t *testing.T) {
	for in, want := range map[string]string{
		"1.2.3.4:8080":       "http://1.2.3.4:8080",
		"  127.0.0.1:7890  ": "http://127.0.0.1:7890",
		"socks5://h:1":       "socks5://h:1",
		"https://h:2":        "https://h:2",
	} {
		got, err := netproxy.Parse(in)
		if err != nil || got.String() != want {
			t.Errorf("Parse(%q) = %v, %v; want %s", in, got, err, want)
		}
	}
}

func TestAcceptForSettingsPriority(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9999")
	u := mustURL(t, "https://chatgpt.com/backend-api/codex/responses")

	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	if p, err := netproxy.For(u); err != nil || str(p) != "socks5://127.0.0.1:1080" {
		t.Fatalf("settings: %v %v", p, err)
	}
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if p, err := netproxy.For(u); err != nil || p != nil {
		t.Fatalf("direct: %v %v", p, err)
	}
	if err := settings.Save(settings.Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if p, err := netproxy.For(u); err != nil || str(p) != "http://127.0.0.1:7890" {
		t.Fatalf("host:port: %v %v", p, err)
	}
}

func TestAcceptForEnvironment(t *testing.T) {
	isolate(t)
	u := mustURL(t, "https://chatgpt.com/x")
	if p, err := netproxy.For(u); err != nil || str(p) != "http://127.0.0.1:9999" {
		t.Fatalf("env: %v %v", p, err)
	}
	if p, err := netproxy.For(mustURL(t, "https://skip.example.org/")); err != nil || p != nil {
		t.Fatalf("no_proxy: %v %v", p, err)
	}
	if p, _ := netproxy.For(mustURL(t, "https://other.example.org/")); str(p) != "http://127.0.0.1:9999" {
		t.Fatalf("other host: %v", p)
	}
}

func TestAcceptFuncLoopback(t *testing.T) {
	isolate(t)
	if err := settings.Save(settings.Settings{Proxy: "http://127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"http://127.0.0.1:3425", "http://localhost:3425/v1", "http://[::1]:3425/",
		"http://foo.localhost/", "https://127.0.0.1/", "http://127.1.2.3:80",
	} {
		req := &http.Request{URL: mustURL(t, s)}
		if p, err := netproxy.Func(req); p != nil || err != nil {
			t.Errorf("%s: %v %v", s, p, err)
		}
	}
	req := &http.Request{URL: mustURL(t, "https://chatgpt.com/")}
	if p, err := netproxy.Func(req); err != nil || str(p) != "http://127.0.0.1:7890" {
		t.Errorf("non-loopback: %v %v", p, err)
	}
}

func TestAcceptDescribe(t *testing.T) {
	isolate(t)
	check := func(wp, ws string) {
		t.Helper()
		if p, s := netproxy.Describe(); p != wp || s != ws {
			t.Fatalf("Describe = (%q, %q), want (%q, %q)", p, s, wp, ws)
		}
	}
	check("http://127.0.0.1:9999", "environment")
	settings.Save(settings.Settings{Proxy: "  socks5://127.0.0.1:1080 "})
	check("socks5://127.0.0.1:1080", "settings")
	settings.Save(settings.Settings{Proxy: "direct"})
	check("", "off")
}

func TestAcceptInstall(t *testing.T) {
	isolate(t)
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	netproxy.Install()
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatalf("DefaultTransport not a Transport with Proxy: %T", http.DefaultTransport)
	}
	p, err := tr.Proxy(&http.Request{URL: mustURL(t, "https://chatgpt.com/")})
	if err != nil || str(p) != "http://127.0.0.1:9999" {
		t.Fatalf("remote: %v %v", p, err)
	}
	p, err = tr.Proxy(&http.Request{URL: mustURL(t, "http://localhost:3425/")})
	if err != nil || p != nil {
		t.Fatalf("loopback: %v %v", p, err)
	}
	settings.Save(settings.Settings{Proxy: "direct"})
	if p, _ := tr.Proxy(&http.Request{URL: mustURL(t, "https://chatgpt.com/")}); p != nil {
		t.Fatalf("direct: %v", p)
	}
}

func TestAcceptSystemNoPanic(t *testing.T) {
	isolate(t)
	p := netproxy.System()
	_ = p.URL
	_ = p.Bypass
}
