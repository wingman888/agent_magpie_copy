// Package netproxy picks the proxy magpie's own outside requests go through.
//
// The order is: the proxy in settings (proxy = "direct" means none), then
// the environment (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY and NO_PROXY, in either
// case), then the system proxy (macOS network settings, Windows Internet
// options; Linux has none beyond the environment). Loopback addresses never
// use a proxy.
package netproxy

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// For returns the proxy a request to u should use, or nil to connect directly.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, nil
	}
	host := strings.ToLower(u.Hostname())
	if isLoopback(host) {
		return nil, nil
	}
	s := settings.Load()
	mode, err := settings.NormalProxy(s.Proxy)
	if err != nil {
		return nil, err
	}
	switch mode {
	case "direct":
		return nil, nil
	case "":
	default:
		return parseProxy(mode)
	}
	if raw, noProxy, ok := envProxy(os.Getenv); ok {
		if bypass(host, splitList(noProxy, ","), false) {
			return nil, nil
		}
		return parseProxy(raw)
	}
	sys := sysProxy()
	if bypass(host, sys.Exceptions, sys.SimpleHostnames) {
		return nil, nil
	}
	return sys.pick(), nil
}

// ProxyFunc is for http.Transport.Proxy.
func ProxyFunc(r *http.Request) (*url.URL, error) {
	return For(r.URL)
}

// Transport is a transport like http.DefaultTransport that takes its proxy
// from For.
func Transport() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t = &http.Transport{}
	}
	t = t.Clone()
	t.Proxy = ProxyFunc
	return t
}

// Client is the client for magpie's requests to providers and vendors.
var Client = &http.Client{Transport: Transport()}

// envProxy returns the first proxy variable set, in HTTPS, HTTP, ALL order
// (the same preference as the system proxy), with the NO_PROXY list.
func envProxy(getenv func(string) string) (raw, noProxy string, ok bool) {
	get := func(names ...string) string {
		for _, n := range names {
			if v := strings.TrimSpace(getenv(n)); v != "" {
				return v
			}
		}
		return ""
	}
	raw = get("HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy")
	noProxy = get("NO_PROXY", "no_proxy")
	return raw, noProxy, raw != ""
}

// parseProxy reads a proxy URL; a bare host:port is http.
func parseProxy(raw string) (*url.URL, error) {
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("proxy %q: %v", raw, err)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("proxy %q has no host", raw)
	}
	return u, nil
}

// systemProxy is what the operating system says a proxy is. URLs are kept as
// strings until pick chooses one.
type systemProxy struct {
	HTTPS, HTTP, SOCKS string
	Exceptions         []string
	SimpleHostnames    bool // "local" names (no dots) bypass the proxy
}

// pick returns HTTPS, then HTTP, then SOCKS, or nil when none is set.
func (s systemProxy) pick() *url.URL {
	for _, raw := range []string{s.HTTPS, s.HTTP, s.SOCKS} {
		if raw == "" {
			continue
		}
		if u, err := parseProxy(raw); err == nil {
			return u
		}
	}
	return nil
}

// isLoopback is true for localhost and loopback IPs.
func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// bypass reports whether host matches an exception: "*" (everything), an
// exact host or IP, a domain suffix ("example.com", ".example.com",
// "*.example.com"), a CIDR or scutil-style prefix ("10.0.0.0/8", "169.254/16"),
// "host:port" (port ignored), or, when simpleHostnames is set, any name
// without a dot.
func bypass(host string, rules []string, simpleHostnames bool) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "" {
		return false
	}
	if simpleHostnames && !strings.Contains(host, ".") && net.ParseIP(host) == nil {
		return true
	}
	ip := net.ParseIP(host)
	for _, r := range rules {
		r = strings.ToLower(strings.TrimSpace(r))
		if r == "" {
			continue
		}
		if r == "*" {
			return true
		}
		if r == "<local>" {
			if !strings.Contains(host, ".") && ip == nil {
				return true
			}
			continue
		}
		if ip != nil {
			if n := parseNet(r); n != nil && n.Contains(ip) {
				return true
			}
		}
		if h, _, err := net.SplitHostPort(r); err == nil {
			r = h
		}
		if ip != nil && net.ParseIP(r) != nil && net.ParseIP(r).Equal(ip) {
			return true
		}
		r = strings.TrimPrefix(r, "*")
		r = strings.TrimPrefix(r, ".")
		if r == "" {
			continue
		}
		if host == r || strings.HasSuffix(host, "."+r) {
			return true
		}
	}
	return false
}

// parseNet reads a CIDR, or a scutil prefix such as "169.254/16" or
// "10.1/16", which lacks trailing zero octets. It returns nil for anything
// else.
func parseNet(r string) *net.IPNet {
	if _, n, err := net.ParseCIDR(r); err == nil {
		return n
	}
	addr, bits, ok := strings.Cut(r, "/")
	if !ok || strings.Count(addr, ".") > 3 {
		return nil
	}
	parts := strings.Split(addr, ".")
	for len(parts) < 4 {
		parts = append(parts, "0")
	}
	if _, n, err := net.ParseCIDR(strings.Join(parts, ".") + "/" + bits); err == nil {
		return n
	}
	return nil
}

// splitList splits a comma or semicolon separated list and drops blanks.
func splitList(s, sep string) []string {
	var out []string
	for _, f := range strings.Split(s, sep) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
