// Package netproxy decides which proxy magpie uses for its own outbound
// requests to model vendors. The order, highest first:
//
//  1. loopback hosts are always direct;
//  2. the "proxy" setting: "direct" means no proxy, anything else is the proxy;
//  3. the proxy environment variables (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY,
//     NO_PROXY and their lower-case forms), read the way the Go standard
//     library reads them;
//  4. the operating system proxy (macOS and Windows only);
//  5. direct.
//
// For, Describe and the default HTTP transport installed by Install all use
// the same decision function, so they always agree.
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

// Source names, as reported by Describe.
const (
	sourceSettings    = "settings"
	sourceOff         = "off"
	sourceEnvironment = "environment"
	sourceSystem      = "system"
	sourceNone        = "none"
)

// describeURL is the stand-in external host Describe decides for.
const describeURL = "https://api.example.com"

// systemCacheTTL bounds how long a system proxy reading is reused. Changes to
// the system proxy therefore show up within this window without a restart.
const systemCacheTTL = 10 * time.Second

// loadSettings and readSystemProxy are variables so tests can replace them.
var (
	loadSettings    = settings.Load
	readSystemProxy = readPlatformProxy
)

var (
	sysMu     sync.Mutex
	sysCache  systemProxy
	sysCached time.Time // zero when nothing is cached
)

// Install makes http.DefaultTransport follow the same proxy rules as For.
// It is idempotent: every call sets Proxy to the same function, so calling it
// more than once is harmless.
func Install() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = proxyFunc
	}
}

func proxyFunc(r *http.Request) (*url.URL, error) {
	return For(r.URL)
}

// For returns the proxy to use when requesting u, or nil for a direct
// connection. An unusable proxy setting or environment value is an error;
// it never silently turns into a direct connection.
func For(u *url.URL) (*url.URL, error) {
	pu, _, err := decide(u)
	return pu, err
}

// Describe reports the proxy that outbound requests to an external host use
// now, and where that proxy comes from. proxy is "" when there is none, and
// source is one of settings, off, environment, system or none.
func Describe() (proxy, source string) {
	u, err := url.Parse(describeURL)
	if err != nil {
		return "", sourceNone
	}
	pu, src, err := decide(u)
	if err != nil {
		return "", sourceNone
	}
	if pu == nil {
		return "", src
	}
	return pu.String(), src
}

// decide implements the precedence order. It returns the proxy (nil for
// direct), the source, and an error for an unusable configured proxy.
func decide(u *url.URL) (*url.URL, string, error) {
	// 1. Loopback is never proxied, whatever is configured.
	if u == nil || isLoopback(u.Hostname()) {
		return nil, sourceNone, nil
	}

	// 2. The proxy setting. It is read on every call so that a change
	// applies to the next request without a restart.
	if s := strings.TrimSpace(loadSettings().Proxy); s != "" {
		if strings.EqualFold(s, "direct") {
			return nil, sourceOff, nil
		}
		if err := settings.ValidateProxy(s); err != nil {
			return nil, "", fmt.Errorf("proxy setting: %w", err)
		}
		pu, err := parseProxyURL(s)
		if err != nil {
			return nil, "", fmt.Errorf("proxy setting %q: %w", s, err)
		}
		return pu, sourceSettings, nil
	}

	// 3. Environment variables. When one applies, NO_PROXY decides between
	// the proxy and direct, and the system proxy is not consulted.
	if raw := envProxyValue(u.Scheme); raw != "" {
		// NO_PROXY is checked before the value is parsed: an excluded host
		// goes direct and never needs a usable environment proxy.
		if envNoProxy(u) {
			return nil, sourceNone, nil
		}
		pu, err := parseProxyURL(raw)
		if err != nil {
			return nil, "", fmt.Errorf("proxy environment variable: %w", err)
		}
		return pu, sourceEnvironment, nil
	}

	// 4. The operating system proxy. Read failures count as "no proxy".
	sp := currentSystemProxy()
	if sp.URL != "" && !matchBypass(u.Hostname(), sp.Bypass) {
		if pu, err := parseProxyURL(sp.URL); err == nil {
			return pu, sourceSystem, nil
		}
	}

	// 5. Direct.
	return nil, sourceNone, nil
}

