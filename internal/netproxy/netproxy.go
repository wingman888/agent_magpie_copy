// Package netproxy decides which proxy magpie's own outgoing requests use.
//
// The order is: the proxy in settings (proxy = "direct" means none), then the
// environment (HTTPS_PROXY and friends, NO_PROXY), then the system proxy
// (macOS network settings, Windows Internet options; Linux has none). Loopback
// addresses never go through a proxy.
package netproxy

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/settings"
)

// sysProxy is the system proxy as the OS reports it. Empty fields are off.
type sysProxy struct {
	HTTPS, HTTP, SOCKS string   // host:port
	Exceptions         []string // bypass list; "<local>" means dotless names
}

// systemProxyFunc reads the system proxy. Tests replace it.
var systemProxyFunc = readSystemProxy

var (
	clientOnce sync.Once
	client     *http.Client
)

// Client is the shared client for magpie's outgoing requests that have no
// settings of their own. Its transport is the default one with Proxy set to
// ProxyFunc.
func Client() *http.Client {
	clientOnce.Do(func() {
		client = &http.Client{Transport: Transport()}
	})
	return client
}

// Transport returns a new transport like http.DefaultTransport that follows
// the proxy rules of this package.
func Transport() *http.Transport {
	var t *http.Transport
	if d, ok := http.DefaultTransport.(*http.Transport); ok {
		t = d.Clone()
	} else {
		t = &http.Transport{}
	}
	t.Proxy = ProxyFunc
	return t
}

// ProxyFunc is For in the shape http.Transport.Proxy expects.
func ProxyFunc(r *http.Request) (*url.URL, error) {
	return For(r.URL)
}

// For returns the proxy a request to u should use, or nil for a direct
// connection.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, nil
	}
	host := u.Hostname()
	if isLoopback(host) {
		return nil, nil
	}
	s := settings.Load()
	if p := strings.TrimSpace(s.Proxy); p != "" {
		if p == "direct" {
			return nil, nil
		}
		if err := settings.ValidProxy(p); err != nil {
			return nil, err
		}
		return parseProxy(p)
	}

	noProxy := envAny("NO_PROXY", "no_proxy")
	if noProxy != "" && matchList(host, strings.Split(noProxy, ",")) {
		return nil, nil
	}
	var envKeys []string
	switch strings.ToLower(u.Scheme) {
	case "https":
		envKeys = []string{"HTTPS_PROXY", "https_proxy"}
	case "http":
		envKeys = []string{"HTTP_PROXY", "http_proxy"}
	}
	envKeys = append(envKeys, "ALL_PROXY", "all_proxy")
	if v := envAny(envKeys...); v != "" {
		return parseProxy(v)
	}

	sp := systemProxyFunc()
	if matchList(host, sp.Exceptions) {
		return nil, nil
	}
	switch {
	case sp.HTTPS != "":
		return parseProxy("http://" + sp.HTTPS)
	case sp.HTTP != "":
		return parseProxy("http://" + sp.HTTP)
	case sp.SOCKS != "":
		return parseProxy("socks5://" + sp.SOCKS)
	}
	return nil, nil
}

func envAny(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// parseProxy accepts a URL or a bare host:port, which is an http proxy.
func parseProxy(p string) (*url.URL, error) {
	if !strings.Contains(p, "://") {
		p = "http://" + p
	}
	u, err := url.Parse(p)
	if err != nil {
		return nil, fmt.Errorf("proxy %q: %v", p, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("proxy %q has no host", p)
	}
	return u, nil
}

func isLoopback(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// matchList reports whether host is covered by a NO_PROXY-style list. Each
// entry may be "*", an exact host or IP, a CIDR block, a domain suffix written
// as ".example.com" or "*.example.com", a glob such as "192.168.*", or
// "<local>" for dotless names.
func matchList(host string, list []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	for _, raw := range list {
		e := strings.ToLower(strings.TrimSpace(raw))
		if e == "" {
			continue
		}
		if e == "*" {
			return true
		}
		if e == "<local>" {
			if !strings.Contains(host, ".") {
				return true
			}
			continue
		}
		if _, cidr, err := net.ParseCIDR(e); err == nil {
			if ip := net.ParseIP(host); ip != nil && cidr.Contains(ip) {
				return true
			}
			continue
		}
		if strings.HasPrefix(e, "*.") {
			e = e[1:] // "*.example.com" -> ".example.com"
		}
		if strings.HasPrefix(e, ".") {
			if strings.HasSuffix(host, e) || host == e[1:] {
				return true
			}
			continue
		}
		if strings.ContainsAny(e, "*?[") {
			if ok, _ := path.Match(e, host); ok {
				return true
			}
			continue
		}
		if h, _, err := net.SplitHostPort(e); err == nil {
			e = h
		}
		if e == host {
			return true
		}
	}
	return false
}
