// Package netproxy decides which proxy, if any, a request from magpie goes
// through. The order is: the proxy in settings, then the environment
// ($HTTPS_PROXY / $HTTP_PROXY with $NO_PROXY), then the system proxy
// (macOS and Windows; Linux has only the environment). Loopback addresses
// never use a proxy.
package netproxy

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// settingsProxy returns the Proxy value of the settings file. It is a
// variable so tests can replace it.
var settingsProxy = func() string { return settings.Load().Proxy }

// For returns the proxy a request to u should use, or nil for a direct
// connection.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, fmt.Errorf("netproxy: nil URL")
	}
	host := u.Hostname()
	if isLoopback(host) {
		return nil, nil
	}

	// 1. settings
	if p := settingsProxy(); p != "" {
		return settings.ParseProxy(p)
	}

	// 2. environment
	if raw := envProxy(u.Scheme); raw != "" {
		if noProxy(host, os.Getenv("NO_PROXY"), os.Getenv("no_proxy")) {
			return nil, nil
		}
		return settings.ParseProxy(raw)
	}

	// 3. system
	cfg, err := systemConfig()
	if err != nil || cfg == nil {
		return nil, nil // an unreadable system setting means direct, not a failed request
	}
	return cfg.proxyFor(u.Scheme, host), nil
}

// envProxy is the proxy variable for the scheme, "" when none is set.
func envProxy(scheme string) string {
	names := []string{"HTTP_PROXY", "http_proxy"}
	if scheme == "https" {
		names = []string{"HTTPS_PROXY", "https_proxy"}
	}
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// isLoopback is true for 127.0.0.0/8, ::1 and localhost names.
func isLoopback(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

// noProxy reports whether host is matched by a NO_PROXY list (either of the
// two spellings, comma separated). "*" matches every host.
func noProxy(host string, lists ...string) bool {
	for _, list := range lists {
		for _, e := range strings.Split(list, ",") {
			if matchHost(strings.TrimSpace(e), host, false) {
				return true
			}
		}
	}
	return false
}

// matchHost matches one exception entry against host. Entries may be "*",
// "*.example.com", ".example.com" (a suffix), "example.com" (the host or
// its subdomains, as NO_PROXY is read), a glob, or with simpleHostnames
// "<local>" (Windows) for hosts without a dot. Empty entries never match.
func matchHost(entry, host string, simpleHostnames bool) bool {
	if entry == "" {
		return false
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	e := strings.ToLower(entry)
	if e == "*" {
		return true
	}
	if e == "<local>" {
		return !strings.Contains(h, ".")
	}
	if simpleHostnames && !strings.Contains(h, ".") {
		return true
	}
	if strings.Contains(e, "/") {
		if _, network, err := net.ParseCIDR(cidrOf(e)); err == nil {
			if ip := net.ParseIP(h); ip != nil {
				return network.Contains(ip)
			}
		}
		return false
	}
	if strings.HasPrefix(e, "*.") {
		return strings.HasSuffix(h, e[1:])
	}
	if strings.HasPrefix(e, ".") {
		return strings.HasSuffix(h, e)
	}
	if strings.Contains(e, "*") {
		ok, _ := path.Match(e, h)
		return ok
	}
	return h == e || strings.HasSuffix(h, "."+e)
}

// cidrOf completes the short forms the macOS list uses ("169.254/16").
func cidrOf(e string) string {
	ip, bits, _ := strings.Cut(e, "/")
	parts := strings.Split(ip, ".")
	for len(parts) < 4 {
		parts = append(parts, "0")
	}
	return strings.Join(parts, ".") + "/" + bits
}

// sysConfig is the system proxy: one URL per scheme and the exceptions.
type sysConfig struct {
	HTTP, HTTPS, SOCKS *url.URL
	Exceptions         []string
	SimpleHostnames    bool // exclude plain host names (no dot)
}

// proxyFor applies the system proxy to a request, or returns nil.
func (c *sysConfig) proxyFor(scheme, host string) *url.URL {
	for _, e := range c.Exceptions {
		if matchHost(e, host, c.SimpleHostnames) {
			return nil
		}
	}
	if c.SimpleHostnames && !strings.Contains(host, ".") {
		return nil
	}
	// HTTPS first, then HTTP, then SOCKS, whatever the request's scheme.
	switch {
	case c.HTTPS != nil:
		return c.HTTPS
	case c.HTTP != nil:
		return c.HTTP
	case c.SOCKS != nil:
		return c.SOCKS
	}
	return nil
}
