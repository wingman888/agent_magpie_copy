// Package netproxy decides which proxy magpie's own outbound requests use.
//
// The order is: the proxy in settings (proxy = "direct" means none), then the
// environment (HTTPS_PROXY / HTTP_PROXY / ALL_PROXY and NO_PROXY), then the
// system proxy (macOS network settings, Windows Internet options; Linux has
// none beyond the environment). Loopback addresses never use a proxy.
package netproxy

import (
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

// For returns the proxy a request to u should go through, or nil to connect
// directly.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, nil
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || loopback(host) {
		return nil, nil
	}
	s := settings.Load()
	if s.Proxy == "direct" {
		return nil, nil
	}
	if s.Proxy != "" {
		return settings.ProxyURL(s.Proxy)
	}

	if raw := fromEnv(u.Scheme); raw != "" {
		if excludedByEnv(host) {
			return nil, nil
		}
		return settings.ProxyURL(raw)
	}

	sys := systemProxy()
	if sys.none() || excludedBySystem(host, sys) {
		return nil, nil
	}
	return sys.pick()
}

// Proxy is For in the shape http.Transport.Proxy wants.
func Proxy(r *http.Request) (*url.URL, error) {
	if r == nil {
		return nil, nil
	}
	return For(r.URL)
}

// Transport is a transport that follows For. It starts from the default
// transport's settings, so timeouts and pooling match what Go does anyway.
func Transport() *http.Transport {
	var t *http.Transport
	if d, ok := http.DefaultTransport.(*http.Transport); ok {
		t = d.Clone()
	} else {
		t = &http.Transport{ForceAttemptHTTP2: true}
	}
	t.Proxy = Proxy
	return t
}

var (
	sharedOnce sync.Once
	shared     *http.Client
)

// Client returns a shared client for magpie's outbound requests. It has no
// timeout, like http.DefaultClient, and uses the transport from Transport.
func Client() *http.Client {
	sharedOnce.Do(func() { shared = &http.Client{Transport: Transport()} })
	return shared
}

// fromEnv returns the environment proxy for a scheme, "" when none is set.
func fromEnv(scheme string) string {
	names := []string{"ALL_PROXY", "all_proxy"}
	if scheme == "https" {
		names = append([]string{"HTTPS_PROXY", "https_proxy"}, names...)
	} else if scheme == "http" {
		names = append([]string{"HTTP_PROXY", "http_proxy"}, names...)
	}
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func excludedByEnv(host string) bool {
	for _, e := range strings.Split(firstNonEmpty(os.Getenv("NO_PROXY"), os.Getenv("no_proxy")), ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == "*" || matchHost(host, e) {
			return true
		}
	}
	return false
}

// matchHost matches a NO_PROXY entry: an exact host, a domain suffix (with or
// without a leading dot or "*."), an IP, or a CIDR block.
func matchHost(host, e string) bool {
	if h, _, err := net.SplitHostPort(e); err == nil {
		e = h
	}
	if _, n, err := net.ParseCIDR(e); err == nil {
		ip := net.ParseIP(host)
		return ip != nil && n.Contains(ip)
	}
	e = strings.TrimPrefix(strings.TrimPrefix(e, "*"), ".")
	if e == "" {
		return false
	}
	return host == e || strings.HasSuffix(host, "."+e)
}

// excludedBySystem applies the system exception list: glob patterns such as
// *.corp.example or 192.168.*, "<local>" (a host name without a dot), and
// macOS's ExcludeSimpleHostnames flag, which means the same.
func excludedBySystem(host string, sys system) bool {
	if sys.excludeLocal && !strings.Contains(host, ".") && net.ParseIP(host) == nil {
		return true
	}
	for _, e := range sys.exceptions {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
			continue
		case e == "<local>":
			if !strings.Contains(host, ".") && net.ParseIP(host) == nil {
				return true
			}
		case strings.Contains(e, "/"):
			if _, n, err := net.ParseCIDR(padCIDR(e)); err == nil {
				if ip := net.ParseIP(host); ip != nil && n.Contains(ip) {
					return true
				}
			}
		default:
			if ok, _ := path.Match(e, host); ok {
				return true
			}
		}
	}
	return false
}

// padCIDR turns macOS's short form "169.254/16" into "169.254.0.0/16".
func padCIDR(e string) string {
	addr, bits, _ := strings.Cut(e, "/")
	for strings.Count(addr, ".") < 3 {
		addr += ".0"
	}
	return addr + "/" + bits
}

func loopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// system is the proxy the operating system reports.
type system struct {
	https, http, socks string // host:port, "" when not set
	exceptions         []string
	excludeLocal       bool
}

func (s system) none() bool { return s.https == "" && s.http == "" && s.socks == "" }

// pick chooses HTTPS first, then HTTP, then SOCKS. HTTPS and HTTP proxies are
// spoken to with CONNECT, so they are http:// URLs.
func (s system) pick() (*url.URL, error) {
	switch {
	case s.https != "":
		return url.Parse("http://" + s.https)
	case s.http != "":
		return url.Parse("http://" + s.http)
	case s.socks != "":
		return url.Parse("socks5://" + s.socks)
	}
	return nil, nil
}

var (
	sysMu      sync.Mutex
	sysCached  system
	sysAt      time.Time
	sysTTL     = 30 * time.Second
	readSystem = readPlatform // replaced in tests
)

// systemProxy reads the system proxy, caching it briefly so that a busy
// gateway does not run a helper process for every request.
func systemProxy() system {
	sysMu.Lock()
	defer sysMu.Unlock()
	if !sysAt.IsZero() && time.Since(sysAt) < sysTTL {
		return sysCached
	}
	sysCached = readSystem()
	sysAt = time.Now()
	return sysCached
}
