// Package netproxy decides which proxy, if any, magpie's own outgoing
// requests use. The order is: the proxy in magpie's settings ("direct"
// turning it off), then the usual proxy environment variables, then the
// operating system's proxy settings, then none. Loopback hosts are never
// proxied, whatever is configured.
//
// Apps started from the macOS Dock or the Windows Start menu don't inherit
// a shell's HTTPS_PROXY, so reading the OS settings is what lets those
// launches reach vendors that are only reachable through a proxy.
package netproxy

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// Sources reported by Describe.
const (
	SourceSettings    = "settings"
	SourceOff         = "off"
	SourceEnvironment = "environment"
	SourceSystem      = "system"
	SourceNone        = "none"
)

// Install makes http.DefaultTransport (and so http.DefaultClient and every
// client without its own transport) follow the same rules as For.
func Install() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = ProxyFunc
	}
}

// ProxyFunc is For in the shape http.Transport.Proxy wants.
func ProxyFunc(r *http.Request) (*url.URL, error) {
	if r == nil || r.URL == nil {
		return nil, nil
	}
	return For(r.URL)
}

// For returns the proxy to use for u; nil means connect directly. The
// settings are read on every call, so a change applies to the next request.
func For(u *url.URL) (*url.URL, error) {
	if u == nil || isLoopback(u.Hostname()) {
		return nil, nil
	}
	if p := settings.Load().Proxy; p != "" {
		if settings.IsDirect(p) {
			return nil, nil
		}
		return settings.ParseProxy(p)
	}
	if env := fromEnvironment(); env.has(u.Scheme) {
		return env.proxyFor(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, nil
	}
	sp := system()
	if sp == nil || sp.proxy == nil || matchException(u.Hostname(), sp.exceptions) {
		return nil, nil
	}
	c := *sp.proxy
	return &c, nil
}

// describeURL stands in for "a vendor" when Describe reports what applies.
var describeURL = &url.URL{Scheme: "https", Host: "api.openai.com"}

// Describe reports the proxy vendor requests currently use and where it
// comes from: settings, off, environment, system or none. The proxy is ""
// for off and none.
func Describe() (proxy, source string) {
	if p := settings.Load().Proxy; p != "" {
		if settings.IsDirect(p) {
			return "", SourceOff
		}
		if u, err := settings.ParseProxy(p); err == nil {
			return u.String(), SourceSettings
		}
		return p, SourceSettings
	}
	if env := fromEnvironment(); env.has(describeURL.Scheme) {
		raw := env.raw(describeURL.Scheme)
		if u, err := parseEnvProxy(raw); err == nil {
			return u.String(), SourceEnvironment
		}
		return raw, SourceEnvironment
	}
	if sp := system(); sp != nil && sp.proxy != nil {
		return sp.proxy.String(), SourceSystem
	}
	return "", SourceNone
}

// isLoopback reports whether host is localhost, *.localhost or a loopback IP.
func isLoopback(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if i := strings.IndexByte(h, '%'); i >= 0 { // fe80::1%lo0 style zones
		h = h[:i]
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// matchException reports whether host is in an OS proxy exception list:
// exact names (case-insensitive), "*.example.com" or ".example.com" for the
// domain and its subdomains, and "<local>" for names without a dot.
func matchException(host string, list []string) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	if h == "" {
		return false
	}
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == "<local>" {
			if !strings.Contains(h, ".") && net.ParseIP(h) == nil {
				return true
			}
			continue
		}
		if e == "*" {
			return true
		}
		if hh, _, err := net.SplitHostPort(e); err == nil && !strings.HasPrefix(e, "[") && strings.Count(e, ":") == 1 {
			e = hh
		}
		e = strings.TrimSuffix(strings.TrimPrefix(e, "["), "]")
		e = strings.TrimSuffix(e, ".")
		switch {
		case strings.HasPrefix(e, "*."):
			d := e[2:]
			if d != "" && (h == d || strings.HasSuffix(h, "."+d)) {
				return true
			}
		case strings.HasPrefix(e, "."):
			d := e[1:]
			if d != "" && (h == d || strings.HasSuffix(h, "."+d)) {
				return true
			}
		case strings.Contains(e, "*"):
			if wildcard(e, h) {
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

// wildcard matches s against a pattern where * stands for any run of
// characters, as OS exception lists such as "192.168.*" use.
func wildcard(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for i, p := range parts[1:] {
		if i == len(parts)-2 {
			return strings.HasSuffix(s, p)
		}
		j := strings.Index(s, p)
		if j < 0 {
			return false
		}
		s = s[j+len(p):]
	}
	return true
}
