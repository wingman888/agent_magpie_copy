// Package netproxy decides which proxy magpie uses for its own outbound
// requests to model vendors. The order, highest first, is: the proxy in
// settings ("direct" turns proxying off), the proxy environment variables,
// the operating system's proxy settings, and finally a direct connection.
// Loopback hosts are never proxied.
package netproxy

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// Install makes http.DefaultTransport follow For. Call it once at startup.
func Install() {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return
	}
	c := t.Clone()
	c.Proxy = func(r *http.Request) (*url.URL, error) { return For(r.URL) }
	http.DefaultTransport = c
}

// For returns the proxy to use when reaching u, or nil for a direct
// connection. An error means the settings or environment hold a proxy value
// that cannot be used; the request must fail rather than go direct.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, errors.New("netproxy: nil URL")
	}
	if isLoopback(u.Hostname()) {
		return nil, nil
	}
	p, _, err := resolve(u)
	return p, err
}

// Describe reports the proxy that outbound vendor requests use right now and
// where it comes from: "settings", "off", "environment", "system" or "none".
// proxy is empty for "off" and "none". The credentials in proxy, if any, are
// hidden. When the settings or environment hold an unusable value, proxy is
// empty and source is the layer that holds it; the raw value is never
// returned.
func Describe() (proxy, source string) {
	sample, _ := url.Parse("https://example.com/")
	p, src, err := resolve(sample)
	if err != nil {
		return "", src
	}
	if p == nil {
		return "", src
	}
	return p.Redacted(), src
}

// resolve applies the priority order to u. It returns the proxy (nil for
// direct) and the source label.
func resolve(u *url.URL) (*url.URL, string, error) {
	s := strings.TrimSpace(settings.Load().Proxy)
	if s == "direct" {
		return nil, "off", nil
	}
	if s != "" {
		p, err := parseSettingsProxy(s)
		if err != nil {
			return nil, "settings", err
		}
		return p, "settings", nil
	}
	// The environment wins for u when it gives a proxy for u's scheme, or
	// when NO_PROXY excludes u (direct, and the system setting is not read).
	// With no variable for the scheme, the system setting applies.
	if p, decided, err := envProxy(u); err != nil {
		return nil, "environment", err
	} else if decided {
		if p == nil {
			return nil, "none", nil
		}
		return p, "environment", nil
	}
	if sp := systemProxy(); sp.proxy != nil && !sp.bypasses(u.Hostname()) {
		cp := *sp.proxy
		return &cp, "system", nil
	}
	return nil, "none", nil
}

// parseSettingsProxy turns a validated settings value into a proxy URL. A
// bare host:port is an HTTP proxy.
func parseSettingsProxy(v string) (*url.URL, error) {
	v = strings.TrimSpace(v)
	// The error does not repeat v: it may carry credentials, and it may end up in logs.
	if err := settings.CheckProxy(v); err != nil {
		return nil, errors.New("netproxy: the proxy in settings is not a valid proxy address; use direct, or a form such as http://127.0.0.1:7890")
	}
	if v == "" || v == "direct" {
		return nil, errors.New("netproxy: not a proxy address")
	}
	if !strings.Contains(v, "://") {
		v = "http://" + v
	}
	return url.Parse(v)
}

// isLoopback reports whether host is localhost, a name under .localhost, or a
// loopback IP address.
func isLoopback(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
