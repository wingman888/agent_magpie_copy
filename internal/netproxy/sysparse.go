package netproxy

import (
	"net"
	"strconv"
	"strings"
)

// sysConfig is the operating system proxy: one proxy URL for outbound
// requests (empty if none) and the exception list.
type sysConfig struct {
	proxy      string
	exceptions []string
}

// parseScutil reads the output of `scutil --proxy` on macOS. HTTPS beats HTTP,
// and HTTP beats SOCKS. HTTP and HTTPS proxies are used as http://, SOCKS as
// socks5://. A proxy that is not enabled, or has no host, is skipped.
func parseScutil(out string) sysConfig {
	vals := map[string]string{}
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
				if v = strings.TrimSpace(v); v != "" {
					exc = append(exc, v)
				}
			}
			continue
		}
		if strings.HasPrefix(line, "ExceptionsList") {
			inList = strings.Contains(line, "<array>")
			continue
		}
		if k, v, ok := strings.Cut(line, " : "); ok {
			vals[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}

	enabled := func(k string) bool { return vals[k+"Enable"] == "1" && vals[k+"Proxy"] != "" }
	for _, k := range []string{"HTTPS", "HTTP"} {
		if enabled(k) {
			return sysConfig{
				proxy:      "http://" + hostPort(vals[k+"Proxy"], vals[k+"Port"]),
				exceptions: exc,
			}
		}
	}
	if enabled("SOCKS") {
		return sysConfig{
			proxy:      "socks5://" + hostPort(vals["SOCKSProxy"], vals["SOCKSPort"]),
			exceptions: exc,
		}
	}
	return sysConfig{}
}

// hostPort joins a host and an optional port. A bare IPv6 address without a
// port is wrapped in brackets.
func hostPort(host, port string) string {
	if port == "" {
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			return "[" + host + "]"
		}
		return host
	}
	return net.JoinHostPort(host, port)
}

// parseWindowsProxy reads the manual proxy settings from the Internet Settings
// key. enable is ProxyEnable, server is ProxyServer and override is
// ProxyOverride. A single "host:port" server is HTTP. A "proto=addr;..." list
// uses the first non-empty of https, http, socks, in that order.
func parseWindowsProxy(enable, server, override string) sysConfig {
	n, err := strconv.ParseUint(strings.TrimSpace(enable), 0, 32)
	if err != nil || n != 1 {
		return sysConfig{}
	}
	var exc []string
	for _, e := range strings.Split(override, ";") {
		if e = strings.TrimSpace(e); e != "" {
			exc = append(exc, e)
		}
	}

	server = strings.TrimSpace(server)
	if server == "" {
		return sysConfig{}
	}
	var proxy string
	if !strings.Contains(server, "=") {
		proxy = withScheme(server, "http")
	} else {
		parts := map[string]string{}
		for _, seg := range strings.Split(server, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(seg), "=")
			if !ok {
				continue
			}
			parts[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
		switch {
		case parts["https"] != "":
			proxy = withScheme(parts["https"], "http")
		case parts["http"] != "":
			proxy = withScheme(parts["http"], "http")
		case parts["socks"] != "":
			proxy = withScheme(parts["socks"], "socks5")
		}
	}
	if proxy == "" {
		return sysConfig{}
	}
	return sysConfig{proxy: proxy, exceptions: exc}
}

// withScheme keeps addr as it is if it already has a scheme, and otherwise
// puts def in front.
func withScheme(addr, def string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	return def + "://" + addr
}

// parseRegValue returns the value of the named entry in `reg query` output,
// or "" if the entry is missing. The format is name, type and value, separated
// by whitespace. The value may contain spaces.
func parseRegValue(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if len(line) <= len(name) || !strings.EqualFold(line[:len(name)], name) {
			continue
		}
		rest := line[len(name):]
		if rest[0] != ' ' && rest[0] != '\t' {
			continue
		}
		rest = strings.TrimLeft(rest, " \t")
		i := strings.IndexAny(rest, " \t")
		if i < 0 {
			return ""
		}
		return strings.TrimSpace(rest[i:])
	}
	return ""
}

// matchException reports whether host is listed in a system exception list.
// "<local>" matches names without a dot. "*.example.com" and ".example.com"
// match subdomains and example.com itself. Other entries must equal the host.
// Matching ignores case.
func matchException(host string, list []string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
		case e == "<local>":
			if !strings.Contains(h, ".") {
				return true
			}
		case strings.HasPrefix(e, "*."):
			d := e[1:]
			if h == d[1:] || strings.HasSuffix(h, d) {
				return true
			}
		case strings.HasPrefix(e, "."):
			if h == e[1:] || strings.HasSuffix(h, e) {
				return true
			}
		default:
			if h == e {
				return true
			}
		}
	}
	return false
}
