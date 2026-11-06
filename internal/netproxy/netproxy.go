// Package netproxy decides which proxy magpie's own outbound requests to
// model vendors go through. The rules, highest first:
//
//  1. the proxy setting (settings.Proxy): "direct" or a proxy URL;
//  2. the proxy environment variables (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY,
//     NO_PROXY and their lowercase forms), with the standard library's meaning;
//  3. the operating system's proxy settings (macOS and Windows only);
//  4. a direct connection.
//
// Loopback hosts always connect directly, whatever the level above says.
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

// systemTTL bounds how stale a cached operating-system proxy setting may be.
const systemTTL = 10 * time.Second

// sources reported by Describe.
const (
	sourceSettings    = "settings"
	sourceOff         = "off"
	sourceEnvironment = "environment"
	sourceSystem      = "system"
	sourceNone        = "none"
)

// sysConfig is the operating system's proxy setting, reduced to one proxy
// URL ("" when no proxy is enabled) and the hosts that bypass it.
type sysConfig struct {
	proxy      string
	exceptions []string
}

// readSystemProxy reads the operating system's setting. It is a variable so
// tests can replace it.
var readSystemProxy = func() (sysConfig, error) { return readOSProxy() }

var (
	sysMu    sync.Mutex
	sysValue sysConfig
	sysTime  time.Time
	sysSet   bool
)

// systemConfig returns the operating system's setting, cached for systemTTL.
// A failed read counts as "no system proxy".
func systemConfig() sysConfig {
	sysMu.Lock()
	defer sysMu.Unlock()
	if sysSet && time.Since(sysTime) < systemTTL {
		return sysValue
	}
	cfg, err := readSystemProxy()
	if err != nil {
		cfg = sysConfig{}
	}
	sysValue, sysTime, sysSet = cfg, time.Now(), true
	return cfg
}

// For returns the proxy that requests to u should use, or nil to connect
// directly. An unusable proxy setting or environment value is an error, never
// a silent direct connection.
func For(u *url.URL) (*url.URL, error) {
	if u == nil || isLoopback(u.Hostname()) {
		return nil, nil
	}
	raw, _ := decide(u)
	if raw == "" {
		return nil, nil
	}
	return parseProxy(raw)
}

// Describe reports the proxy that vendor requests use now and where it comes
// from. proxy is "" when source is "off" or "none".
func Describe() (proxy, source string) {
	raw, src := decide(nil)
	if raw == "" {
		if src == sourceOff {
			return "", sourceOff
		}
		return "", sourceNone
	}
	if u, err := parseProxy(raw); err == nil {
		return u.String(), src
	}
	return raw, src
}

// decide returns the raw proxy text chosen for u ("" means direct) and the
// source of that choice. A nil u means no particular host, so host-based
// exceptions are not applied.
func decide(u *url.URL) (raw, source string) {
	p := strings.TrimSpace(settings.Load().Proxy)
	switch {
	case p == "direct":
		return "", sourceOff
	case p != "":
		return p, sourceSettings
	}
	if envValue("HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy") != "" {
		return envRaw(u), sourceEnvironment
	}
	sys := systemConfig()
	if sys.proxy == "" {
		return "", sourceNone
	}
	if u != nil && bypassSystem(u.Hostname(), sys.exceptions) {
		return "", sourceSystem
	}
	return sys.proxy, sourceSystem
}

// envRaw picks the environment proxy for u the way the standard library does:
// the scheme-specific variable first, then ALL_PROXY, and NO_PROXY can
// exempt the host.
func envRaw(u *url.URL) string {
	var raw string
	if u != nil && u.Scheme == "http" {
		raw = envValue("HTTP_PROXY", "http_proxy")
	} else {
		raw = envValue("HTTPS_PROXY", "https_proxy")
	}
	if raw == "" {
		raw = envValue("ALL_PROXY", "all_proxy")
	}
	if raw == "" || u == nil {
		return raw
	}
	if noProxyMatch(u) {
		return ""
	}
	return raw
}

// parseProxy turns a setting or environment value into a proxy URL. A bare
// host:port is HTTP. Anything settings.ValidateProxy rejects is an error.
func parseProxy(raw string) (*url.URL, error) {
	if err := settings.ValidateProxy(raw); err != nil {
		return nil, err
	}
	s := raw
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("proxy %q: %w", raw, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("proxy %q: unsupported scheme %q", raw, u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("proxy %q: missing host", raw)
	}
	return u, nil
}

// isLoopback reports whether host is localhost, *.localhost, or a loopback IP.
func isLoopback(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// envValue returns the first non-empty value among the named variables.
func envValue(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// noProxyMatch applies NO_PROXY to u with the standard library's rules:
// "*", host or .domain suffixes, IPs, CIDRs, and optional host:port entries.
func noProxyMatch(u *url.URL) bool {
	list := envValue("NO_PROXY", "no_proxy")
	if list == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
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
		if _, cidr, err := net.ParseCIDR(e); err == nil {
			if ip != nil && cidr.Contains(ip) {
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
		eh = strings.Trim(eh, "[]")
		if eip := net.ParseIP(eh); eip != nil {
			if ip != nil && eip.Equal(ip) {
				return true
			}
			continue
		}
		eh = strings.TrimPrefix(eh, "*")
		if eh == "" {
			continue
		}
		if !strings.HasPrefix(eh, ".") {
			eh = "." + eh
		}
		if host == eh[1:] || strings.HasSuffix(host, eh) {
			return true
		}
	}
	return false
}

// bypassSystem applies the operating system's exception list to host:
// exact names, "*.example.com" or ".example.com" (subdomains and the apex),
// and "<local>" (names without a dot). Empty items are ignored.
func bypassSystem(host string, list []string) bool {
	h := strings.ToLower(host)
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
		case e == "<local>":
			if !strings.Contains(h, ".") {
				return true
			}
		case strings.HasPrefix(e, "*.") || strings.HasPrefix(e, "."):
			d := strings.TrimPrefix(strings.TrimPrefix(e, "*"), ".")
			if h == d || strings.HasSuffix(h, "."+d) {
				return true
			}
		default:
			if h == e {
				return true
			}
		}
	}
	return false
}

var installOnce sync.Once

// Install makes http.DefaultTransport follow For. Call it once at startup;
// further calls do nothing.
func Install() {
	installOnce.Do(func() {
		t, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return
		}
		c := t.Clone()
		c.Proxy = func(r *http.Request) (*url.URL, error) { return For(r.URL) }
		http.DefaultTransport = c
	})
}
