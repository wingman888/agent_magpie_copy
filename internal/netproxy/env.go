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

// envProxy decides u using the environment variables. decided is false when
// the variables give no proxy for u's scheme, so the caller goes on to the
// system setting. When decided is true, p is the proxy to use, or nil for a
// direct connection because NO_PROXY excludes u.
func envProxy(u *url.URL) (p *url.URL, decided bool, err error) {
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
	port := u.Port()
	if port == "" {
		port = defaultPort(u.Scheme)
	}
	// NO_PROXY excluding the host means direct, even when no variable gives a
	// proxy for this scheme: the system setting is not consulted then.
	if envBypass(e.noProxy, u.Hostname(), port) {
		return nil, true, nil
	}
	if raw == "" {
		return nil, false, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	p, perr := url.Parse(raw)
	if perr != nil || p.Host == "" {
		return nil, true, errors.New("netproxy: a proxy environment variable is not a valid proxy address")
	}
	return p, true, nil
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
