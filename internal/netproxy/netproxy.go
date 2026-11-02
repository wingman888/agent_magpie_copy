// Package netproxy decides which proxy magpie's own requests to vendors go
// through. An app started from the Dock or the Start menu has no
// HTTPS_PROXY in its environment, so without this every call to a vendor
// that is only reachable through a proxy hangs until the agent gives up.
//
// In order: the proxy set in magpie's settings (or "direct" for none), the
// usual *_PROXY variables, then the system's proxy (macOS network
// settings, Windows Internet Options). Loopback is never proxied.
package netproxy

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// Install routes http.DefaultTransport (and so http.DefaultClient) through
// Func.
func Install() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = Func
	}
}

// Func is an http.Transport Proxy function.
func Func(req *http.Request) (*url.URL, error) {
	if loopback(req.URL.Hostname()) {
		return nil, nil
	}
	return For(req.URL)
}

// For is the proxy a request to u goes through; nil means direct.
func For(u *url.URL) (*url.URL, error) {
	switch s := strings.TrimSpace(settings.Load().Proxy); s {
	case "direct":
		return nil, nil
	case "":
	default:
		return Parse(s)
	}
	if p, err := fromEnv(u); p != nil || err != nil {
		return p, err
	}
	sys := System()
	if sys.URL == "" || bypassed(u.Hostname(), sys.Bypass) {
		return nil, nil
	}
	return Parse(sys.URL)
}

// Parse reads a proxy address; a bare host:port is an HTTP proxy.
func Parse(s string) (*url.URL, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return url.Parse(s)
}

// Describe says, for the settings page, what a request to u would use.
func Describe() (proxy, source string) {
	switch s := strings.TrimSpace(settings.Load().Proxy); s {
	case "direct":
		return "", "off"
	case "":
	default:
		return s, "settings"
	}
	u, _ := url.Parse("https://chatgpt.com")
	if p, _ := fromEnv(u); p != nil {
		return p.String(), "environment"
	}
	if sys := System(); sys.URL != "" {
		return sys.URL, "system"
	}
	return "", "none"
}

func fromEnv(u *url.URL) (*url.URL, error) { return http.ProxyFromEnvironment(&http.Request{URL: u}) }

// Proxy is the system's proxy: one address, and the hosts that skip it.
type Proxy struct {
	URL    string
	Bypass []string
}

var sysCache struct {
	sync.Mutex
	at time.Time
	p  Proxy
}

// System reads the system proxy, at most every 15 seconds.
func System() Proxy {
	sysCache.Lock()
	defer sysCache.Unlock()
	if time.Since(sysCache.at) > 15*time.Second {
		sysCache.p, sysCache.at = system(), time.Now()
	}
	return sysCache.p
}

func loopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// bypassed matches the system's exception list: exact hosts, "*.example.com"
// or ".example.com" suffixes, and "<local>" for dotless names.
func bypassed(host string, list []string) bool {
	host = strings.ToLower(host)
	for _, b := range list {
		b = strings.ToLower(strings.TrimSpace(b))
		switch {
		case b == "":
		case b == "<local>":
			if !strings.Contains(host, ".") {
				return true
			}
		case strings.HasPrefix(b, "*."), strings.HasPrefix(b, "."):
			suf := strings.TrimPrefix(b, "*")
			if strings.HasSuffix(host, suf) || host == suf[1:] {
				return true
			}
		case host == b:
			return true
		}
	}
	return false
}
