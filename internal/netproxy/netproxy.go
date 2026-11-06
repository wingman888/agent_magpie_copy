// Package netproxy decides which proxy magpie's own outbound requests use.
//
// The order is: the proxy in settings (proxy: "direct" means none), then the
// environment (HTTPS_PROXY / HTTP_PROXY with NO_PROXY), then the system proxy
// (macOS network settings, Windows Internet options; Linux has none of its
// own). Loopback addresses never go through a proxy.
package netproxy

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// sysProxy is the operating system's proxy setting. Each address is a full
// URL (http://, https:// or socks5://); "" means none. Bypass is the exception
// list, where "<local>" stands for hostnames without a dot.
type sysProxy struct {
	HTTPS, HTTP, SOCKS string
	Bypass             []string
}

// systemProxy returns the OS proxy. It is a variable so tests can replace it.
var systemProxy = cachedSystem

var (
	sysMu  sync.Mutex
	sysAt  time.Time
	sysVal sysProxy
)

// cachedSystem reads the OS setting at most every 30 seconds: the readers
// run a command on macOS and Windows, and this is called per request.
func cachedSystem() sysProxy {
	sysMu.Lock()
	defer sysMu.Unlock()
	if !sysAt.IsZero() && time.Since(sysAt) < 30*time.Second {
		return sysVal
	}
	sysVal = readOS()
	sysAt = time.Now()
	return sysVal
}

// For returns the proxy a request to u should use, or nil to connect directly.
// Errors never include proxy credentials.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, errors.New("netproxy: nil URL")
	}
	host := u.Hostname()
	if isLoopback(host) {
		return nil, nil
	}

	// 1. settings
	set := settings.Load().Proxy
	if strings.EqualFold(set, "direct") {
		return nil, nil
	}
	if set != "" {
		return settings.ProxyURL(set)
	}

	// 2. environment
	noProxy := envValue("NO_PROXY", "no_proxy")
	raw, name := envValue("HTTP_PROXY", "http_proxy"), "HTTP_PROXY"
	if u.Scheme == "https" {
		raw, name = envValue("HTTPS_PROXY", "https_proxy"), "HTTPS_PROXY"
	}
	if raw != "" {
		if bypassed(host, splitList(noProxy)) {
			return nil, nil
		}
		p, err := settings.ProxyURL(raw)
		if err != nil {
			return nil, errors.New("netproxy: invalid proxy in " + name + ": " + err.Error())
		}
		return p, nil
	}

	// 3. system
	sys := systemProxy()
	if bypassed(host, sys.Bypass) {
		return nil, nil
	}
	for _, addr := range []string{sys.HTTPS, sys.HTTP, sys.SOCKS} {
		if addr != "" {
			return settings.ProxyURL(addr)
		}
	}
	return nil, nil
}

// Proxy is For for an http.Transport's Proxy field.
func Proxy(r *http.Request) (*url.URL, error) {
	return For(r.URL)
}

// Transport is a new http.Transport with the defaults of http.DefaultTransport
// and the proxy chosen by For. Callers that set their own fields should set
// Proxy to netproxy.Proxy instead.
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = Proxy
	return t
}

var client = &http.Client{Transport: Transport()}

// Client is a shared client for magpie's outbound requests, proxied as above.
func Client() *http.Client { return client }

func envValue(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func splitList(s string) []string {
	return strings.Split(s, ",")
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// bypassed reports whether host matches an exception list entry:
// "*" matches everything; "<local>" matches names without a dot; "*.x.com"
// and ".x.com" match subdomains; "10.0.0.0/8" matches addresses in a range;
// any other entry matches the host itself or its subdomains.
func bypassed(host string, list []string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	ip := net.ParseIP(h)
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
			continue
		case e == "*":
			return true
		case e == "<local>":
			if ip == nil && !strings.Contains(h, ".") {
				return true
			}
		case strings.Contains(e, "/"):
			if _, n, err := net.ParseCIDR(e); err == nil && ip != nil && n.Contains(ip) {
				return true
			}
		case strings.HasPrefix(e, "*."):
			if strings.HasSuffix(h, e[1:]) {
				return true
			}
		case strings.HasPrefix(e, "."):
			if strings.HasSuffix(h, e) {
				return true
			}
		case strings.HasSuffix(e, "*"):
			// Windows writes "192.168.*": a prefix match on what precedes the *
			if strings.HasPrefix(h, strings.TrimSuffix(e, "*")) {
				return true
			}
		default:
			if h == e || strings.HasSuffix(h, "."+e) {
				return true
			}
			if ip != nil && ip.Equal(net.ParseIP(e)) {
				return true
			}
		}
	}
	return false
}
