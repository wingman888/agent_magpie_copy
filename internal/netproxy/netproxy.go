// Package netproxy decides which proxy magpie's own outbound requests (to the
// model vendors) go through. The order, highest first, is:
//
//  1. the "proxy" setting: "direct" means no proxy, anything else is the proxy;
//  2. the environment: HTTPS_PROXY / HTTP_PROXY / ALL_PROXY and NO_PROXY,
//     with their lowercase forms;
//  3. the operating system's proxy settings (macOS and Windows only);
//  4. no proxy.
//
// Loopback hosts are never proxied, whatever the level.
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

// systemCacheTTL bounds how stale the operating system's proxy reading may be.
const systemCacheTTL = 10 * time.Second

// systemProxyFunc reads the operating system's proxy settings. It is a variable
// so tests can replace it. Platform files define readSystemProxy.
var systemProxyFunc = readSystemProxy

// systemConfig is one reading of the operating system's proxy settings.
// proxy is "" when no usable system proxy is enabled.
type systemConfig struct {
	proxy  string
	bypass []string
}

var sysCache struct {
	mu     sync.Mutex
	valid  bool
	at     time.Time
	config systemConfig
}

// systemConfigCached returns the system proxy reading, refreshing it at most
// every systemCacheTTL.
func systemConfigCached() systemConfig {
	sysCache.mu.Lock()
	defer sysCache.mu.Unlock()
	if sysCache.valid && time.Since(sysCache.at) < systemCacheTTL {
		return sysCache.config
	}
	sysCache.config = systemProxyFunc()
	sysCache.at = time.Now()
	sysCache.valid = true
	return sysCache.config
}

// resetSystemCache forces the next lookup to read the system settings again.
func resetSystemCache() {
	sysCache.mu.Lock()
	defer sysCache.mu.Unlock()
	sysCache.valid = false
}

// For returns the proxy to use when reaching u, or nil for a direct connection.
// It reads the settings on every call, so a change takes effect immediately.
// An unusable configured proxy is returned as an error, never as a silent
// direct connection.
func For(u *url.URL) (*url.URL, error) {
	p, _, err := resolve(u)
	return p, err
}

// Describe reports the proxy that requests to ordinary external hosts use now,
// and where it comes from: "settings", "off" (set to direct), "environment",
// "system" or "none". proxy is "" for "off" and "none". The proxy's password,
// if any, is redacted. If the proxy at the level that applies is unusable,
// Describe returns ("", src) where src names that level: "settings",
// "environment" or "system".
func Describe() (proxy, source string) {
	probe, _ := url.Parse("https://example.invalid/")
	p, src, err := resolve(probe)
	if err != nil {
		return "", src
	}
	if p == nil {
		return "", src
	}
	return p.Redacted(), src
}

// Install makes http.DefaultTransport follow the same rules as For.
// Call it once at program start.
func Install() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = func(r *http.Request) (*url.URL, error) { return For(r.URL) }
	}
}

// resolve applies the priority order to u and reports the source it used.
func resolve(u *url.URL) (*url.URL, string, error) {
	if u == nil {
		return nil, "none", errors.New("netproxy: nil URL")
	}
	host := strings.ToLower(u.Hostname())
	if isLoopback(host) {
		return nil, "none", nil
	}

	// 1. The setting.
	s := strings.TrimSpace(settings.Load().Proxy)
	if s == "direct" {
		return nil, "off", nil
	}
	if s != "" {
		if err := settings.ValidateProxy(s); err != nil {
			return nil, "settings", err
		}
		p, err := parseProxy(s)
		if err != nil {
			return nil, "settings", err
		}
		return p, "settings", nil
	}

	// 2. The environment.
	if name, raw := envProxy(u.Scheme); raw != "" {
		if noProxyMatches(getEnvAny("NO_PROXY", "no_proxy"), host, portOf(u)) {
			return nil, "environment", nil
		}
		p, err := parseProxy(raw)
		if err != nil {
			return nil, "environment", fmt.Errorf("%s: %w", name, err)
		}
		return p, "environment", nil
	}

	// 3. The operating system.
	sys := systemConfigCached()
	if sys.proxy != "" {
		if matchBypass(host, sys.bypass) {
			return nil, "system", nil
		}
		p, err := parseProxy(sys.proxy)
		if err != nil {
			return nil, "system", err
		}
		return p, "system", nil
	}

	// 4. Nothing.
	return nil, "none", nil
}

// envProxy returns the name and value of the first proxy variable that applies
// to scheme. Empty when none is set.
func envProxy(scheme string) (string, string) {
	var names []string
	if scheme == "http" {
		names = []string{"HTTP_PROXY", "http_proxy"}
	} else {
		names = []string{"HTTPS_PROXY", "https_proxy"}
	}
	names = append(names, "ALL_PROXY", "all_proxy")
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return n, v
		}
	}
	return "", ""
}

func getEnvAny(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// parseProxy turns a proxy address into a URL. A bare host:port is HTTP.
// Only http, https, socks5 and socks5h schemes are accepted.
func parseProxy(s string) (*url.URL, error) {
	raw := s
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("netproxy: proxy %q cannot be parsed: %w", s, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("netproxy: proxy %q has unsupported scheme %q", s, u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("netproxy: proxy %q has no host", s)
	}
	return u, nil
}

// portOf returns the port of u, or the scheme's default port.
func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "http" {
		return "80"
	}
	return "443"
}

// isLoopback reports whether host (lowercase, without brackets) is localhost,
// *.localhost, or a loopback IP address.
func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// noProxyMatches reports whether host:port is listed in a NO_PROXY value.
// It follows the conventions of Go's net/http: "*" matches everything;
// entries are comma-separated; an entry may be an IP, a CIDR block, a domain
// (matching the domain and its subdomains; a leading "." or "*." is allowed),
// optionally with a port.
func noProxyMatches(list, host, port string) bool {
	hostIP := net.ParseIP(host)
	for _, e := range strings.Split(list, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == "*" {
			return true
		}
		if _, ipnet, err := net.ParseCIDR(e); err == nil {
			if hostIP != nil && ipnet.Contains(hostIP) {
				return true
			}
			continue
		}
		if h, p, err := net.SplitHostPort(e); err == nil {
			if p != port {
				continue
			}
			e = h
		}
		e = strings.TrimPrefix(e, "*")
		if ip := net.ParseIP(e); ip != nil {
			if hostIP != nil && ip.Equal(hostIP) {
				return true
			}
			continue
		}
		e = strings.TrimPrefix(e, ".")
		if e != "" && (host == e || strings.HasSuffix(host, "."+e)) {
			return true
		}
	}
	return false
}
