// Package netproxy decides which proxy magpie's outgoing requests use.
//
// Order: the proxy setting (or "direct"), then the environment
// (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY, NO_PROXY), then the OS proxy settings,
// then a direct connection. Loopback hosts are never proxied.
package netproxy

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
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

// Install makes the default HTTP transport follow For. Safe to call repeatedly.
func Install() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = ForRequest
	}
}

func getenv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func envHTTPS() string { return getenv("HTTPS_PROXY", "https_proxy") }
func envHTTP() string  { return getenv("HTTP_PROXY", "http_proxy") }
func envAll() string   { return getenv("ALL_PROXY", "all_proxy") }
func envNo() string    { return getenv("NO_PROXY", "no_proxy") }

func envSet() bool { return envHTTPS() != "" || envHTTP() != "" || envAll() != "" }

// isLoopback reports whether host (no port) is localhost, *.localhost or a loopback IP.
func isLoopback(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ForRequest is For as an http.Transport.Proxy function.
func ForRequest(r *http.Request) (*url.URL, error) { return For(r.URL) }

// For returns the proxy to use for u, or nil for a direct connection.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, nil
	}
	host := u.Hostname()
	if host == "" || isLoopback(host) {
		return nil, nil
	}
	raw := strings.TrimSpace(settings.Load().Proxy)
	switch raw {
	case "":
	case settings.Direct:
		return nil, nil
	default:
		p, err := settings.ParseProxy(raw)
		if err != nil {
			return nil, err
		}
		return p, nil
	}
	if envSet() {
		return forEnv(u)
	}
	if sp, ok := systemProxy(); ok && sp.URL != nil {
		if bypass(host, sp.Exceptions) {
			return nil, nil
		}
		return sp.URL, nil
	}
	return nil, nil
}

func forEnv(u *url.URL) (*url.URL, error) {
	var v string
	switch strings.ToLower(u.Scheme) {
	case "https":
		v = envHTTPS()
	case "http":
		v = envHTTP()
	default:
		return nil, nil
	}
	if v == "" {
		v = envAll()
	}
	if v == "" || envNoMatch(u, envNo()) {
		return nil, nil
	}
	p, err := parseEnvProxy(v)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy environment variable %q: %v", v, err)
	}
	return p, nil
}

func parseEnvProxy(v string) (*url.URL, error) {
	p, err := url.Parse(v)
	if err != nil || p.Scheme == "" || p.Host == "" {
		if p2, err2 := url.Parse("http://" + v); err2 == nil && p2.Host != "" {
			return p2, nil
		}
		if err == nil {
			err = fmt.Errorf("missing host")
		}
		return nil, err
	}
	switch p.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		// e.g. "host:port" parsed with host as scheme
		if p2, err2 := url.Parse("http://" + v); err2 == nil && p2.Host != "" {
			return p2, nil
		}
		return nil, fmt.Errorf("unsupported scheme %q", p.Scheme)
	}
	return p, nil
}

// envNoMatch implements NO_PROXY like the Go standard library: "*", host,
// .domain / domain suffix, optional :port, IPs and CIDRs.
func envNoMatch(u *url.URL, noProxy string) bool {
	if noProxy == "" {
		return false
	}
	if noProxy == "*" {
		return true
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	ip := net.ParseIP(host)
	for _, e := range strings.Split(noProxy, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(e); err == nil {
			if ip != nil && n.Contains(ip) {
				return true
			}
			continue
		}
		ePort := ""
		if h, p, err := net.SplitHostPort(e); err == nil {
			e, ePort = h, p
		}
		e = strings.Trim(e, "[]")
		if ePort != "" && ePort != port {
			continue
		}
		if eip := net.ParseIP(e); eip != nil {
			if ip != nil && eip.Equal(ip) {
				return true
			}
			continue
		}
		if e == "" {
			continue
		}
		if strings.HasPrefix(e, "*.") {
			e = e[1:]
		}
		if strings.HasPrefix(e, ".") {
			if strings.HasSuffix(host, e) || host == e[1:] {
				return true
			}
			continue
		}
		if host == e || strings.HasSuffix(host, "."+e) {
			return true
		}
	}
	return false
}

// Describe reports the proxy outgoing vendor requests currently use and where
// that choice comes from. The address is empty for "off" and "none".
func Describe() (proxy, source string) {
	raw := strings.TrimSpace(settings.Load().Proxy)
	switch raw {
	case "":
	case settings.Direct:
		return "", SourceOff
	default:
		if p, err := settings.ParseProxy(raw); err == nil {
			return p.String(), SourceSettings
		}
		return raw, SourceSettings
	}
	if envSet() {
		v := envHTTPS()
		if v == "" {
			v = envHTTP()
		}
		if v == "" {
			v = envAll()
		}
		if p, err := parseEnvProxy(v); err == nil {
			return p.String(), SourceEnvironment
		}
		return v, SourceEnvironment
	}
	if sp, ok := systemProxy(); ok && sp.URL != nil {
		return sp.URL.String(), SourceSystem
	}
	return "", SourceNone
}
