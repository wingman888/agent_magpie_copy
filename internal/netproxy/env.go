package netproxy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// envConfig follows golang.org/x/net/http/httpproxy, which is what
// http.ProxyFromEnvironment uses. That one reads the environment only once
// per process; this one reads it on every call, so a changed variable (or a
// test's t.Setenv) counts. ALL_PROXY, which the standard library ignores, is
// used when the scheme's own variable is unset, as curl does.
type envConfig struct {
	httpProxy, httpsProxy, allProxy string
	noProxy                         string
	cgi                             bool
}

func fromEnvironment() envConfig {
	return envConfig{
		httpProxy:  getenvAny("HTTP_PROXY", "http_proxy"),
		httpsProxy: getenvAny("HTTPS_PROXY", "https_proxy"),
		allProxy:   getenvAny("ALL_PROXY", "all_proxy"),
		noProxy:    getenvAny("NO_PROXY", "no_proxy"),
		cgi:        os.Getenv("REQUEST_METHOD") != "",
	}
}

func getenvAny(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// set reports whether the environment names any proxy; when it does, it
// alone decides, NO_PROXY included.
func (c envConfig) set() bool {
	return c.httpProxy != "" || c.httpsProxy != "" || c.allProxy != ""
}

func (c envConfig) proxyFor(u *url.URL) (*url.URL, error) {
	var raw string
	switch u.Scheme {
	case "https", "wss":
		raw = c.httpsProxy
	case "http", "ws":
		raw = c.httpProxy
		if raw != "" && c.cgi {
			return nil, errors.New("refusing to use HTTP_PROXY value in CGI environment; see golang.org/s/cgihttpproxy")
		}
	}
	if raw == "" {
		raw = c.allProxy
	}
	if raw == "" {
		return nil, nil
	}
	p, err := parseEnvProxy(raw)
	if err != nil || p == nil {
		return nil, nil // an unusable variable is ignored, as httpproxy does
	}
	if !c.useProxy(canonicalAddr(u)) {
		return nil, nil
	}
	return p, nil
}

// parseEnvProxy is httpproxy's parseProxy: a value without a scheme is
// taken as http://value.
func parseEnvProxy(proxy string) (*url.URL, error) {
	u, err := url.Parse(proxy)
	if err != nil || u.Scheme == "" || u.Host == "" {
		if u2, err2 := url.Parse("http://" + proxy); err2 == nil {
			return u2, nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("invalid proxy address %q: %v", proxy, err)
	}
	return u, nil
}

// useProxy is httpproxy's NO_PROXY check. addr is host:port.
func (c envConfig) useProxy(addr string) bool {
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
	for _, p := range strings.Split(c.noProxy, ",") {
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
		phost, pport, err := net.SplitHostPort(p)
		if err == nil {
			if phost == "" {
				continue
			}
			if phost[0] == '[' && phost[len(phost)-1] == ']' {
				phost = phost[1 : len(phost)-1]
			}
		} else {
			phost, pport = p, ""
		}
		if pip := net.ParseIP(phost); pip != nil {
			if ip != nil && pip.Equal(ip) && (pport == "" || pport == port) {
				return false
			}
			continue
		}
		if phost == "" {
			continue
		}
		if strings.HasPrefix(phost, "*.") {
			phost = phost[1:]
		}
		matchHost := false
		if phost[0] != '.' {
			matchHost = true
			phost = "." + phost
		}
		if (strings.HasSuffix(host, phost) || (matchHost && host == phost[1:])) && (pport == "" || pport == port) {
			return false
		}
	}
	return true
}

var defaultPorts = map[string]string{"http": "80", "https": "443", "ws": "80", "wss": "443", "socks5": "1080", "socks5h": "1080"}

// canonicalAddr is u's host:port, with the scheme's port when u names none.
func canonicalAddr(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = defaultPorts[u.Scheme]
	}
	return net.JoinHostPort(host, port)
}
