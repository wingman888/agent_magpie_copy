// Package netproxy decides which proxy magpie's own outbound requests use.
//
// The order is: the proxy setting in magpie's settings file, then the
// HTTPS_PROXY family of environment variables (with NO_PROXY), then the
// system proxy (macOS network settings, Windows Internet Options). Linux has
// no system proxy beyond the environment. Loopback is never proxied.
package netproxy

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

func init() {
	// everything that uses http.DefaultClient follows the same rules
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = Func
	}
}

// Func is an http.Transport.Proxy.
func Func(r *http.Request) (*url.URL, error) { return For(r.URL) }

// Setting returns the proxy value from the settings; tests replace it.
var Setting = func() string { return settings.Load().Proxy }

// System returns the system proxy configuration; tests replace it.
var System = cachedSystem

// For returns the proxy a request to u should use; nil means direct.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, nil
	}
	host := u.Hostname()
	if isLoopback(host) {
		return nil, nil
	}
	switch s := strings.TrimSpace(Setting()); {
	case strings.EqualFold(s, "direct"):
		return nil, nil
	case s != "":
		return parseProxy(s)
	}
	if px, used, err := fromEnv(u); used {
		return px, err
	}
	if cfg := System(); cfg != nil {
		return cfg.proxyFor(u)
	}
	return nil, nil
}

func isLoopback(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// parseProxy accepts a proxy URL or a bare host:port (an http proxy).
func parseProxy(s string) (*url.URL, error) {
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	p, err := url.Parse(s)
	if err != nil || p.Hostname() == "" {
		return nil, errBad(s)
	}
	switch strings.ToLower(p.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return p, nil
	}
	return nil, errBad(s)
}

type errBad string

func (e errBad) Error() string { return "invalid proxy " + string(e) }

// ---- environment --------------------------------------------------------

func env(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// fromEnv reports the proxy the environment names for u, and whether the
// environment configures a proxy at all (otherwise the system gets a say).
func fromEnv(u *url.URL) (*url.URL, bool, error) {
	https := env("HTTPS_PROXY", "https_proxy")
	http_ := env("HTTP_PROXY", "http_proxy")
	all := env("ALL_PROXY", "all_proxy")
	if https == "" && http_ == "" && all == "" {
		return nil, false, nil
	}
	var v string
	if strings.EqualFold(u.Scheme, "https") {
		v = https
	} else {
		v = http_
	}
	if v == "" {
		v = all
	}
	if v == "" {
		return nil, true, nil
	}
	if matchExceptions(u, splitList(env("NO_PROXY", "no_proxy"), ","), false) {
		return nil, true, nil
	}
	p, err := parseProxy(v)
	if err != nil {
		return nil, true, err
	}
	return p, true, nil
}

func splitList(s, sep string) []string {
	var out []string
	for _, f := range strings.Split(s, sep) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// ---- exceptions ---------------------------------------------------------

// matchExceptions reports whether u's host is on a no-proxy list. sys turns
// on the system-list extras: "<local>" and wildcard patterns.
func matchExceptions(u *url.URL, list []string, sys bool) bool {
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	ip := net.ParseIP(host)
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == "*" {
			return true
		}
		if e == "<local>" {
			if sys && !strings.Contains(host, ".") && ip == nil {
				return true
			}
			continue
		}
		if strings.Contains(e, "://") {
			if x, err := url.Parse(e); err == nil {
				e = x.Host
			}
		}
		// CIDR, including the short macOS form "169.254/16"
		if i := strings.Index(e, "/"); i > 0 {
			if ip != nil {
				if _, n, err := net.ParseCIDR(padCIDR(e)); err == nil && n.Contains(ip) {
					return true
				}
			}
			continue
		}
		pat, pport := e, ""
		if h, p, err := net.SplitHostPort(e); err == nil {
			pat, pport = h, p
		} else if strings.HasPrefix(e, "[") && strings.HasSuffix(e, "]") {
			pat = e[1 : len(e)-1]
		}
		if pport != "" && pport != port {
			continue
		}
		if matchHost(host, ip, pat, sys) {
			return true
		}
	}
	return false
}

func padCIDR(c string) string {
	i := strings.Index(c, "/")
	addr, bits := c[:i], c[i:]
	if !strings.Contains(addr, ":") {
		for strings.Count(addr, ".") < 3 {
			addr += ".0"
		}
	}
	return addr + bits
}

func matchHost(host string, ip net.IP, pat string, sys bool) bool {
	if pat == host {
		return true
	}
	if strings.Contains(pat, "*") {
		return wildcard(pat, host)
	}
	if ip != nil {
		if pip := net.ParseIP(pat); pip != nil {
			return pip.Equal(ip)
		}
		return false
	}
	pat = strings.TrimPrefix(pat, ".")
	return host == pat || strings.HasSuffix(host, "."+pat)
}

// wildcard matches pat, where * stands for any run of characters.
func wildcard(pat, s string) bool {
	parts := strings.Split(pat, "*")
	if len(parts) == 1 {
		return pat == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, last)
}

// ---- system -------------------------------------------------------------

// SystemConfig is the proxy configuration of the operating system.
type SystemConfig struct {
	HTTPS, HTTP, SOCKS *url.URL
	Exceptions         []string
	ExcludeSimple      bool // host names without a dot go direct
}

func (c *SystemConfig) proxyFor(u *url.URL) (*url.URL, error) {
	if c.ExcludeSimple && !strings.Contains(u.Hostname(), ".") && net.ParseIP(u.Hostname()) == nil {
		return nil, nil
	}
	if matchExceptions(u, c.Exceptions, true) {
		return nil, nil
	}
	switch {
	case c.HTTPS != nil:
		return c.HTTPS, nil
	case c.HTTP != nil:
		return c.HTTP, nil
	case c.SOCKS != nil:
		return c.SOCKS, nil
	}
	return nil, nil
}

var (
	sysMu   sync.Mutex
	sysAt   time.Time
	sysConf *SystemConfig
)

func cachedSystem() *SystemConfig {
	sysMu.Lock()
	defer sysMu.Unlock()
	if sysAt.IsZero() || time.Since(sysAt) > 30*time.Second {
		sysConf, sysAt = readSystem(), time.Now()
	}
	return sysConf
}
