package netproxy

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
)

// envConfig is the proxy environment, read afresh on every call so a
// changed environment is honoured (net/http caches it for the process).
// The semantics follow golang.org/x/net/http/httpproxy, which is what
// http.ProxyFromEnvironment uses, plus ALL_PROXY as a fallback for both
// schemes.
type envConfig struct {
	httpProxy, httpsProxy, allProxy, noProxy string
}

func getenv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

func fromEnvironment() envConfig {
	c := envConfig{
		httpsProxy: getenv("HTTPS_PROXY", "https_proxy"),
		allProxy:   getenv("ALL_PROXY", "all_proxy"),
		noProxy:    getenv("NO_PROXY", "no_proxy"),
	}
	if os.Getenv("REQUEST_METHOD") != "" {
		// In a CGI environment HTTP_PROXY may come from a request header
		// (httpoxy); like net/http, don't trust it.
		c.httpProxy = os.Getenv("http_proxy")
	} else {
		c.httpProxy = getenv("HTTP_PROXY", "http_proxy")
	}
	return c
}

// raw is the configured proxy for scheme, or "".
func (c envConfig) raw(scheme string) string {
	var v string
	switch strings.ToLower(scheme) {
	case "https", "wss":
		v = c.httpsProxy
	case "http", "ws":
		v = c.httpProxy
	default:
		return ""
	}
	if v == "" {
		v = c.allProxy
	}
	return strings.TrimSpace(v)
}

// has reports whether the environment configures a proxy for scheme.
func (c envConfig) has(scheme string) bool { return c.raw(scheme) != "" }

// proxyFor applies the environment to u: the proxy for its scheme unless
// NO_PROXY excludes the host.
func (c envConfig) proxyFor(u *url.URL) (*url.URL, error) {
	raw := c.raw(u.Scheme)
	if raw == "" {
		return nil, nil
	}
	p, err := parseEnvProxy(raw)
	if err != nil {
		return nil, err
	}
	if !useProxy(c.noProxy, canonicalAddr(u)) {
		return nil, nil
	}
	return p, nil
}

// parseEnvProxy parses a proxy variable the way httpproxy does: a missing
// scheme means http.
func parseEnvProxy(proxy string) (*url.URL, error) {
	u, err := url.Parse(proxy)
	if err != nil || u.Scheme == "" || u.Host == "" {
		if u2, err2 := url.Parse("http://" + proxy); err2 == nil {
			return u2, nil
		}
	}
	if err != nil {
		return nil, errors.New("invalid proxy address " + proxy + ": " + err.Error())
	}
	return u, nil
}

var defaultPorts = map[string]string{"http": "80", "https": "443", "ws": "80", "wss": "443", "socks5": "1080", "socks5h": "1080"}

func canonicalAddr(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = defaultPorts[strings.ToLower(u.Scheme)]
	}
	return net.JoinHostPort(host, port)
}

// useProxy reports whether NO_PROXY lets addr (host:port) use a proxy,
// with the same matching as httpproxy: "*" disables proxying, IPs and
// CIDRs match addresses, "example.com" matches it and its subdomains,
// ".example.com" and "*.example.com" only subdomains, and an optional
// :port narrows any of those.
func useProxy(noProxy, addr string) bool {
	if addr == "" {
		return true
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return false
	}
	host = strings.ToLower(strings.TrimSpace(host))
	for _, p := range strings.Split(noProxy, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if p == "*" {
			return false
		}
		if _, cidr, err := net.ParseCIDR(p); err == nil {
			if ip != nil && cidr.Contains(ip) {
				return false
			}
			continue
		}
		if pip := net.ParseIP(p); pip != nil {
			if ip != nil && pip.Equal(ip) {
				return false
			}
			continue
		}
		phost, pport := p, ""
		if h, pt, err := net.SplitHostPort(p); err == nil {
			phost, pport = h, pt
			if len(phost) > 1 && phost[0] == '[' && phost[len(phost)-1] == ']' {
				phost = phost[1 : len(phost)-1]
			}
			if pip := net.ParseIP(phost); pip != nil {
				if ip != nil && pip.Equal(ip) && pport == port {
					return false
				}
				continue
			}
		}
		if phost == "" {
			continue
		}
		if phost[0] == '*' {
			phost = phost[1:]
		}
		matchHost := false
		if phost == "" || phost[0] != '.' {
			matchHost = true
			phost = "." + phost
		}
		if strings.HasSuffix(host, phost) || (matchHost && host == phost[1:]) {
			if pport == "" || pport == port {
				return false
			}
		}
	}
	return true
}
