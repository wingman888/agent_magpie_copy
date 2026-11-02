// Package netproxy decides which proxy magpie's outgoing requests use:
// the user's setting first, then the environment (HTTPS_PROXY, NO_PROXY and
// friends), then the operating system's proxy configuration.
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
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = For
	}
}

// Transport is a ready http.Transport that follows For.
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = For
	return t
}

// For returns the proxy a request to u should use; nil means direct.
func For(u *url.URL) (*url.URL, error) {
	if u == nil || u.Hostname() == "" || isLoopback(u.Hostname()) {
		return nil, nil
	}
	set := settings.Load().Proxy
	if set == "direct" {
		return nil, nil
	}
	if set != "" {
		return settings.ParseProxy(set)
	}
	if p, decided := fromEnv(u); decided {
		return p, nil
	}
	return fromSystem(u), nil
}

func isLoopback(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func getenv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// fromEnv reports the environment's proxy for u; decided is false when the
// environment names no proxy for it (so the system gets a say).
func fromEnv(u *url.URL) (*url.URL, bool) {
	var v string
	if u.Scheme == "https" {
		v = getenv("HTTPS_PROXY", "https_proxy")
	} else {
		v = getenv("HTTP_PROXY", "http_proxy")
	}
	if v == "" {
		v = getenv("ALL_PROXY", "all_proxy")
	}
	if v == "" {
		return nil, false
	}
	if noProxy(u.Hostname(), u.Port(), getenv("NO_PROXY", "no_proxy")) {
		return nil, true
	}
	p, err := settings.ParseProxy(v)
	if err != nil || p == nil {
		return nil, false
	}
	return p, true
}

// noProxy implements the NO_PROXY convention: comma separated hosts,
// domain suffixes (with or without a leading dot or *.), IPs, CIDRs, "*".
func noProxy(host, port, list string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
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
		if h, p, err := net.SplitHostPort(e); err == nil && p != "" {
			if p != port {
				continue
			}
			e = h
		}
		e = strings.TrimPrefix(strings.TrimPrefix(e, "*"), ".")
		if host == e || strings.HasSuffix(host, "."+e) {
			return true
		}
	}
	return false
}

// sysConfig is an operating system's proxy configuration.
type sysConfig struct {
	HTTPS, HTTP, SOCKS *url.URL
	Exceptions         []string
	ExcludeSimple      bool // hosts without a dot bypass the proxy
}

// readSystem is replaced in tests.
var readSystem = platformSystem

var (
	sysMu    sync.Mutex
	sysAt    time.Time
	sysCache *sysConfig
)

func systemConfig() *sysConfig {
	sysMu.Lock()
	defer sysMu.Unlock()
	if !sysAt.IsZero() && time.Since(sysAt) < 30*time.Second {
		return sysCache
	}
	sysCache, sysAt = readSystem(), time.Now()
	return sysCache
}

func fromSystem(u *url.URL) *url.URL {
	c := systemConfig()
	if c == nil {
		return nil
	}
	return c.pick(u)
}

func (c *sysConfig) pick(u *url.URL) *url.URL {
	if c.bypass(u.Hostname()) {
		return nil
	}
	switch {
	case c.HTTPS != nil:
		return c.HTTPS
	case c.HTTP != nil:
		return c.HTTP
	}
	return c.SOCKS
}

func (c *sysConfig) bypass(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if c.ExcludeSimple && !strings.Contains(host, ".") && net.ParseIP(host) == nil {
		return true
	}
	ip := net.ParseIP(host)
	for _, e := range c.Exceptions {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == "<local>" {
			if !strings.Contains(host, ".") && ip == nil {
				return true
			}
			continue
		}
		if _, n, err := net.ParseCIDR(e); err == nil {
			if ip != nil && n.Contains(ip) {
				return true
			}
			continue
		}
		if strings.Contains(e, "*") {
			if globMatch(e, host) {
				return true
			}
			// "*.example.com" also covers example.com itself
			if rest, ok := strings.CutPrefix(e, "*."); ok && !strings.Contains(rest, "*") && host == rest {
				return true
			}
			continue
		}
		if host == e || strings.HasSuffix(host, "."+strings.TrimPrefix(e, ".")) {
			return true
		}
	}
	return false
}

// globMatch matches s against a pattern whose only wildcard is "*".
func globMatch(pat, s string) bool {
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
