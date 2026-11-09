package netproxy

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
)

// envSettings are the proxy variables, read from the process environment on
// every call so that changes apply at once. The names and their semantics
// follow net/http's ProxyFromEnvironment, which is not used directly because
// it reads the environment only once.
type envSettings struct {
	https, http, all, noProxy string
}

func readEnv() envSettings {
	get := func(names ...string) string {
		for _, n := range names {
			if v := strings.TrimSpace(os.Getenv(n)); v != "" {
				return v
			}
		}
		return ""
	}
	e := envSettings{
		https:   get("HTTPS_PROXY", "https_proxy"),
		http:    get("HTTP_PROXY", "http_proxy"),
		all:     get("ALL_PROXY", "all_proxy"),
		noProxy: get("NO_PROXY", "no_proxy"),
	}
	// Under CGI, HTTP_PROXY comes from the client's header, so net/http ignores it.
	if os.Getenv("REQUEST_METHOD") != "" {
		e.http = ""
	}
	return e
}

// envConfigured reports whether any proxy variable is set. When one is, the
// environment decides for every request, as it does in net/http.
func envConfigured() bool {
	e := readEnv()
	return e.https != "" || e.http != "" || e.all != ""
}

// envProxy returns the environment's proxy for u, or nil when the variables
// give none or NO_PROXY excludes u.
func envProxy(u *url.URL) (*url.URL, error) {
	e := readEnv()
	var raw string
	switch u.Scheme {
	case "https":
		raw = e.https
	case "http":
		raw = e.http
	}
	if raw == "" {
		raw = e.all
	}
	if raw == "" {
		return nil, nil
	}
	port := u.Port()
	if port == "" {
		port = defaultPort(u.Scheme)
	}
	if envBypass(e.noProxy, u.Hostname(), port) {
		return nil, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	p, err := url.Parse(raw)
	if err != nil || p.Host == "" {
		return nil, errors.New("netproxy: a proxy environment variable is not a valid proxy address")
	}
	return p, nil
}

func defaultPort(scheme string) string {
	switch scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

// envBypass applies a NO_PROXY list. "*" excludes everything. Entries are
// host names (a leading "." or "*." also matches subdomains), IP addresses,
// CIDR blocks, and optionally host:port.
func envBypass(list, host, port string) bool {
	host = strings.ToLower(host)
	ip := net.ParseIP(host)
	for _, item := range strings.Split(list, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if item == "*" {
			return true
		}
		if envEntryMatches(item, host, port, ip) {
			return true
		}
	}
	return false
}

func envEntryMatches(item, host, port string, ip net.IP) bool {
	if strings.Contains(item, "/") {
		_, n, err := net.ParseCIDR(item)
		return err == nil && ip != nil && n.Contains(ip)
	}
	entryHost, entryPort := item, ""
	if h, p, err := net.SplitHostPort(item); err == nil {
		entryHost, entryPort = h, p
	}
	if entryPort != "" && entryPort != port {
		return false
	}
	if eip := net.ParseIP(entryHost); eip != nil {
		return ip != nil && eip.Equal(ip)
	}
	if ip != nil {
		return false
	}
	entryHost = strings.TrimPrefix(strings.TrimPrefix(entryHost, "*"), ".")
	if entryHost == "" {
		return false
	}
	return host == entryHost || strings.HasSuffix(host, "."+entryHost)
}
