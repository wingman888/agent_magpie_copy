// Package netproxy decides which proxy, if any, a request to a provider or
// any other outside host should use.
//
// The order is: the magpie setting (settings.json "proxy"), then the
// environment (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY and NO_PROXY, in either
// case), then the operating system's network settings. Loopback addresses
// never use a proxy.
package netproxy

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// For returns the proxy a request to u should go through; nil means connect
// directly. An error means the configured proxy is unusable.
func For(u *url.URL) (*url.URL, error) {
	if u == nil || u.Hostname() == "" {
		return nil, nil
	}
	host := strings.ToLower(u.Hostname())
	if isLoopback(host) {
		return nil, nil
	}

	s := settings.Load()
	switch s.Proxy {
	case settings.ProxyDirect:
		return nil, nil
	case "":
		// follow the environment and the system below
	default:
		return settings.ParseProxy(s.Proxy)
	}

	if raw := envProxy(u.Scheme); raw != "" {
		if excluded(host, getenv("NO_PROXY")) {
			return nil, nil
		}
		return parseEnvProxy(raw)
	}
	return systemFor(host), nil
}

// ForRequest is For for the Proxy field of an http.Transport.
func ForRequest(r *http.Request) (*url.URL, error) { return For(r.URL) }

// Transport is the default transport with the proxy rule above.
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = ForRequest
	return t
}

// shared is the client for magpie's outside requests that need no timeout.
var shared = &http.Client{Transport: Transport()}

// Client returns the shared client for outside requests. It follows For.
func Client() *http.Client { return shared }

// getenv reads NAME, then name, ignoring empty values.
func getenv(name string) string {
	if v := os.Getenv(strings.ToUpper(name)); v != "" {
		return v
	}
	return os.Getenv(strings.ToLower(name))
}

// envProxy returns the proxy variable for a scheme, "" when none is set.
// HTTPS_PROXY serves https and HTTP_PROXY serves http; ALL_PROXY is the fallback.
func envProxy(scheme string) string {
	switch scheme {
	case "https":
		if v := getenv("HTTPS_PROXY"); v != "" {
			return v
		}
	case "http":
		if v := getenv("HTTP_PROXY"); v != "" {
			return v
		}
	}
	return getenv("ALL_PROXY")
}

// parseEnvProxy reads a proxy variable; a bare host:port is an HTTP proxy.
func parseEnvProxy(raw string) (*url.URL, error) {
	return settings.ParseProxy(raw)
}

// excluded reports whether host matches a NO_PROXY list.
func excluded(host, list string) bool {
	for _, rule := range strings.Split(list, ",") {
		rule = stripPort(strings.TrimSpace(rule))
		if matches(host, rule) {
			return true
		}
	}
	return false
}

// stripPort drops a trailing ":port" from a NO_PROXY entry such as example.com:8080.
func stripPort(rule string) string {
	if strings.Count(rule, ":") != 1 {
		return rule
	}
	h, _, ok := strings.Cut(rule, ":")
	if !ok {
		return rule
	}
	return h
}

// isLoopback is true for localhost and 127.0.0.0/8 and ::1.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// matches reports whether host is covered by one exception rule. The rules
// are the forms NO_PROXY and the OS lists use: "*", ".example.com",
// "*.example.com", "example.com", "10.0.0.0/8", "169.254/16", "192.168.*".
func matches(host, rule string) bool {
	rule = strings.ToLower(strings.TrimSpace(rule))
	if rule == "" {
		return false
	}
	if rule == "*" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		if n, ok := parseNet(rule); ok {
			return n.Contains(ip)
		}
	}
	if strings.Contains(rule, "*") {
		ok, _ := path.Match(rule, host)
		return ok
	}
	if strings.HasPrefix(rule, ".") {
		return host == rule[1:] || strings.HasSuffix(host, rule)
	}
	return host == rule
}

// parseNet reads a CIDR rule, including the short form 169.254/16 that
// scutil prints, which means 169.254.0.0/16.
func parseNet(rule string) (*net.IPNet, bool) {
	if _, n, err := net.ParseCIDR(rule); err == nil {
		return n, true
	}
	addr, bits, ok := strings.Cut(rule, "/")
	if !ok {
		return nil, false
	}
	parts := strings.Split(addr, ".")
	if len(parts) > 4 {
		return nil, false
	}
	for len(parts) < 4 {
		parts = append(parts, "0")
	}
	if _, err := strconv.Atoi(bits); err != nil {
		return nil, false
	}
	_, n, err := net.ParseCIDR(strings.Join(parts, ".") + "/" + bits)
	return n, err == nil
}