// currentSystemProxy returns the system proxy, reusing a reading that is
// younger than systemCacheTTL. The read itself runs without holding sysMu,
// because it may execute an external command. Concurrent callers can refresh
// at the same time; that is harmless, since each refresh stores a complete,
// current reading.
func currentSystemProxy() systemProxy {
	sysMu.Lock()
	if !sysCached.IsZero() && time.Since(sysCached) < systemCacheTTL {
		sp := sysCache
		sysMu.Unlock()
		return sp
	}
	sysMu.Unlock()

	sp := readSystemProxy()

	sysMu.Lock()
	sysCache = sp
	sysCached = time.Now()
	sysMu.Unlock()
	return sp
}

// resetSystemCache forgets the cached system proxy reading.
func resetSystemCache() {
	sysMu.Lock()
	defer sysMu.Unlock()
	sysCached = time.Time{}
}

// isLoopback reports whether host is localhost, *.localhost, 127.0.0.0/8 or
// ::1. It accepts host:port and bracketed IPv6 forms as well.
func isLoopback(host string) bool {
	h := strings.TrimSpace(host)
	if strings.HasPrefix(h, "[") {
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		} else {
			h = strings.Trim(h, "[]")
		}
	} else if strings.Count(h, ":") == 1 {
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// envProxyValue returns the environment proxy for a request scheme, following
// the Go standard library: the scheme-specific variable first, then ALL_PROXY.
// It returns "" when none is set.
func envProxyValue(scheme string) string {
	var v string
	switch strings.ToLower(scheme) {
	case "https":
		v = getEnvAny("HTTPS_PROXY", "https_proxy")
	case "http":
		v = getEnvAny("HTTP_PROXY", "http_proxy")
	}
	if v == "" {
		v = getEnvAny("ALL_PROXY", "all_proxy")
	}
	return v
}

// envNoProxy reports whether NO_PROXY excludes u. It supports "*", exact
// hosts, domain suffixes (".example.com", "*.example.com", "example.com"),
// IP addresses and CIDR ranges, and an optional port on each entry.
func envNoProxy(u *url.URL) bool {
	list := getEnvAny("NO_PROXY", "no_proxy")
	if list == "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	port := u.Port()
	if port == "" {
		port = defaultPort(u.Scheme)
	}
	ip := net.ParseIP(host)
	sep := func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }
	for _, item := range strings.FieldsFunc(list, sep) {
		item = strings.ToLower(item)
		if item == "*" {
			return true
		}
		entryHost, entryPort := item, ""
		if h, p, err := net.SplitHostPort(item); err == nil {
			entryHost, entryPort = h, p
		}
		if entryPort != "" && entryPort != port {
			continue
		}
		entryHost = strings.Trim(entryHost, "[]")
		if _, cidr, err := net.ParseCIDR(entryHost); err == nil {
			if ip != nil && cidr.Contains(ip) {
				return true
			}
			continue
		}
		if eip := net.ParseIP(entryHost); eip != nil {
			if ip != nil && eip.Equal(ip) {
				return true
			}
			continue
		}
		entryHost = strings.TrimPrefix(entryHost, "*")
		if entryHost == "" || entryHost == "." {
			continue
		}
		if strings.HasPrefix(entryHost, ".") {
			if host == entryHost[1:] || strings.HasSuffix(host, entryHost) {
				return true
			}
		} else if host == entryHost || strings.HasSuffix(host, "."+entryHost) {
			return true
		}
	}
	return false
}

// getEnvAny returns the first non-empty value of the named variables.
func getEnvAny(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// defaultPort is the port implied by a request scheme.
func defaultPort(scheme string) string {
	switch strings.ToLower(scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

// parseProxyURL turns a proxy setting or environment value into a URL. A value
// without a scheme is taken as an HTTP proxy.
func parseProxyURL(v string) (*url.URL, error) {
	raw := strings.TrimSpace(v)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%q is not a valid proxy address", v)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("%q has an unsupported scheme (use http, https, socks5 or socks5h)", v)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("%q has no host (use a form such as http://127.0.0.1:7890)", v)
	}
	return u, nil
}
