// Package netproxy decides which proxy magpie uses for its own requests to
// the model vendors. The order, highest first, is:
//
//  1. the "proxy" setting: "direct" means no proxy, anything else is the proxy;
//  2. the environment variables HTTPS_PROXY, HTTP_PROXY, ALL_PROXY, NO_PROXY
//     (and their lowercase forms), with the same meaning as Go's standard library;
//  3. the operating system's proxy settings (macOS and Windows only);
//  4. no proxy.
//
// Loopback hosts are always connected to directly. The setting is read on
// every call, so a change takes effect for the next request.
package netproxy

import (
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

// Sources reported by Describe.
const (
	sourceSettings    = "settings"
	sourceOff         = "off"
	sourceEnvironment = "environment"
	sourceSystem      = "system"
	sourceNone        = "none"
	// sourceInvalid is reported when the configured proxy cannot be used,
	// so requests fail instead of quietly going direct.
	sourceInvalid = "invalid"
)

// sysCacheTTL bounds how long an operating-system proxy reading is reused.
const sysCacheTTL = 10 * time.Second

// Replaceable hooks, so tests can inject settings, environment and the OS
// proxy without touching the real machine.
var (
	loadSettings    = settings.Load
	envGet          = os.Getenv
	readSystemProxy = readOSProxy

	sysMu  sync.Mutex
	sysAt  time.Time
	sysVal sysProxy
)

// Install makes the default HTTP client follow the same proxy rules. Call it
// once at program start.
func Install() {
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.Proxy = func(r *http.Request) (*url.URL, error) { return For(r.URL) }
	}
}

// For returns the proxy to use when requesting u, or nil to connect directly.
// An error means a configured proxy is unusable; the request must not go direct.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, nil
	}
	host := normHost(u.Hostname())
	if host == "" || isLoopbackHost(host) {
		return nil, nil
	}
	port := u.Port()
	if port == "" {
		port = defaultPort(u.Scheme)
	}
	c, err := decide(u.Scheme, host, port)
	if err != nil {
		return nil, err
	}
	return c.proxy, nil
}

// Describe reports the proxy that vendor requests use in general, and where
// it comes from. The source is one of "settings", "off", "environment",
// "system", "none", or "invalid" (the configured proxy cannot be used). For
// "off", "none" and "invalid" the proxy is "". Host-specific exceptions
// (NO_PROXY and the system exception list) are not applied here.
func Describe() (proxy, source string) {
	c, err := decide("https", "", "")
	if err != nil {
		return "", sourceInvalid
	}
	if c.proxy == nil {
		return "", c.source
	}
	return c.proxy.String(), c.source
}

// choice is the outcome of the precedence order for one request.
type choice struct {
	proxy  *url.URL // nil means connect directly
	source string
}

// decide applies the precedence order. host is normalized and not loopback;
// it may be "" when describing the general case, which skips host exceptions.
func decide(scheme, host, port string) (choice, error) {
	// 1. the setting
	p := strings.TrimSpace(loadSettings().Proxy)
	if p == "direct" {
		return choice{nil, sourceOff}, nil
	}
	if p != "" {
		if err := settings.ValidateProxy(p); err != nil {
			return choice{}, err
		}
		u, err := parseProxy(p)
		if err != nil {
			return choice{}, err
		}
		return choice{u, sourceSettings}, nil
	}

	// 2. the environment
	if raw := envProxy(scheme); raw != "" {
		if noProxyMatch(firstEnv("NO_PROXY", "no_proxy"), host, port) {
			return choice{nil, sourceEnvironment}, nil
		}
		u, err := parseProxy(raw)
		if err != nil {
			return choice{}, fmt.Errorf("environment proxy: %w", err)
		}
		return choice{u, sourceEnvironment}, nil
	}

	// 3. the operating system
	sp := currentSystemProxy()
	if sp.proxy != "" {
		if matchExceptions(host, sp.exceptions) {
			return choice{nil, sourceSystem}, nil
		}
		u, err := parseProxy(sp.proxy)
		if err != nil {
			return choice{}, err
		}
		return choice{u, sourceSystem}, nil
	}

	// 4. nothing
	return choice{nil, sourceNone}, nil
}

