// Package netproxy decides which proxy magpie's outgoing requests use.
//
// Order: the proxy in settings ("direct" = none), then the environment
// (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY, NO_PROXY), then the OS proxy settings
// (macOS and Windows only), then a direct connection. Loopback hosts are never
// proxied.
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

// Sources reported by Describe.
const (
	SourceSettings    = "settings"
	SourceOff         = "off"
	SourceEnvironment = "environment"
	SourceSystem      = "system"
	SourceNone        = "none"
)

// Hooks for tests.
var (
	// settingValue returns the raw proxy setting.
	settingValue = func() string { return cachedSetting() }
	// getenv reads the environment.
	getenv = os.Getenv
)

// Install makes http.DefaultTransport follow the same rules.
func Install() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = ForRequest
	}
}

// ForRequest is For as an http.Transport.Proxy function.
func ForRequest(r *http.Request) (*url.URL, error) { return For(r.URL) }

// For returns the proxy to use for u; nil means connect directly.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, nil
	}
	if isLoopback(u.Hostname()) {
		return nil, nil
	}
	if raw := strings.TrimSpace(settingValue()); raw != "" {
		p, direct, err := settings.ParseProxy(raw)
		if err != nil {
			return nil, fmt.Errorf("proxy setting: %w", err)
		}
		if direct {
			return nil, nil
		}
		return p, nil
	}
	if envConfigured() {
		return envFor(u)
	}
	if sp := systemProxy(); sp != nil {
		if sp.bypass(u.Hostname()) {
			return nil, nil
		}
		return sp.URL, nil
	}
	return nil, nil
}

// Describe reports the proxy outgoing requests currently use and where it
// comes from. The address is empty for "off" and "none".
func Describe() (proxy, source string) {
	if raw := strings.TrimSpace(settingValue()); raw != "" {
		p, direct, err := settings.ParseProxy(raw)
		if err == nil && direct {
			return "", SourceOff
		}
		if err == nil {
			return p.String(), SourceSettings
		}
		return raw, SourceSettings
	}
	if envConfigured() {
		for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY"} {
			if v := envGet(k); v != "" {
				if p, err := parseEnvProxy(v); err == nil {
					return p.String(), SourceEnvironment
				}
				return v, SourceEnvironment
			}
		}
	}
	if sp := systemProxy(); sp != nil {
		return sp.URL.String(), SourceSystem
	}
	return "", SourceNone
}

// ---- settings cache ----

var (
	setMu   sync.Mutex
	setAt   time.Time
	setVal  string
	setTTL  = time.Second
	nowFunc = time.Now
)

func cachedSetting() string {
	setMu.Lock()
	defer setMu.Unlock()
	if !setAt.IsZero() && nowFunc().Sub(setAt) < setTTL {
		return setVal
	}
	setVal = settings.Load().Proxy
	setAt = nowFunc()
	return setVal
}

// InvalidateSettings drops the cached setting so the next request re-reads it.
func InvalidateSettings() {
	setMu.Lock()
	setAt = time.Time{}
	setMu.Unlock()
}

// ---- loopback ----

func isLoopback(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ---- environment ----

func envGet(k string) string {
	if v := strings.TrimSpace(getenv(k)); v != "" {
		return v
	}
	return strings.TrimSpace(getenv(strings.ToLower(k)))
}

func envConfigured() bool {
	return envGet("HTTPS_PROXY") != "" || envGet("HTTP_PROXY") != "" || envGet("ALL_PROXY") != ""
}

func parseEnvProxy(v string) (*url.URL, error) {
	p, direct, err := settings.ParseProxy(v)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy in environment: %w", err)
	}
	if direct || p == nil {
		return nil, fmt.Errorf("invalid proxy in environment: %q", v)
	}
	return p, nil
}

func envFor(u *url.URL) (*url.URL, error) {
	var v string
	switch strings.ToLower(u.Scheme) {
	case "https":
		v = envGet("HTTPS_PROXY")
	case "http":
		v = envGet("HTTP_PROXY")
	}
	if v == "" {
		v = envGet("ALL_PROXY")
	}
	if v == "" {
		return nil, nil
	}
	if noProxy(envGet("NO_PROXY"), u) {
		return nil, nil
	}
	return parseEnvProxy(v)
}

// noProxy follows the Go standard library's NO_PROXY semantics.
func noProxy(list string, u *url.URL) bool {
	if list == "" {
		return false
	}
	if list == "*" {
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
	for _, e := range strings.Split(list, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if _, cidr, err := net.ParseCIDR(e); err == nil {
			if ip != nil && cidr.Contains(ip) {
				return true
			}
			continue
		}
		ePort := ""
		if h, p, err := net.SplitHostPort(e); err == nil {
			e, ePort = h, p
		}
		if ePort != "" && ePort != port {
			continue
		}
		if eip := net.ParseIP(strings.Trim(e, "[]")); eip != nil {
			if ip != nil && eip.Equal(ip) {
				return true
			}
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
