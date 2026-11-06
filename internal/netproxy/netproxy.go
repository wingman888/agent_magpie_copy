// Package netproxy decides which proxy magpie uses for its own outbound
// requests to model vendors. The order, from the top:
//
//  1. loopback hosts never use a proxy;
//  2. the proxy in the settings file ("direct" turns proxies off);
//  3. the environment (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY and NO_PROXY);
//  4. the operating system's proxy settings (macOS and Windows only);
//  5. no proxy.
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

// sysProxy is what the operating system says: a proxy URL ("" for none) and
// the exception list that goes with it.
type sysProxy struct {
	URL    string
	Except []string
}

// readSystemProxy reads the OS proxy settings. It is a variable so tests can
// replace it. Each platform file defines readSystem.
var readSystemProxy = readSystem

// sysTTL is how long a reading of the OS settings is kept. Changes made by the
// user show up within this time.
const sysTTL = 10 * time.Second

var (
	sysMu  sync.Mutex
	sysVal sysProxy
	sysAt  time.Time
)

// systemProxy returns the OS proxy reading, cached for sysTTL.
func systemProxy() sysProxy {
	sysMu.Lock()
	defer sysMu.Unlock()
	if !sysAt.IsZero() && time.Since(sysAt) < sysTTL {
		return sysVal
	}
	sysVal = readSystemProxy()
	sysAt = time.Now()
	return sysVal
}

// Install makes http.DefaultTransport follow For. Call it once at start-up.
// If DefaultTransport is not an *http.Transport, it does nothing.
func Install() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = func(r *http.Request) (*url.URL, error) {
			return For(r.URL)
		}
	}
}

// For returns the proxy to use when reaching u, or nil to connect directly.
// An error means the settings or the environment hold a value that cannot be
// used; the request must fail rather than quietly go direct.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, errors.New("netproxy: nil URL")
	}
	host := u.Hostname()
	if isLoopback(host) {
		return nil, nil
	}
	if raw := strings.TrimSpace(settings.Load().Proxy); raw != "" {
		if raw == "direct" {
			return nil, nil
		}
		p, err := settings.ParseProxyAddr(raw)
		if err != nil {
			return nil, fmt.Errorf("netproxy: settings proxy: %w", err)
		}
		return p, nil
	}
	if envPresent() {
		return envFor(u)
	}
	sys := systemProxy()
	if sys.URL == "" || matchBypass(host, sys.Except) {
		return nil, nil
	}
	p, err := url.Parse(sys.URL)
	if err != nil {
		return nil, nil
	}
	return p, nil
}

// Describe reports the proxy that outbound requests use right now, and where
// it comes from: "settings", "off" (settings say direct), "environment",
// "system" or "none". The proxy is "" for "off" and "none". The target host
// is not considered.
func Describe() (proxy, source string) {
	s := strings.TrimSpace(settings.Load().Proxy)
	if s == "direct" {
		return "", "off"
	}
	if s != "" {
		return s, "settings"
	}
	if envPresent() {
		return getEnvAny("HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"), "environment"
	}
	if p := systemProxy().URL; p != "" {
		return p, "system"
	}
	return "", "none"
}

// isLoopback reports whether host is localhost, *.localhost or a loopback IP.
func isLoopback(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// envPresent reports whether any proxy variable is set.
func envPresent() bool {
	return getEnvAny("HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy") != ""
}

// envFor applies the environment variables the way net/http's
// ProxyFromEnvironment does: HTTPS_PROXY for https, HTTP_PROXY for http,
// ALL_PROXY as the fallback, and NO_PROXY to exclude hosts. Hosts that NO_PROXY
// excludes go direct.
func envFor(u *url.URL) (*url.URL, error) {
	var raw string
	switch u.Scheme {
	case "https":
		raw = getEnvAny("HTTPS_PROXY", "https_proxy")
	case "http":
		raw = getEnvAny("HTTP_PROXY", "http_proxy")
	}
	if raw == "" {
		raw = getEnvAny("ALL_PROXY", "all_proxy")
	}
	if raw == "" {
		return nil, nil
	}
	if noProxyMatch(u, getEnvAny("NO_PROXY", "no_proxy")) {
		return nil, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	p, err := url.Parse(raw)
	if err != nil || p.Host == "" {
		return nil, fmt.Errorf("netproxy: environment proxy %q is not a valid URL", raw)
	}
	return p, nil
}

// noProxyMatch reports whether NO_PROXY list excludes u. The list is comma
// separated. Each entry can be "*", an IP, a CIDR, a domain (with or without a
// leading dot or "*."), or any of these followed by ":port".
func noProxyMatch(u *url.URL, list string) bool {
	host := strings.ToLower(u.Hostname())
	if isLoopback(host) {
		return true
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	ip := net.ParseIP(host)
	for _, e := range strings.Split(list, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == "*" {
			return true
		}
		if _, n, err := net.ParseCIDR(e); err == nil {
			if ip != nil && n.Contains(ip) {
				return true
			}
			continue
		}
		eh, ep := e, ""
		if h, p, err := net.SplitHostPort(e); err == nil {
			eh, ep = h, p
		}
		if ep != "" && ep != port {
			continue
		}
		eh = strings.TrimPrefix(strings.TrimPrefix(eh, "*"), ".")
		if eh == "" {
			continue
		}
		if ip != nil {
			if eh == host {
				return true
			}
			continue
		}
		if host == eh || strings.HasSuffix(host, "."+eh) {
			return true
		}
	}
	return false
}

// getEnvAny returns the first non-empty value among the named variables.
func getEnvAny(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}
