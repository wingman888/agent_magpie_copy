package settings

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ProxyExample is the form shown to users when a proxy value is rejected.
const ProxyExample = "http://127.0.0.1:7890"

// ValidateProxy reports whether s is an acceptable value for Settings.Proxy.
// "" (follow the environment and the system) and "direct" (no proxy) are
// always valid. Otherwise s is a proxy address: a URL whose scheme is http,
// https, socks5 or socks5h and which names a host, or a bare host:port that
// is taken as an HTTP proxy. s is checked as given; callers trim spaces.
func ValidateProxy(s string) error {
	if s == "" || s == "direct" {
		return nil
	}
	bad := func(why string) error {
		return fmt.Errorf("proxy %q is invalid (%s); use direct, or an address such as %s, socks5://127.0.0.1:1080, or 127.0.0.1:7890",
			s, why, ProxyExample)
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return bad("cannot be parsed")
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return bad("scheme must be http, https, socks5 or socks5h")
		}
		if u.Hostname() == "" {
			return bad("missing host")
		}
		if p := u.Port(); p != "" && !validPort(p) {
			return bad("invalid port")
		}
		return nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return bad("expected host:port")
	}
	if host == "" {
		return bad("missing host")
	}
	if !validPort(port) {
		return bad("invalid port")
	}
	return nil
}

func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}
