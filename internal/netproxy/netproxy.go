// Package netproxy decides which proxy magpie uses for its own outgoing
// requests to model vendors. The order, from highest to lowest, is:
//
//  1. the "proxy" setting: "direct" means no proxy, anything else is the proxy;
//  2. the proxy environment variables (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY,
//     NO_PROXY and their lowercase forms), read with the standard semantics;
//  3. the operating system's proxy settings (macOS and Windows only);
//  4. no proxy.
//
// Loopback hosts are always direct. Install makes the default HTTP transport
// follow the same rules, so code that never passes through the gateway does
// not bypass them.
package netproxy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// Install makes http.DefaultTransport use For. It is safe to call more than once.
func Install() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = proxyFunc
	}
}

func proxyFunc(r *http.Request) (*url.URL, error) {
	return For(r.URL)
}

// For returns the proxy to use when reaching u, or nil to connect directly.
// It returns an error when a proxy is configured but cannot be used, so the
// request fails instead of quietly going direct.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, errors.New("netproxy: nil URL")
	}
	p, _, err := decide(u, settings.Load().Proxy, os.Getenv, systemCached)
	return p, err
}

// probe is the request Describe asks about: an HTTPS request to a vendor
// host that no exception list is likely to name.
var probe = &url.URL{Scheme: "https", Host: "vendor.invalid"}

// Describe reports the proxy that outgoing requests to vendors use now, and
// where it comes from: settings, off (the setting is "direct"), environment,
// system or none. The proxy is empty for off and none.
func Describe() (proxy, source string) {
	set := settings.Load().Proxy
	p, src, err := decide(probe, set, os.Getenv, systemCached)
	if err != nil {
		// The setting cannot be used; For returns this error for every request.
		return strings.TrimSpace(set), "settings"
	}
	if src == "off" {
		return "", "off"
	}
	if p == nil {
		return "", "none"
	}
	return p.Redacted(), src
}

// decide applies the priority order to one request. set is the settings
// value, get reads environment variables and sys reads the system settings.
// For and Describe both call it, so they always agree.
func decide(u *url.URL, set string, get func(string) string, sys func() sysConf) (*url.URL, string, error) {
	host := u.Hostname()
	if isLoopback(host) {
		return nil, "none", nil
	}

	set = strings.TrimSpace(set)
	if strings.EqualFold(set, "direct") {
		return nil, "off", nil
	}
	if set != "" {
		v, err := settings.ValidateProxy(set)
		if err != nil {
			return nil, "", err
		}
		p, err := toProxyURL(v)
		if err != nil {
			return nil, "", err
		}
		return p, "settings", nil
	}

	if raw := envRaw(get, u.Scheme); raw != "" {
		// Once the environment names a proxy, NO_PROXY decides, as it always has.
		if noProxyMatch(host, envGet(get, "NO_PROXY", "no_proxy")) {
			return nil, "environment", nil
		}
		p, err := toProxyURL(raw)
		if err != nil {
			return nil, "", fmt.Errorf("netproxy: proxy from environment %q is not usable: %w", raw, err)
		}
		return p, "environment", nil
	}

	sc := sys()
	if sc.proxy == nil || matchException(host, sc.exceptions) {
		return nil, "none", nil
	}
	return sc.proxy, "system", nil
}

// envRaw returns the proxy variable that applies to scheme, or "".
func envRaw(get func(string) string, scheme string) string {
	switch scheme {
	case "https":
		return envGet(get, "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy")
	case "http":
		return envGet(get, "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy")
	}
	return ""
}

// envGet returns the first non-empty variable among names.
func envGet(get func(string) string, names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(get(n)); v != "" {
			return v
		}
	}
	return ""
}

// noProxyMatch applies a NO_PROXY list with the standard library's rules: "*"
// matches everything, an IP or CIDR matches addresses, and a domain matches
// itself and its subdomains (with or without a leading dot).
func noProxyMatch(host, list string) bool {
	host = strings.ToLower(host)
	ip := net.ParseIP(host)
	for _, entry := range strings.Split(list, ",") {
		e := strings.ToLower(strings.TrimSpace(entry))
		switch {
		case e == "":
		case e == "*":
			return true
		case strings.Contains(e, "/"):
			if _, n, err := net.ParseCIDR(e); err == nil && ip != nil && n.Contains(ip) {
				return true
			}
		case net.ParseIP(e) != nil:
			if ip != nil && ip.Equal(net.ParseIP(e)) {
				return true
			}
		default:
			if domainMatch(host, strings.TrimPrefix(e, ".")) {
				return true
			}
		}
	}
	return false
}

// isLoopback reports whether host is localhost, a name under .localhost, or a
// loopback IP address. The host must not carry a port or brackets.
func isLoopback(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// toProxyURL parses a proxy address. A value without a scheme is an HTTP proxy.
func toProxyURL(s string) (*url.URL, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("proxy %q has no host", s)
	}
	return u, nil
}

const systemTTL = 5 * time.Second

var (
	sysMu    sync.Mutex
	sysCache sysConf
	sysAt    time.Time
	sysSet   bool
)

// systemCached returns the system proxy setting, reading it again after
// systemTTL so that a change in the OS settings takes effect within seconds.
func systemCached() sysConf {
	sysMu.Lock()
	defer sysMu.Unlock()
	if sysSet && time.Since(sysAt) < systemTTL {
		return sysCache
	}
	sysCache = readSystem()
	sysAt = time.Now()
	sysSet = true
	return sysCache
}
