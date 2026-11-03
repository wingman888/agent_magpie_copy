package netproxy

import (
	"net"
	"net/url"
	"strings"
)

// system is the operating system's proxy configuration, reduced to what For
// needs. HTTPS, HTTP and SOCKS are host:port, "" when that proxy is off.
type system struct {
	HTTPS, HTTP, SOCKS string
	Exceptions         []string
	// NoLocal bypasses host names without a dot ("local names").
	NoLocal bool
}

// systemProxy reads the operating system's settings. Tests replace it.
// An unreadable setting counts as no system proxy.
var systemProxy = readSystem

// systemFor returns the system proxy for host, nil when it should be direct.
func systemFor(host string) *url.URL {
	sys := systemProxy()
	if sys.NoLocal && !strings.Contains(host, ".") && net.ParseIP(host) == nil {
		return nil
	}
	for _, rule := range sys.Exceptions {
		if matches(host, rule) {
			return nil
		}
	}
	switch {
	case sys.HTTPS != "":
		return &url.URL{Scheme: "http", Host: sys.HTTPS}
	case sys.HTTP != "":
		return &url.URL{Scheme: "http", Host: sys.HTTP}
	case sys.SOCKS != "":
		return &url.URL{Scheme: "socks5", Host: sys.SOCKS}
	}
	return nil
}

// parseScutil reads the output of `scutil --proxy`.
func parseScutil(out string) system {
	var sys system
	kv := map[string]string{}
	inList := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExceptionsList") {
			inList = true
			continue
		}
		if inList {
			if line == "}" {
				inList = false
				continue
			}
			if _, v, ok := strings.Cut(line, " : "); ok {
				sys.Exceptions = append(sys.Exceptions, strings.TrimSpace(v))
			}
			continue
		}
		if k, v, ok := strings.Cut(line, " : "); ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	addr := func(name string) string {
		if kv[name+"Enable"] != "1" || kv[name+"Proxy"] == "" {
			return ""
		}
		if port := kv[name+"Port"]; port != "" {
			return net.JoinHostPort(kv[name+"Proxy"], port)
		}
		return kv[name+"Proxy"]
	}
	sys.HTTPS = addr("HTTPS")
	sys.HTTP = addr("HTTP")
	sys.SOCKS = addr("SOCKS")
	sys.NoLocal = kv["ExcludeSimpleHostnames"] == "1"
	return sys
}

// parseWindows reads the Internet Settings values ProxyEnable, ProxyServer
// and ProxyOverride. ProxyServer is either one host:port for every protocol
// or a list such as "http=h:p;https=h:p;socks=h:p".
func parseWindows(enable, server, override string) system {
	var sys system
	if e := strings.ToLower(strings.TrimSpace(enable)); e != "1" && e != "0x1" {
		return sys
	}
	server = strings.TrimSpace(server)
	if strings.Contains(server, "=") {
		for _, part := range strings.Split(server, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok {
				continue
			}
			v = stripScheme(strings.TrimSpace(v))
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "https":
				sys.HTTPS = v
			case "http":
				sys.HTTP = v
			case "socks":
				sys.SOCKS = v
			}
		}
	} else if server != "" {
		sys.HTTP = stripScheme(server)
	}
	for _, r := range strings.Split(override, ";") {
		r = strings.TrimSpace(r)
		switch {
		case r == "":
		case strings.EqualFold(r, "<local>"):
			sys.NoLocal = true
		default:
			sys.Exceptions = append(sys.Exceptions, r)
		}
	}
	return sys
}

// stripScheme turns "http://h:p" into "h:p".
func stripScheme(v string) string {
	if _, rest, ok := strings.Cut(v, "://"); ok {
		return rest
	}
	return v
}

// regValue returns the data of the value line in `reg query` output:
//
//	ProxyServer    REG_SZ    127.0.0.1:7890
func regValue(out string) string {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !strings.HasPrefix(f[1], "REG_") {
			continue
		}
		i := strings.Index(line, f[1])
		return strings.TrimSpace(line[i+len(f[1]):])
	}
	return ""
}
