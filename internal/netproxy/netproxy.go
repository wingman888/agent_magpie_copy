// Package netproxy picks the proxy for magpie's own outgoing requests: the
// one set in settings, else the environment's, else the operating system's.
// Loopback is never proxied.
package netproxy

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// Proxy is a system proxy: one address plus hosts that skip it.
type Proxy struct {
	URL    string
	Bypass []string
}

// Func is an http.Transport.Proxy.
func Func(req *http.Request) (*url.URL, error) {
	if loopback(req.URL.Hostname()) {
		return nil, nil
	}
	return For(req.URL)
}

// For is the proxy to reach u through; nil means direct.
func For(u *url.URL) (*url.URL, error) {
	switch p := strings.TrimSpace(settings.Load().Proxy); p {
	case "":
	case "direct":
		return nil, nil
	default:
		return Parse(p)
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

// Parse reads a proxy address; a bare host:port is an http proxy.
func Parse(s string) (*url.URL, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return url.Parse(s)
}

// Install points the default transport (and so the default client) at Func.
func Install() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = Func
	}
}

// Describe says which proxy is in force and where it came from: off,
// settings, environment, system or none.
func Describe() (proxy, source string) {
	switch p := strings.TrimSpace(settings.Load().Proxy); p {
	case "":
	case "direct":
		return "", "off"
	default:
		return p, "settings"
	}
	if p, err := fromEnv(&url.URL{Scheme: "https", Host: "chatgpt.com"}); err == nil && p != nil {
		return p.String(), "environment"
	}
	if u := System().URL; u != "" {
		return u, "system"
	}
	return "", "none"
}

func loopback(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

var (
	mu      sync.Mutex
	cached  Proxy
	readAt  time.Time
	readSys = platformRead // replaced by tests
	now     = time.Now
)

// System is the operating system's proxy, re-read at most every 15 seconds.
func System() Proxy {
	mu.Lock()
	defer mu.Unlock()
	if !readAt.IsZero() && now().Sub(readAt) < 15*time.Second {
		return cached
	}
	cached, readAt = readSys(), now()
	return cached
}

// bypassed reports whether host is on an exception list.
func bypassed(host string, list []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
		case e == "<local>":
			if !strings.Contains(host, ".") {
				return true
			}
		case strings.HasPrefix(e, "*."), strings.HasPrefix(e, "."):
			d := strings.TrimPrefix(strings.TrimPrefix(e, "*"), ".")
			if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
				return true
			}
		case e == host:
			return true
		}
	}
	return false
}

// fromEnv follows http.ProxyFromEnvironment, but reads the variables on every
// call instead of once.
func fromEnv(u *url.URL) (*url.URL, error) {
	var v string
	if u.Scheme == "https" {
		v = env("HTTPS_PROXY", "https_proxy")
	} else {
		v = env("HTTP_PROXY", "http_proxy")
	}
	if v == "" || noProxy(u, env("NO_PROXY", "no_proxy")) {
		return nil, nil
	}
	return Parse(v)
}

func env(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func noProxy(u *url.URL, list string) bool {
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	host = strings.ToLower(host)
	ip := net.ParseIP(host)
	for _, e := range strings.Split(list, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == "*" {
			return true
		}
		if _, n, err := net.ParseCIDR(e); err == nil {
			if ip != nil && n.Contains(ip) {
				return true
			}
			continue
		}
		var ep string
		if h, p, err := net.SplitHostPort(e); err == nil {
			e, ep = strings.Trim(h, "[]"), p
		}
		if ep != "" && ep != port {
			continue
		}
		if ip != nil {
			if eip := net.ParseIP(e); eip != nil && eip.Equal(ip) {
				return true
			}
		}
		e = strings.TrimPrefix(e, "*")
		if strings.HasPrefix(e, ".") {
			if host == e[1:] || strings.HasSuffix(host, e) {
				return true
			}
		} else if host == e || strings.HasSuffix(host, "."+e) {
			return true
		}
	}
	return false
}
