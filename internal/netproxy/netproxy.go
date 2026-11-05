// Package netproxy decides which proxy, if any, a request goes through.
//
// The order is magpie's own setting (settings.json "proxy"), then the
// HTTPS_PROXY / HTTP_PROXY / ALL_PROXY environment, then the operating
// system's proxy settings. Loopback addresses never use a proxy.
package netproxy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

func init() {
	// Clients that use http.DefaultClient (provider calls, catalogs, usage)
	// follow the same rule as the gateway.
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = ProxyFunc
	}
}

// ProxyFunc is For in the shape http.Transport.Proxy wants.
func ProxyFunc(r *http.Request) (*url.URL, error) { return For(r.URL) }

// For returns the proxy a request to u should use, or nil to connect directly.
func For(u *url.URL) (*url.URL, error) {
	if u == nil {
		return nil, errors.New("netproxy: no URL")
	}
	host := strings.ToLower(u.Hostname())
	if loopback(host) {
		return nil, nil
	}
	if s := settings.Load().Proxy; s != "" {
		if s == "direct" {
			return nil, nil
		}
		return parse(s)
	}
	if listMatch(host, splitList(envAny("NO_PROXY"))) {
		return nil, nil
	}
	if v := envProxy(u.Scheme); v != "" {
		return parse(v)
	}
	sys := system()
	if listMatch(host, sys.Exceptions) {
		return nil, nil
	}
	if v := sys.url(); v != "" {
		return parse(v)
	}
	return nil, nil
}

func parse(v string) (*url.URL, error) {
	n, err := settings.NormalizeProxy(v)
	if err != nil {
		return nil, err
	}
	if n == "" || n == "direct" {
		return nil, fmt.Errorf("netproxy: %q is not a proxy address", v)
	}
	return url.Parse(n)
}

// envProxy is the proxy named for a scheme in the environment, if any.
func envProxy(scheme string) string {
	var names []string
	switch strings.ToLower(scheme) {
	case "https":
		names = append(names, "HTTPS_PROXY")
	case "http":
		names = append(names, "HTTP_PROXY")
	}
	names = append(names, "ALL_PROXY")
	for _, n := range names {
		if v := envAny(n); v != "" {
			return v
		}
	}
	return ""
}

// envAny reads a variable in upper case, then lower case.
func envAny(name string) string {
	for _, n := range []string{strings.ToUpper(name), strings.ToLower(name)} {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// loopback is true for localhost and the loopback IP addresses.
func loopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// listMatch reports whether host is covered by an exception list, in the
// shared syntax of NO_PROXY and the OS exception lists. "*" matches
// everything; "<local>" matches names without a dot; "example.com" matches
// the host and its subdomains (".example.com" and "*.example.com" too);
// "*.corp" and "10.*" are shell-style wildcards; "10.0.0.0/8" and "169.254/16"
// are CIDR ranges; "127.0.0.1" or "host:port" is a literal address.
func listMatch(host string, list []string) bool {
	host = strings.ToLower(host)
	ip := net.ParseIP(host)
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
			continue
		case e == "*":
			return true
		case e == "<local>":
			if !strings.Contains(host, ".") {
				return true
			}
			continue
		case strings.Contains(e, "/"):
			if ip != nil && cidrHas(e, ip) {
				return true
			}
			continue
		}
		if h, _, err := net.SplitHostPort(e); err == nil {
			e = h
		}
		e = strings.TrimPrefix(strings.TrimPrefix(e, "*"), ".")
		if e == "" {
			continue
		}
		if strings.Contains(e, "*") {
			if ok, _ := path.Match(e, host); ok {
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

func cidrHas(e string, ip net.IP) bool {
	addr, bits, _ := strings.Cut(e, "/")
	if !strings.Contains(addr, ":") {
		for strings.Count(addr, ".") < 3 {
			addr += ".0"
		}
	}
	_, n, err := net.ParseCIDR(addr + "/" + bits)
	return err == nil && n.Contains(ip)
}
