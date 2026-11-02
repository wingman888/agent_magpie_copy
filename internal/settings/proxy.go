package settings

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ProxyDirect is the Proxy value that turns proxying off altogether.
const ProxyDirect = "direct"

// proxyExample is quoted in every error so the user sees what is expected.
const proxyExample = `use an address such as http://127.0.0.1:7890 or socks5://127.0.0.1:1080, or "direct" for no proxy`

// ParseProxy reads a Proxy setting. "" and "direct" give a nil URL and no
// error: the first means "decide automatically", the second "no proxy",
// which the caller tells apart by the string itself. Anything else must be
// an http, https, socks5 or socks5h URL with a host, or a bare host:port,
// which means an HTTP proxy.
func ParseProxy(s string) (*url.URL, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, ProxyDirect) {
		return nil, nil
	}
	u, err := parseProxy(s)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy %q: %v; %s", s, err, proxyExample)
	}
	return u, nil
}

func parseProxy(s string) (*url.URL, error) {
	if strings.ContainsAny(s, " \t\r\n") {
		return nil, errors.New("it contains spaces")
	}
	if !strings.Contains(s, "://") {
		host, port, err := net.SplitHostPort(s)
		if err != nil {
			return nil, errors.New("it is neither a URL nor host:port")
		}
		if host == "" || port == "" {
			return nil, errors.New("host or port is missing")
		}
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("scheme %q is not supported (http, https, socks5, socks5h)", u.Scheme)
	}
	if u.Opaque != "" || u.Hostname() == "" {
		return nil, errors.New("the host is missing")
	}
	if p := u.Port(); p != "" {
		for _, c := range p {
			if c < '0' || c > '9' {
				return nil, fmt.Errorf("port %q is not a number", p)
			}
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("the port is empty")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("a proxy address has no path, query or fragment")
	}
	u.Path = ""
	return u, nil
}
