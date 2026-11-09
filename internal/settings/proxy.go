package settings

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ProxyExamples is shown when a proxy value cannot be used.
const ProxyExamples = "http://127.0.0.1:7890, https://proxy.example.com:8443, socks5://127.0.0.1:1080, socks5h://127.0.0.1:1080, or 127.0.0.1:7890 (host:port is an HTTP proxy)"

// CheckProxy reports whether v is a usable value for Settings.Proxy after
// trimming: "" (follow the environment and system), "direct" (no proxy), or a
// proxy address. A proxy address has one of the schemes http, https, socks5,
// socks5h and a host and a valid port; a bare host:port is an HTTP proxy.
func CheckProxy(v string) error {
	v = strings.TrimSpace(v)
	if v == "" || v == "direct" {
		return nil
	}
	bad := func() error {
		return fmt.Errorf("proxy %q is not valid; write it as one of: %s, or direct", v, ProxyExamples)
	}
	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil {
			return bad()
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return bad()
		}
		if !validHostPort(u.Host) {
			return bad()
		}
		if u.Path != "" && u.Path != "/" {
			return bad()
		}
		return nil
	}
	if !validHostPort(v) {
		return bad()
	}
	return nil
}

// validHostPort accepts "host:port" with a non-empty host and a port 1-65535.
func validHostPort(hp string) bool {
	host, port, err := net.SplitHostPort(hp)
	if err != nil || host == "" {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}
