package netproxy

import (
	"net"
	"strings"
)

// sysProxy is one reading of the operating system's proxy settings.
// proxy is "" when no system proxy is enabled or the value is unusable.
type sysProxy struct {
	proxy      string
	exceptions []string
}

// parseScutil reads the output of `scutil --proxy` (macOS). Among the enabled
// proxies, HTTPS is preferred over HTTP, and HTTP over SOCKS. HTTP and HTTPS
// proxies are used as http://, SOCKS as socks5://.
func parseScutil(out string) sysProxy {
	vals := map[string]string{}
	var exc []string
	inExc := false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if inExc {
			if line == "}" {
				inExc = false
				continue
			}
			if _, v, ok := strings.Cut(line, " : "); ok {
				exc = append(exc, strings.TrimSpace(v))
			}
			continue
		}
		key, val, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key == "ExceptionsList" {
			inExc = true
			continue
		}
		vals[key] = val
	}

	addr := func(host, port string) string {
		if host == "" {
			return ""
		}
		if port == "" {
			if strings.Contains(host, ":") {
				return "[" + host + "]"
			}
			return host
		}
		return net.JoinHostPort(host, port)
	}
	enabled := func(k string) bool { return vals[k+"Enable"] == "1" }

	p := ""
	if enabled("HTTPS") {
		if a := addr(vals["HTTPSProxy"], vals["HTTPSPort"]); a != "" {
			p = "http://" + a
		}
	}
	if p == "" && enabled("HTTP") {
		if a := addr(vals["HTTPProxy"], vals["HTTPPort"]); a != "" {
			p = "http://" + a
		}
	}
	if p == "" && enabled("SOCKS") {
		if a := addr(vals["SOCKSProxy"], vals["SOCKSPort"]); a != "" {
			p = "socks5://" + a
		}
	}
	return sysProxy{proxy: usable(p), exceptions: exc}
}

// parseWindowsProxy interprets the Windows ProxyServer and ProxyOverride
// values. The caller has already checked that ProxyEnable is on. ProxyServer
// is either a single host:port (HTTP), or per-protocol pairs such as
// "http=...;https=...;socks=...", of which the first of https, http, socks
// with a value is used. ProxyOverride is a semicolon-separated list.
func parseWindowsProxy(server, override string) sysProxy {
	var exc []string
	for _, e := range strings.Split(override, ";") {
		if e = strings.TrimSpace(e); e != "" {
			exc = append(exc, e)
		}
	}

	server = strings.TrimSpace(server)
	p := ""
	if server != "" && !strings.Contains(server, "=") {
		p = withScheme(server, "http")
	} else if server != "" {
		byProto := map[string]string{}
		for _, part := range strings.Split(server, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok {
				continue
			}
			byProto[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
		for _, c := range []struct{ key, scheme string }{
			{"https", "http"},
			{"http", "http"},
			{"socks", "socks5"},
		} {
			if v := byProto[c.key]; v != "" {
				p = withScheme(v, c.scheme)
				break
			}
		}
	}
	return sysProxy{proxy: usable(p), exceptions: exc}
}

// withScheme adds scheme:// to an address that has none.
func withScheme(addr, scheme string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	return scheme + "://" + addr
}

// usable returns p if it is a proxy address this package can use, and "" if
// not. A system value that cannot be used counts as no system proxy.
func usable(p string) string {
	if p == "" {
		return ""
	}
	if _, err := parseProxy(p); err != nil {
		return ""
	}
	return p
}
