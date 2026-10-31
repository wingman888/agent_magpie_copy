package netproxy_test

import (
	"net/url"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
)

func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
}

func forURL(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := netproxy.For(u)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

func TestAcceptProxyOrder(t *testing.T) {
	clearEnv(t)
	const target = "https://api.deepseek.com/v1/chat/completions"

	// environment, when settings say nothing
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9001")
	if got := forURL(t, target); got != "http://127.0.0.1:9001" {
		t.Errorf("env: %q", got)
	}
	// NO_PROXY exempts a host
	t.Setenv("NO_PROXY", "deepseek.com")
	if got := forURL(t, target); got != "" {
		t.Errorf("NO_PROXY: %q", got)
	}
	t.Setenv("NO_PROXY", "")

	// settings beat the environment
	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	if got := forURL(t, target); got != "socks5://127.0.0.1:1080" {
		t.Errorf("setting: %q", got)
	}
	if err := settings.Save(settings.Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if got := forURL(t, target); got != "http://127.0.0.1:7890" {
		t.Errorf("bare host:port: %q", got)
	}
	// direct turns the proxy off even with an environment proxy
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if got := forURL(t, target); got != "" {
		t.Errorf("direct: %q", got)
	}
	// back to following the environment
	if err := settings.Save(settings.Settings{}); err != nil {
		t.Fatal(err)
	}
	if got := forURL(t, target); got != "http://127.0.0.1:9001" {
		t.Errorf("empty setting: %q", got)
	}
}

func TestAcceptLoopbackNeverProxied(t *testing.T) {
	clearEnv(t)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9001")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9001")
	for _, raw := range []string{"http://127.0.0.1:3425/v1", "http://localhost:11434/v1", "http://[::1]:8080/x"} {
		if got := forURL(t, raw); got != "" {
			t.Errorf("env %s: %q", raw, got)
		}
	}
}
