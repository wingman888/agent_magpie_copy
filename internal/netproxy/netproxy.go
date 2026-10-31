// Package netproxy decides which network proxy magpie's own outbound
// requests use. Priority, high to low: the "proxy" setting ("direct" means
// none), the environment (HTTPS_PROXY and friends), the operating system's
// proxy settings, then a direct connection. Loopback hosts are never proxied.
package netproxy

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Example is the form error messages point the user to.
const Example = "http://127.0.0.1:7890"

// Direct is the setting value that turns proxying off.
const Direct = "direct"

// Sources reported by Describe.
const (
	SourceSettings    = "settings"
	SourceOff         = "off"
	SourceEnvironment = "environment"
	SourceSystem      = "system"
	SourceNone        = "none"
)

// SettingsProxy returns the raw "proxy" value of the settings file. It is a
// variable so tests can replace it. The default reads the file on every call,
// so a saved change is seen by the next request. netproxy cannot import
// internal/settings (which uses Normalize), hence the direct read.
var SettingsProxy = readSettingsProxy

func readSettingsProxy() string {
	p := os.Getenv("XDG_CONFIG_HOME")
	if p != "" {
		p = filepath.Join(p, "magpie", "settings.json")
	} else {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, ".config", "magpie", "settings.json")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var s struct {
		Proxy string `json:"proxy"`
	}
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s.Proxy)
}

// Normalize validates a proxy address and returns it as scheme://host[:port].
// A bare host:port is an HTTP proxy.
func Normalize(s string) (string, error) {
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("invalid proxy %q: %s; use a form like %s (also https://, socks5://, socks5h://), or %q for no proxy", s, why, Example, Direct)
	}
	t := strings.TrimSpace(s)
	if t == "" {
		return bad("empty")
	}
	if !strings.Contains(t, "://") {
		t = "http://" + t
	}
	u, err := url.Parse(t)
	if err != nil {
		return bad("cannot parse")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return bad("unsupported scheme " + u.Scheme)
	}
	if u.Hostname() == "" {
		return bad("missing host")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return bad("unexpected path")
	}
	if p := u.Port(); p != "" {
		for _, c := range p {
			if c < '0' || c > '9' {
				return bad("bad port")
			}
		}
	}
	u.Path, u.RawPath = "", ""
	return u.String(), nil
}

// isLoopback reports whether host (no port) never goes through a proxy.
func isLoopback(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if i := strings.IndexByte(h, '%'); i >= 0 {
		h = h[:i]
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// For returns the proxy to use for u; nil means connect directly.
func For(u *url.URL) (*url.URL, error) {
	if u == nil || isLoopback(u.Hostname()) {
		return nil, nil
	}
	if raw := SettingsProxy(); raw != "" {
		if strings.EqualFold(raw, Direct) {
			return nil, nil
		}
		n, err := Normalize(raw)
		if err != nil {
			return nil, err
		}
		return url.Parse(n)
	}
	if envConfigured() {
		return forEnv(u)
	}
	if sp, exc := systemProxy(); sp != "" {
		if matchExceptions(u.Hostname(), exc) {
			return nil, nil
		}
		return url.Parse(sp)
	}
	return nil, nil
}

// Describe reports the proxy outbound requests currently use and where that
// choice comes from. Per-host exceptions are not considered.
func Describe() (proxy, source string) {
	if raw := SettingsProxy(); raw != "" {
		if strings.EqualFold(raw, Direct) {
			return "", SourceOff
		}
		if n, err := Normalize(raw); err == nil {
			return n, SourceSettings
		}
		return raw, SourceSettings
	}
	if p := envProxyValue(); p != "" {
		if n, err := Normalize(p); err == nil {
			p = n
		}
		return p, SourceEnvironment
	}
	if sp, _ := systemProxy(); sp != "" {
		return sp, SourceSystem
	}
	return "", SourceNone
}

// Install makes http.DefaultTransport follow the same rules.
func Install() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = Func
	}
}

// Func is For in the shape http.Transport.Proxy wants.
func Func(r *http.Request) (*url.URL, error) { return For(r.URL) }

func getenvAny(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func envConfigured() bool { return envProxyValue() != "" }

// envProxyValue is the proxy Describe shows for the environment.
func envProxyValue() string {
	return getenvAny("HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy")
}

func forEnv(u *url.URL) (*url.URL, error) {
	var v string
	switch u.Scheme {
	case "https":
		v = getenvAny("HTTPS_PROXY", "https_proxy")
	case "http":
		v = getenvAny("HTTP_PROXY", "http_proxy")
	default:
		return nil, nil
	}
	if v == "" {
		v = getenvAny("ALL_PROXY", "all_proxy")
	}
	if v == "" {
		return nil, nil
	}
	if noProxyEnv(u, getenvAny("NO_PROXY", "no_proxy")) {
		return nil, nil
	}
	if n, err := Normalize(v); err == nil {
		return url.Parse(n)
	}
	// Like the standard library, accept a few oddities rather than fail.
	pu, err := url.Parse(v)
	if err != nil || pu.Scheme == "" || pu.Host == "" {
		if pu2, err2 := url.Parse("http://" + v); err2 == nil && pu2.Host != "" {
			return pu2, nil
		}
		return nil, fmt.Errorf("invalid proxy address %q in environment: %v", v, err)
	}
	return pu, nil
}

// noProxyEnv follows the semantics of Go's httpproxy NO_PROXY handling.
func noProxyEnv(u *url.URL, list string) bool {
	if list == "" {
		return false
	}
	if list == "*" {
		return true
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
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
		eh, ep := e, ""
		if h, p, err := net.SplitHostPort(e); err == nil {
			eh, ep = h, p
		}
		if ep != "" && ep != port {
			continue
		}
		if eip := net.ParseIP(eh); eip != nil {
			if ip != nil && eip.Equal(ip) {
				return true
			}
			continue
		}
		if strings.HasPrefix(eh, "*.") {
			eh = eh[1:]
		}
		if strings.HasPrefix(eh, ".") {
			if strings.HasSuffix(host, eh) || host == eh[1:] {
				return true
			}
			continue
		}
		if host == eh {
			return true
		}
	}
	return false
}
