// Package netproxy decides which proxy, if any, one of magpie's own HTTP
// requests (gateway upstream, model lists, provider tests, usage) goes
// through. The order is:
//
//  1. loopback hosts (localhost, 127.0.0.0/8, ::1 …) never use a proxy;
//  2. the proxy setting: "direct" means none, an address means that address;
//  3. the environment: HTTPS_PROXY / HTTP_PROXY / ALL_PROXY, with NO_PROXY
//     as the exception list;
//  4. the operating system's proxy (macOS and Windows; Linux has none),
//     with its own exception list.
package netproxy

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

func init() {
	// Every request that goes through http.DefaultClient or
	// http.DefaultTransport, including code that does not know about this
	// package, gets the same proxy rules.
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = Proxy
	}
}

// For returns the proxy a request to u should use, or nil for a direct
// connection.
func For(u *url.URL) (*url.URL, error) {
	if u == nil || loopback(u.Hostname()) {
		return nil, nil
	}
	s := settings.Load()
	switch {
	case s.Proxy == "direct":
		return nil, nil
	case s.Proxy != "":
		return settings.ProxyURL(s.Proxy)
	}
	// NO_PROXY is an explicit "connect directly" list: it applies to the
	// environment proxy and to the system proxy alike.
	if matchesList(firstEnv("NO_PROXY", "no_proxy"), u) {
		return nil, nil
	}
	if p := fromEnv(u); p != nil {
		return p, nil
	}
	return fromSystem(u), nil
}

// Proxy is the http.Transport.Proxy hook: it applies For to the request.
func Proxy(r *http.Request) (*url.URL, error) { return For(r.URL) }

// Transport is a copy of the default transport that uses Proxy. Use it
// for a client that needs its own connection pool or settings.
func Transport() *http.Transport {
	var t *http.Transport
	if d, ok := http.DefaultTransport.(*http.Transport); ok {
		t = d.Clone()
	} else {
		t = &http.Transport{}
	}
	t.Proxy = Proxy
	return t
}

// fromEnv is the proxy named by the environment, or nil.
func fromEnv(u *url.URL) *url.URL {
	var raw string
	if u.Scheme == "https" {
		raw = firstEnv("HTTPS_PROXY", "https_proxy")
	} else {
		raw = firstEnv("HTTP_PROXY", "http_proxy")
	}
	if raw == "" {
		raw = firstEnv("ALL_PROXY", "all_proxy")
	}
	if raw == "" {
		return nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	p, err := url.Parse(raw)
	if err != nil || p.Host == "" {
		return nil
	}
	return p
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// fromSystem is the operating system's proxy for u, or nil.
func fromSystem(u *url.URL) *url.URL {
	sys := readSystem()
	host := u.Hostname()
	if sys.SimpleLocal && host != "" && !strings.ContainsAny(host, ".:") {
		return nil
	}
	if matchesAny(sys.Exceptions, u) {
		return nil
	}
	switch {
	case sys.HTTPS != "":
		return proxyURL(sys.HTTPS, "http")
	case sys.HTTP != "":
		return proxyURL(sys.HTTP, "http")
	case sys.SOCKS != "":
		return proxyURL(sys.SOCKS, "socks5")
	}
	return nil
}

// proxyURL turns "host:port" (or an address that already has a scheme)
// into a URL; nil if it does not parse.
func proxyURL(addr, scheme string) *url.URL {
	if !strings.Contains(addr, "://") {
		addr = scheme + "://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" {
		return nil
	}
	return u
}

// loopback reports whether host is this machine.
func loopback(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// matchesList reports whether u matches a comma-separated exception list
// (NO_PROXY syntax).
func matchesList(list string, u *url.URL) bool {
	for _, item := range strings.Split(list, ",") {
		if matchPattern(item, u) {
			return true
		}
	}
	return false
}

func matchesAny(patterns []string, u *url.URL) bool {
	for _, p := range patterns {
		if matchPattern(p, u) {
			return true
		}
	}
	return false
}

// matchPattern reports whether u is excluded by one exception: "*", an
// exact host, a host:port, ".domain" or "*.domain" (which also match the
// bare domain), a glob such as "10.*.1.*", or a CIDR block.
func matchPattern(pat string, u *url.URL) bool {
	pat = strings.ToLower(strings.TrimSpace(pat))
	host := strings.ToLower(u.Hostname())
	if pat == "" || host == "" {
		return false
	}
	if pat == "*" {
		return true
	}
	if h, p, err := net.SplitHostPort(pat); err == nil {
		if p != portOf(u) {
			return false
		}
		pat = h
	}
	pat = strings.Trim(pat, "[]")
	if _, block, err := net.ParseCIDR(pat); err == nil {
		ip := net.ParseIP(host)
		return ip != nil && block.Contains(ip)
	}
	if strings.HasPrefix(pat, "*.") || strings.HasPrefix(pat, ".") {
		base := strings.TrimPrefix(strings.TrimPrefix(pat, "*"), ".")
		return host == base || strings.HasSuffix(host, "."+base)
	}
	if strings.ContainsAny(pat, "*?[") {
		ok, _ := path.Match(pat, host)
		return ok
	}
	return host == pat
}

func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}
