package netproxy

import (
	"net"
	"net/url"
	"strings"
)

// sysConf is the operating system's proxy setting, reduced to what For needs:
// the one proxy to use (nil when there is none) and the exception list.
type sysConf struct {
	proxy      *url.URL
	exceptions []string
}

// matchException reports whether host is named by a system exception list.
// Entries are compared case-insensitively. "*.example.com" and ".example.com"
// match subdomains and example.com itself, "<local>" matches names without a
// dot, and an IP or CIDR matches addresses. Empty entries are ignored.
func matchException(host string, list []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	ip := net.ParseIP(host)
	for _, raw := range list {
		e := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case e == "":
		case e == "<local>":
			if !strings.Contains(host, ".") {
				return true
			}
		case strings.HasPrefix(e, "*."):
			if domainMatch(host, e[2:]) {
				return true
			}
		case strings.HasPrefix(e, "."):
			if domainMatch(host, e[1:]) {
				return true
			}
		case strings.Contains(e, "/"):
			if n := parseLooseCIDR(e); n != nil && ip != nil && n.Contains(ip) {
				return true
			}
		default:
			if host == e {
				return true
			}
		}
	}
	return false
}

// parseLooseCIDR parses a CIDR whose address may have fewer than four
// octets, as macOS writes it: "169.254/16" means 169.254.0.0/16. It returns
// nil when e is not such a network.
func parseLooseCIDR(e string) *net.IPNet {
	if _, n, err := net.ParseCIDR(e); err == nil {
		return n
	}
	addr, bits, ok := strings.Cut(e, "/")
	if !ok || strings.Contains(addr, ":") {
		return nil
	}
	parts := strings.Split(addr, ".")
	if len(parts) > 4 {
		return nil
	}
	for len(parts) < 4 {
		parts = append(parts, "0")
	}
	_, n, err := net.ParseCIDR(strings.Join(parts, ".") + "/" + bits)
	if err != nil {
		return nil
	}
	return n
}

// domainMatch reports whether host is base or a subdomain of base.
func domainMatch(host, base string) bool {
	if base == "" {
		return false
	}
	return host == base || strings.HasSuffix(host, "."+base)
}

// parseScutil reads the output of `scutil --proxy` on macOS. An enabled HTTPS
// proxy wins over HTTP, and HTTP over SOCKS; HTTP and HTTPS proxies are used as
// http:// and SOCKS as socks5://. When none is enabled the proxy is nil.
func parseScutil(out string) sysConf {
	kv := map[string]string{}
	var exc []string
	inList := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if inList {
			if line == "}" {
				inList = false
				continue
			}
			if _, v, ok := strings.Cut(line, " : "); ok {
				exc = append(exc, strings.TrimSpace(v))
			}
			continue
		}
		k, v, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "ExceptionsList" {
			inList = true
			continue
		}
		kv[k] = v
	}

	var proxy string
	switch {
	case kv["HTTPSEnable"] == "1" && kv["HTTPSProxy"] != "":
		proxy = hostPort("http", kv["HTTPSProxy"], kv["HTTPSPort"])
	case kv["HTTPEnable"] == "1" && kv["HTTPProxy"] != "":
		proxy = hostPort("http", kv["HTTPProxy"], kv["HTTPPort"])
	case kv["SOCKSEnable"] == "1" && kv["SOCKSProxy"] != "":
		proxy = hostPort("socks5", kv["SOCKSProxy"], kv["SOCKSPort"])
	}
	return sysConf{proxy: mustURL(proxy), exceptions: exc}
}

// hostPort joins a scheme, host and optional port into a proxy address.
func hostPort(scheme, host, port string) string {
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host
}

// winSystem turns the Windows manual proxy values into a sysConf. The caller
// has already checked that ProxyEnable is 1. server is ProxyServer and
// override is ProxyOverride, a semicolon-separated exception list.
func winSystem(server, override string) sysConf {
	return sysConf{
		proxy:      mustURL(winServer(server)),
		exceptions: strings.Split(override, ";"),
	}
}

// winServer returns the proxy address that ProxyServer names, or "". It takes
// either one host:port, which is an HTTP proxy, or per-protocol pairs such as
// "http=a:1;https=b:2;socks=c:3". The first of https, http and socks with a
// value is used, and socks becomes socks5://.
func winServer(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	if !strings.Contains(server, "=") {
		return withScheme(server, "http")
	}
	parts := map[string]string{}
	for _, p := range strings.Split(server, ";") {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if _, seen := parts[k]; !seen && v != "" {
			parts[k] = v
		}
	}
	if v := parts["https"]; v != "" {
		return withScheme(v, "http")
	}
	if v := parts["http"]; v != "" {
		return withScheme(v, "http")
	}
	if v := parts["socks"]; v != "" {
		return withScheme(v, "socks5")
	}
	return ""
}

// withScheme adds def:// to s unless s already has a scheme.
func withScheme(s, def string) string {
	if strings.Contains(s, "://") {
		return s
	}
	return def + "://" + s
}

// mustURL parses s, returning nil for "" or an address without a host.
func mustURL(s string) *url.URL {
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	return u
}