// envProxy returns the proxy variable that applies to scheme, following the
// standard library: HTTP_PROXY for http, HTTPS_PROXY otherwise, then ALL_PROXY.
func envProxy(scheme string) string {
	var raw string
	if scheme == "http" {
		raw = firstEnv("HTTP_PROXY", "http_proxy")
	} else {
		raw = firstEnv("HTTPS_PROXY", "https_proxy")
	}
	if raw == "" {
		raw = firstEnv("ALL_PROXY", "all_proxy")
	}
	return raw
}

// firstEnv returns the first non-empty value among the named variables.
func firstEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(envGet(n)); v != "" {
			return v
		}
	}
	return ""
}

// currentSystemProxy returns the operating-system reading, cached for at most
// sysCacheTTL. A failed read is an empty result, never an error.
func currentSystemProxy() sysProxy {
	sysMu.Lock()
	defer sysMu.Unlock()
	if !sysAt.IsZero() && time.Since(sysAt) < sysCacheTTL {
		return sysVal
	}
	sysVal = readSystemProxy()
	sysAt = time.Now()
	return sysVal
}

// parseProxy turns a proxy address into a URL. A missing scheme means http.
func parseProxy(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("proxy %q is not a valid address: %w", raw, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("proxy %q has unsupported scheme %q", raw, u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("proxy %q has no host", raw)
	}
	return u, nil
}

// normHost lowercases host and drops a trailing dot.
func normHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// isLoopbackHost reports localhost, *.localhost, and loopback IP addresses.
func isLoopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func defaultPort(scheme string) string {
	if scheme == "http" {
		return "80"
	}
	return "443"
}

// noProxyMatch applies a NO_PROXY list with the standard library's meaning:
// "*", IP addresses, CIDR ranges, and domain suffixes with an optional leading
// dot, optionally with a port.
func noProxyMatch(list, host, port string) bool {
	if host == "" || list == "" {
		return false
	}
	ip := net.ParseIP(host)
	fields := strings.FieldsFunc(list, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	for _, f := range fields {
		entry := strings.ToLower(strings.TrimSpace(f))
		if entry == "" {
			continue
		}
		if entry == "*" {
			return true
		}
		if _, n, err := net.ParseCIDR(entry); err == nil {
			if ip != nil && n.Contains(ip) {
				return true
			}
			continue
		}
		if eip := net.ParseIP(entry); eip != nil {
			if ip != nil && ip.Equal(eip) {
				return true
			}
			continue
		}
		target, wantPort := entry, ""
		if h, p, err := net.SplitHostPort(entry); err == nil {
			target, wantPort = h, p
		}
		if wantPort != "" && wantPort != port {
			continue
		}
		target = strings.TrimPrefix(strings.TrimPrefix(target, "*"), ".")
		if target == "" {
			continue
		}
		if host == target || strings.HasSuffix(host, "."+target) {
			return true
		}
	}
	return false
}

// matchExceptions applies an operating-system exception list:
// an exact host (case-insensitive), "*.example.com" or ".example.com" (the
// subdomains and example.com itself), "<local>" (hosts without a dot), and
// empty entries, which are ignored.
func matchExceptions(host string, list []string) bool {
	host = normHost(host)
	if host == "" {
		return false
	}
	for _, raw := range list {
		e := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case e == "":
		case e == "<local>":
			if !strings.Contains(host, ".") {
				return true
			}
		case strings.HasPrefix(e, "*.") || strings.HasPrefix(e, "."):
			d := strings.TrimPrefix(strings.TrimPrefix(e, "*"), ".")
			if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
				return true
			}
		case e == host:
			return true
		}
	}
	return false
}
