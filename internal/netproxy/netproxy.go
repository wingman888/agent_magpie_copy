// Package netproxy decides which proxy, if any, a request from magpie goes
// through. The order is the one the user sees in settings:
//
//  1. the proxy in settings.json ("direct" turns the proxy off),
//  2. the environment: NO_PROXY, then HTTPS_PROXY / HTTP_PROXY / ALL_PROXY,
//  3. the system proxy: macOS network settings, Windows Internet options.
//
// Loopback addresses never go through a proxy. Linux has no system layer;
// there the environment is the only source.
package netproxy

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// System is the proxy the operating system is configured with. An empty
// address means that protocol has no proxy.
type System struct {
	HTTPS, HTTP, SOCKS string   // proxy URLs
	Exceptions         []string // hosts that bypass the proxy; "*" and "*.x" globs, CIDR, suffixes
	ExcludePlain       bool     // hosts without a dot bypass the proxy ("<local>", simple hostnames)
}

// Injectable for tests. The environment is read with os.Getenv directly.
var (
	settingsProxy = func() string { return settings.Load().Proxy }
	systemProxy   = cachedSystem
)

// For returns the proxy a request to u should use, or nil for a direct
// connection.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, nil
	}
	host := u.Hostname()
	if IsLoopback(host) {
		return nil, nil
	}
	if p := settingsProxy(); p != "" {
		canon, err := settings.NormalizeProxy(p)
		if err != nil {
			return nil, err
		}
		if canon == settings.Direct {
			return nil, nil
		}
		return parse(canon)
	}
	if noProxy(host, portOf(u)) {
		return nil, nil
	}
	if e := envProxy(u.Scheme); e != "" {
		return parse(e)
	}
	return systemFor(host, portOf(u), systemProxy())
}

// ProxyFunc is For in the form http.Transport.Proxy takes. A socks5h proxy is
// handed to the transport as socks5: the transport already sends host names
// unresolved, which is what socks5h asks for.
func ProxyFunc(r *http.Request) (*url.URL, error) {
	p, err := For(r.URL)
	if err != nil || p == nil {
		return p, err
	}
	if p.Scheme == "socks5h" {
		c := *p
		c.Scheme = "socks5"
		return &c, nil
	}
	return p, nil
}

// Install makes the process-wide default transport use ProxyFunc, so the
// clients that use http.DefaultClient (provider tests, model lists, catalog,
// updates) follow the same rules as the gateway.
func Install() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = ProxyFunc
	}
}

// IsLoopback reports whether host is the local machine: localhost, 127.0.0.0/8
// or ::1.
func IsLoopback(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "http" {
		return "80"
	}
	return "443"
}

func envProxy(scheme string) string {
	names := []string{"ALL_PROXY", "all_proxy"}
	switch scheme {
	case "https":
		names = append([]string{"HTTPS_PROXY", "https_proxy"}, names...)
	case "http":
		names = append([]string{"HTTP_PROXY", "http_proxy"}, names...)
	}
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func noProxy(host, port string) bool {
	list := os.Getenv("NO_PROXY")
	if list == "" {
		list = os.Getenv("no_proxy")
	}
	return excluded(strings.Split(list, ","), false, host, port)
}

// systemFor applies the system exceptions, then picks HTTPS, HTTP, SOCKS.
func systemFor(host, port string, sys System) (*url.URL, error) {
	if excluded(sys.Exceptions, sys.ExcludePlain, host, port) {
		return nil, nil
	}
	for _, s := range []string{sys.HTTPS, sys.HTTP, sys.SOCKS} {
		if s != "" {
			return parse(s)
		}
	}
	return nil, nil
}

// excluded reports whether host matches one of the entries. Entries may be
// "*", a glob ("*.local", "192.168.*"), a CIDR or partial CIDR ("169.254/16"),
// a domain (matches itself and its subdomains, with or without a leading dot),
// or any of those followed by a port, which then also has to match.
func excluded(list []string, plain bool, host, port string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "" {
		return false
	}
	ip := net.ParseIP(h)
	if plain && ip == nil && !strings.Contains(h, ".") {
		return true
	}
	for _, raw := range list {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == "" || p == "<local>" {
			continue
		}
		if p == "*" {
			return true
		}
		if hp, pt, err := net.SplitHostPort(p); err == nil {
			if pt != port {
				continue
			}
			p = hp
		}
		if n := cidrOf(p); n != nil {
			if ip != nil && n.Contains(ip) {
				return true
			}
			continue
		}
		if strings.ContainsAny(p, "*?[") {
			if ok, _ := path.Match(p, h); ok {
				return true
			}
			continue
		}
		p = strings.TrimPrefix(p, ".")
		if h == p || strings.HasSuffix(h, "."+p) {
			return true
		}
	}
	return false
}

// cidrOf parses "10.0.0.0/8", "fe80::/10" and the partial form "169.254/16"
// that macOS writes. It returns nil for anything else.
func cidrOf(p string) *net.IPNet {
	addr, bits, ok := strings.Cut(p, "/")
	if !ok {
		return nil
	}
	parts := strings.Split(addr, ".")
	if len(parts) < 4 && allDigits(parts) {
		for len(parts) < 4 {
			parts = append(parts, "0")
		}
		addr = strings.Join(parts, ".")
	}
	_, n, err := net.ParseCIDR(addr + "/" + bits)
	if err != nil {
		return nil
	}
	return n
}

func allDigits(parts []string) bool {
	for _, s := range parts {
		if s == "" {
			return false
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// parse reads a proxy address; a bare host:port is http.
func parse(s string) (*url.URL, error) {
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	if u.Host == "" {
		return nil, errors.New("netproxy: proxy has no host: " + s)
	}
	return u, nil
}

// systemCache keeps the system settings for a short while: reading them means
// running a program on macOS and Windows, and every request asks.
var systemCache struct {
	sync.Mutex
	at    time.Time
	value System
}

func cachedSystem() System {
	systemCache.Lock()
	defer systemCache.Unlock()
	if !systemCache.at.IsZero() && time.Since(systemCache.at) < 30*time.Second {
		return systemCache.value
	}
	systemCache.value = readSystem()
	systemCache.at = time.Now()
	return systemCache.value
}
