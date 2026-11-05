package netproxy

import (
	"net"
	"strings"
)

// The functions in this file are pure, so they are tested on every platform.
// The platform files feed them the text they read from the operating system.

// parseScutil reads the output of `scutil --proxy` (macOS). An enabled HTTPS
// proxy wins over an enabled HTTP proxy, which wins over an enabled SOCKS
// proxy. A proxy needs both a host and a port. HTTP and HTTPS proxies come
// back as http://, SOCKS as socks5://.
func parseScutil(out string) systemConfig {
	kv := map[string]string{}
	var bypass []string
	inExceptions := false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "ExceptionsList") && strings.Contains(line, "<array>") {
			inExceptions = true
			continue
		}
		if inExceptions {
			if line == "}" {
				inExceptions = false
				continue
			}
			if i := strings.Index(line, " : "); i >= 0 {
				if item := strings.TrimSpace(line[i+3:]); item != "" {
					bypass = append(bypass, item)
				}
			}
			continue
		}
		if i := strings.Index(line, " : "); i >= 0 {
			kv[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+3:])
		}
	}

	addr := func(kind string) string {
		host, port := kv[kind+"Proxy"], kv[kind+"Port"]
		if host == "" || port == "" {
			return ""
		}
		return net.JoinHostPort(host, port)
	}
	enabled := func(kind string) bool { return kv[kind+"Enable"] == "1" }

	cfg := systemConfig{bypass: bypass}
	switch {
	case enabled("HTTPS") && addr("HTTPS") != "":
		cfg.proxy = "http://" + addr("HTTPS")
	case enabled("HTTP") && addr("HTTP") != "":
		cfg.proxy = "http://" + addr("HTTP")
	case enabled("SOCKS") && addr("SOCKS") != "":
		cfg.proxy = "socks5://" + addr("SOCKS")
	}
	return cfg
}

// parseWindowsProxyServer reads the ProxyServer registry value. It is either a
// single host:port (an HTTP proxy) or a list such as
// "http=a:1;https=b:2;socks=c:3". The https, http and socks entries are tried
// in that order. It returns "" when no proxy is named.
func parseWindowsProxyServer(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "=") {
		return withScheme("http", s)
	}
	values := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		values[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	if v := values["https"]; v != "" {
		return withScheme("http", v)
	}
	if v := values["http"]; v != "" {
		return withScheme("http", v)
	}
	if v := values["socks"]; v != "" {
		return withScheme("socks5", v)
	}
	return ""
}

// withScheme prefixes addr with scheme:// unless it already has a scheme.
func withScheme(scheme, addr string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	return scheme + "://" + addr
}

// parseBypassList splits a semicolon-separated exception list (the Windows
// ProxyOverride value). Empty items are dropped.
func parseBypassList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ";") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// matchBypass reports whether host is covered by a system exception list.
// Matching is case-insensitive. Items are:
//   - an exact host name;
//   - "*.example.com" or ".example.com", which match subdomains and example.com itself;
//   - "<local>", which matches host names without a dot.
//
// Empty items are ignored.
func matchBypass(host string, list []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, item := range list {
		item = strings.ToLower(strings.TrimSpace(item))
		switch {
		case item == "":
			continue
		case item == "<local>":
			if !strings.Contains(host, ".") {
				return true
			}
		case strings.HasPrefix(item, "*.") || strings.HasPrefix(item, "."):
			d := strings.TrimPrefix(strings.TrimPrefix(item, "*"), ".")
			if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
				return true
			}
		default:
			if host == item {
				return true
			}
		}
	}
	return false
}
