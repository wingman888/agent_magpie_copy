// Package netproxy decides which proxy magpie uses for its own outbound
// requests to model vendors. The order, highest first, is:
//
//  1. the "proxy" setting: "direct" means no proxy, anything else is the proxy;
//  2. the environment (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY and NO_PROXY, in
//     either case), with the same meaning as net/http.ProxyFromEnvironment;
//  3. the operating system's proxy (macOS and Windows only);
//  4. direct.
//
// Loopback hosts are always direct, whatever the level above says.
package netproxy

import (
	"errors"
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

// Sources reported by Describe and resolve.
const (
	SourceSettings    = "settings"
	SourceOff         = "off"
	SourceEnvironment = "environment"
	SourceSystem      = "system"
	SourceNone        = "none"
)

// systemTTL bounds how long an operating-system proxy reading is reused, so a
// change made in the system settings shows up within about 15 seconds.
const systemTTL = 10 * time.Second

// systemSettings is the part of the operating-system proxy configuration we use.
type systemSettings struct {
	Proxy      string   // proxy URL, or "" when no system proxy is enabled
	Exceptions []string // hosts that bypass the system proxy
}

// readSystem reads the operating-system proxy. Tests replace it.
var readSystem = readSystemProxy

var sysCache struct {
	sync.Mutex
	at    time.Time
	val   systemSettings
	valid bool
}

// sysRead serializes calls to readSystem so concurrent requests after the
// cache expires do not all run the system command. It is never held together
// with sysCache's lock, and readSystem is never called while sysCache is locked.
var sysRead sync.Mutex

// cachedSystem returns the cached reading if it is still fresh.
func cachedSystem() (systemSettings, bool) {
	sysCache.Lock()
	defer sysCache.Unlock()
	if sysCache.valid && time.Since(sysCache.at) < systemTTL {
		return sysCache.val, true
	}
	return systemSettings{}, false
}

// systemNow returns the operating-system proxy, reusing a reading for up to systemTTL.
func systemNow() systemSettings {
	if v, ok := cachedSystem(); ok {
		return v
	}
	sysRead.Lock()
	defer sysRead.Unlock()
	// Another goroutine may have refreshed the cache while we waited.
	if v, ok := cachedSystem(); ok {
		return v
	}
	v := readSystem()
	sysCache.Lock()
	sysCache.val = v
	sysCache.at = time.Now()
	sysCache.valid = true
	sysCache.Unlock()
	return v
}

// resetSystemCache forces the next systemNow call to read the system again.
func resetSystemCache() {
	sysCache.Lock()
	sysCache.valid = false
	sysCache.Unlock()
}

// Install makes http.DefaultTransport follow the same rules as For. Call it
// once at startup; repeated calls do nothing. It does nothing if
// DefaultTransport is not an *http.Transport.
func Install() {
	installMu.Lock()
	defer installMu.Unlock()
	if installed {
		return
	}
	installed = true
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok || t == nil {
		return
	}
	t.Proxy = proxyFunc
}

// installMu and installed make Install run once. They are plain values
// (not sync.Once) so tests can reset them.
var (
	installMu sync.Mutex
	installed bool
)

func proxyFunc(r *http.Request) (*url.URL, error) {
	if r == nil || r.URL == nil {
		return nil, nil
	}
	return For(r.URL)
}

// For returns the proxy to use when reaching u, or nil to connect directly.
// It reads the settings on every call, so a change applies to new requests.
// It returns an error when the stored proxy setting cannot be used.
func For(u *url.URL) (*url.URL, error) {
	p, _, err := resolve(u)
	return p, err
}

// Describe reports the proxy that outbound requests use in general, and where
// it comes from. For direct connections the proxy is "". It is not specific to
// one URL. A stored setting that cannot be parsed gives proxy "" with source
// "settings", so the UI never shows an unusable address; For reports the error
// for that case.
func Describe() (proxy, source string) {
	v := strings.TrimSpace(settings.Load().Proxy)
	switch v {
	case "":
	case "direct":
		return "", SourceOff
	default:
		if p, err := parseSettingProxy(v); err == nil {
			return p.String(), SourceSettings
		}
		return "", SourceSettings
	}
	if p := envDefault(); p != nil {
		return p.String(), SourceEnvironment
	}
	if sys := systemNow(); sys.Proxy != "" {
		return sys.Proxy, SourceSystem
	}
	return "", SourceNone
}

// resolve applies the priority order to u and also reports which level decided.
func resolve(u *url.URL) (*url.URL, string, error) {
	if u == nil {
		return nil, SourceNone, errors.New("netproxy: nil URL")
	}
	host := strings.ToLower(u.Hostname())
	if isLoopback(host) {
		return nil, SourceNone, nil
	}

	switch v := strings.TrimSpace(settings.Load().Proxy); v {
	case "":
	case "direct":
		return nil, SourceOff, nil
	default:
		p, err := parseSettingProxy(v)
		if err != nil {
			return nil, SourceSettings, err
		}
		return p, SourceSettings, nil
	}

	if p, applies := fromEnvironment(u); applies {
		return p, SourceEnvironment, nil
	}

	if sys := systemNow(); sys.Proxy != "" && !excepted(host, sys.Exceptions) {
		p, err := url.Parse(sys.Proxy)
		if err != nil {
			return nil, SourceNone, nil
		}
		return p, SourceSystem, nil
	}
	return nil, SourceNone, nil
}

// parseSettingProxy turns a non-empty, non-"direct" setting into a URL.
// A bare host:port is an HTTP proxy.
func parseSettingProxy(v string) (*url.URL, error) {
	if err := settings.ValidProxy(v); err != nil {
		return nil, fmt.Errorf("netproxy: invalid proxy setting: %w", err)
	}
	if !strings.Contains(v, "://") {
		v = "http://" + v
	}
	u, err := url.Parse(v)
	if err != nil {
		return nil, fmt.Errorf("netproxy: invalid proxy setting %q: %w", v, err)
	}
	return u, nil
}

// fromEnvironment follows net/http.ProxyFromEnvironment. For an https URL it
// uses HTTPS_PROXY, for an http URL HTTP_PROXY, and ALL_PROXY is the fallback.
// applies is false when no proxy variable covers u, so the next level decides.
// When NO_PROXY excludes u, applies is true and p is nil: the answer is direct.
func fromEnvironment(u *url.URL) (p *url.URL, applies bool) {
	switch u.Scheme {
	case "https":
		p = parseEnvProxy(getenv("HTTPS_PROXY", "https_proxy"))
	case "http":
		p = parseEnvProxy(getenv("HTTP_PROXY", "http_proxy"))
	}
	if p == nil {
		p = parseEnvProxy(getenv("ALL_PROXY", "all_proxy"))
	}
	if p == nil {
		return nil, false
	}
	if excludedByNoProxy(u) {
		return nil, true
	}
	return p, true
}

// envDefault is the environment proxy for general use, for Describe.
func envDefault() *url.URL {
	for _, names := range [][]string{
		{"HTTPS_PROXY", "https_proxy"},
		{"HTTP_PROXY", "http_proxy"},
		{"ALL_PROXY", "all_proxy"},
	} {
		if p := parseEnvProxy(getenv(names...)); p != nil {
			return p
		}
	}
	return nil
}

// getenv returns the first non-empty value among names.
func getenv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// parseEnvProxy parses a proxy variable. A missing scheme means http. Invalid
// values are ignored, as net/http does.
func parseEnvProxy(v string) *url.URL {
	if v == "" {
		return nil
	}
	if !strings.Contains(v, "://") {
		v = "http://" + v
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return nil
	}
	return u
}

// excludedByNoProxy reports whether NO_PROXY lists u's host or host:port.
// It follows net/http: "*", exact hosts and IPs, CIDR ranges, and domain
// suffixes written as "example.com", ".example.com" or "*.example.com".
func excludedByNoProxy(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	for _, e := range strings.Split(getenv("NO_PROXY", "no_proxy"), ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == "*" {
			return true
		}
		if _, n, err := net.ParseCIDR(e); err == nil {
			if ip := net.ParseIP(host); ip != nil && n.Contains(ip) {
				return true
			}
			continue
		}
		if h, p, err := net.SplitHostPort(e); err == nil {
			if p != port {
				continue
			}
			e = h
		}
		if ip := net.ParseIP(e); ip != nil {
			if hip := net.ParseIP(host); hip != nil && hip.Equal(ip) {
				return true
			}
			continue
		}
		e = strings.TrimPrefix(strings.TrimPrefix(e, "*"), ".")
		if e == "" {
			continue
		}
		if host == e || strings.HasSuffix(host, "."+e) {
			return true
		}
	}
	return false
}

// excepted reports whether host matches an operating-system exception entry:
//   - an exact host name, case-insensitive;
//   - "*.example.com" or ".example.com": a subdomain of example.com, or example.com itself;
//   - "<local>": any host name without a dot.
//
// Empty entries are ignored.
func excepted(host string, list []string) bool {
	host = strings.ToLower(host)
	for _, p := range list {
		p = strings.ToLower(strings.TrimSpace(p))
		switch {
		case p == "":
		case p == "<local>":
			if !strings.Contains(host, ".") {
				return true
			}
		case strings.HasPrefix(p, "*."):
			d := p[1:] // ".example.com"
			if strings.HasSuffix(host, d) || host == d[1:] {
				return true
			}
		case strings.HasPrefix(p, "."):
			if strings.HasSuffix(host, p) || host == p[1:] {
				return true
			}
		default:
			if host == p {
				return true
			}
		}
	}
	return false
}

// isLoopback reports whether host is localhost, *.localhost, or a loopback IP.
// The host must not include brackets.
func isLoopback(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
