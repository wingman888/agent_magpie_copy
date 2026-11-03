package netproxy

import (
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

func TestParseScutil(t *testing.T) {
	out := `<dictionary> {
  ExceptionsList : <array> {
    0 : 127.0.0.1
    1 : *.local
  }
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7891
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 7892
  SOCKSProxy : 127.0.0.1
}`
	p := parseScutil(out)
	if p.URL != "http://127.0.0.1:7891" || len(p.Bypass) != 2 || p.Bypass[1] != "*.local" {
		t.Fatalf("%+v", p)
	}
	if p := parseScutil("<dictionary> {\n  SOCKSEnable : 1\n  SOCKSPort : 1080\n  SOCKSProxy : 10.0.0.2\n}"); p.URL != "socks5://10.0.0.2:1080" {
		t.Fatalf("socks: %+v", p)
	}
	if p := parseScutil("<dictionary> {\n  HTTPEnable : 0\n  HTTPProxy : x\n}"); p.URL != "" {
		t.Fatalf("off: %+v", p)
	}
}

func TestParseWindows(t *testing.T) {
	for server, want := range map[string]string{
		"127.0.0.1:7890":                     "http://127.0.0.1:7890",
		"http=127.0.0.1:1;https=127.0.0.1:2": "http://127.0.0.1:2",
		"socks=127.0.0.1:1080":               "socks5://127.0.0.1:1080",
		"":                                   "",
	} {
		if got := parseWindows(server, "").URL; got != want {
			t.Errorf("%q: %q, want %q", server, got, want)
		}
	}
	p := parseWindows("127.0.0.1:7890", "*.corp;<local>")
	if !bypassed("git.corp", p.Bypass) || !bypassed("intranet", p.Bypass) || bypassed("chatgpt.com", p.Bypass) {
		t.Fatalf("bypass: %+v", p.Bypass)
	}
}

func TestFor(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	sysCache.at, sysCache.p = farFuture, Proxy{URL: "http://127.0.0.1:7890", Bypass: []string{"*.corp"}}
	t.Cleanup(func() { sysCache.at = zero })

	u, _ := url.Parse("https://chatgpt.com/backend-api/codex/responses")
	if p, _ := For(u); p == nil || p.String() != "http://127.0.0.1:7890" {
		t.Fatalf("system: %v", p)
	}
	corp, _ := url.Parse("https://git.corp/x")
	if p, _ := For(corp); p != nil {
		t.Fatalf("bypass: %v", p)
	}
	local := &http.Request{URL: &url.URL{Scheme: "http", Host: "127.0.0.1:3425"}}
	if p, _ := Func(local); p != nil {
		t.Fatalf("loopback: %v", p)
	}

	// magpie's own setting wins; "direct" turns it off
	settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"})
	if p, _ := For(u); p == nil || p.String() != "socks5://127.0.0.1:1080" {
		t.Fatalf("setting: %v", p)
	}
	settings.Save(settings.Settings{Proxy: "direct"})
	if p, _ := For(u); p != nil {
		t.Fatalf("direct: %v", p)
	}
	if err := settings.Save(settings.Settings{Proxy: "ftp://x"}); err == nil {
		t.Fatal("ftp proxy accepted")
	}
	if settings.Save(settings.Settings{Proxy: "127.0.0.1:7890"}) != nil || filepath.Dir(settings.Path()) != filepath.Join(dir, "magpie") {
		t.Fatal("host:port proxy refused")
	}
}

var farFuture = time.Now().Add(time.Hour)
var zero time.Time
