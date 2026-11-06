// Package netproxy decides which proxy magpie's own outbound requests (to
// model vendors) go through. The order, highest first, is:
//
//  1. the "proxy" setting: "direct" means no proxy, anything else is the proxy;
//  2. the environment: HTTPS_PROXY, HTTP_PROXY, ALL_PROXY and NO_PROXY, with
//     the same meaning as Go's standard library;
//  3. the operating system proxy (macOS and Windows only);
//  4. no proxy.
//
// Loopback hosts are never proxied, whatever the level above says.
package netproxy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// sysTTL is how long a read of the operating system proxy is reused.
const sysTTL = 10 * time.Second

// readSystemProxy reads the operating system proxy. It is a variable so tests
// can replace it.
var readSystemProxy = readSystem

var (
	sysMu    sync.Mutex
	sysCache sysConfig
	sysAt    time.Time
	sysValid bool
)

// systemConfig returns the operating system proxy, reusing a recent read.
func systemConfig() sysConfig {
	sysMu.Lock()
	defer sysMu.Unlock()
	if sysValid && time.Since(sysAt) < sysTTL {
		return sysCache
	}
	sysCache = readSystemProxy()
	sysAt = time.Now()
	sysValid = true
	return sysCache
}

// resetSystemCache forgets the cached operating system proxy.
func resetSystemCache() {
	sysMu.Lock()
	defer sysMu.Unlock()
	sysValid = false
}

var (
	installMu          sync.Mutex
	installedTransport *http.Transport // the transport last installed by Install
)

// Install makes http.DefaultTransport follow the same proxy rules. It changes
// nothing if the default transport is not an *http.Transport. It may be called
// again; if http.DefaultTransport has been replaced since the last call (for
// example restored by a test), the new one is set up as well.
func Install() {
	installMu.Lock()
	defer installMu.Unlock()
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return
	}
	if tr == installedTransport && tr.Proxy != nil {
		return
	}
	c := tr.Clone()
	c.Proxy = func(r *http.Request) (*url.URL, error) { return For(r.URL) }
	http.DefaultTransport = c
	installedTransport = c
}

// For returns the proxy to use when requesting u, or nil to connect directly.
// The settings are read on every call, so a change applies at once.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, errors.New("netproxy: nil URL")
	}
	if isLoopback(u.Hostname()) {
		return nil, nil
	}
	p, _, err := choose(u)
	return p, err
}

// Describe returns the proxy that outbound requests use in general, and where
// it comes from: settings, off, environment, system or none. It describes the
// global configuration, so per-host exceptions are not applied. For "off" and
// "none" the proxy is empty.
func Describe() (proxy, source string) {
	s := strings.TrimSpace(settings.Load().Proxy)
	switch s {
	case "":
	case "direct":
		return "", "off"
	default:
		return displayProxy(s), "settings"
	}
	// Same variables as For uses for https requests, so the two agree.
	if v := envProxy("https"); v != "" {
		return displayProxy(v), "environment"
	}
	if cfg := systemConfig(); cfg.proxy != "" {
		return displayProxy(cfg.proxy), "system"
	}
	return "", "none"
}

// choose applies the priority order to a non-loopback URL. The returned source
// is one of settings, off, environment, system or none.
func choose(u *url.URL) (*url.URL, string, error) {
	host := u.Hostname()

	s := strings.TrimSpace(settings.Load().Proxy)
	switch s {
	case "":
	case "direct":
		return nil, "off", nil
	default:
		p, err := parseSettingProxy(s)
		if err != nil {
			return nil, "settings", fmt.Errorf("netproxy: 设置里的代理不可用: %w", err)
		}
		return p, "settings", nil
	}

	if raw := envProxy(u.Scheme); raw != "" {
		if noProxyMatch(host, portOf(u)) {
			return nil, "environment", nil
		}
		p, err := parseProxy(raw)
		if err != nil {
			return nil, "environment", fmt.Errorf("netproxy: 环境变量里的代理不可用: %w", err)
		}
		return p, "environment", nil
	}

	cfg := systemConfig()
	if cfg.proxy == "" {
		return nil, "none", nil
	}
	if matchException(host, cfg.exceptions) {
		return nil, "system", nil
	}
	p, err := parseProxy(cfg.proxy)
	if err != nil {
		return nil, "none", nil
	}
	return p, "system", nil
}

// parseProxy turns a proxy setting or environment value into a URL. A value
// without a scheme is HTTP. Only http, https, socks5 and socks5h are accepted.
func parseProxy(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, errors.New("代理地址为空")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("代理地址无效: %q，正确写法示例: http://127.0.0.1:7890", raw)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return u, nil
	}
	return nil, fmt.Errorf("代理地址无效: %q，正确写法示例: http://127.0.0.1:7890", raw)
}

// parseSettingProxy is parseProxy for the settings value. Without a scheme the
// value must be a host:port with a port from 1 to 65535, the same rule as
// settings.Save, so a bare host such as "127.0.0.1" is an error.
func parseSettingProxy(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "://") {
		host, port, err := net.SplitHostPort(s)
		if err != nil || host == "" {
			return nil, fmt.Errorf("代理地址无效: %q，正确写法示例: http://127.0.0.1:7890", raw)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("代理地址无效: %q，正确写法示例: http://127.0.0.1:7890", raw)
		}
	}
	return parseProxy(s)
}

// displayProxy formats a proxy for Describe. A value that does not parse is
// returned as it is.
func displayProxy(s string) string {
	u, err := parseProxy(s)
	if err != nil {
		return s
	}
	return u.String()
}

// envProxy returns the environment proxy that applies to a scheme, or "" when
// no variable applies. It reads the variables on each call.
func envProxy(scheme string) string {
	switch strings.ToLower(scheme) {
	case "https", "wss":
		return getenv("HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy")
	}
	return getenv("HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy")
}

// getenv returns the first non-empty variable among names.
func getenv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// noProxyMatch reports whether NO_PROXY lists the host (and port), using the
// rules of Go's standard library.
func noProxyMatch(host, port string) bool {
	list := getenv("NO_PROXY", "no_proxy")
	if list == "" {
		return false
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	ip := net.ParseIP(h)
	for _, e := range strings.Split(list, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == "*" {
			return true
		}
		if eh, ep, err := net.SplitHostPort(e); err == nil {
			if ep != port {
				continue
			}
			e = eh
		}
		if _, cidr, err := net.ParseCIDR(e); err == nil {
			if ip != nil && cidr.Contains(ip) {
				return true
			}
			continue
		}
		if eip := net.ParseIP(e); eip != nil {
			if ip != nil && ip.Equal(eip) {
				return true
			}
			continue
		}
		if ip != nil {
			continue
		}
		if strings.HasPrefix(e, "*.") {
			e = e[1:]
		}
		if strings.HasPrefix(e, ".") {
			if strings.HasSuffix(h, e) || h == e[1:] {
				return true
			}
			continue
		}
		if h == e || strings.HasSuffix(h, "."+e) {
			return true
		}
	}
	return false
}

// portOf returns the port of u, using the scheme's default when none is given.
func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		return "443"
	}
	return "80"
}

// isLoopback reports whether host is localhost, *.localhost, or a loopback IP.
func isLoopback(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
