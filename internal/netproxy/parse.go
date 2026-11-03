package netproxy

import (
	"net"
	"net/url"
	"strings"
)

// parseDarwinProxy reads the output of `scutil --proxy`. Among the enabled
// proxies it picks HTTPS first, then HTTP, then SOCKS. HTTP and HTTPS proxies
// are returned as http://, SOCKS as socks5://. It returns no proxy when none is
// enabled and has a host. The exception list is returned in any case.
func parseDarwinProxy(out string) systemSettings {
	var sys systemSettings
	kv := map[string]string{}
	inExc := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if inExc {
			if line == "}" {
				inExc = false
				continue
			}
			if _, v, ok := strings.Cut(line, " : "); ok {
				if v = strings.TrimSpace(v); v != "" {
					sys.Exceptions = append(sys.Exceptions, v)
				}
			}
			continue
		}
		if strings.HasPrefix(line, "ExceptionsList") && strings.Contains(line, "<array>") {
			inExc = true
			continue
		}
		if k, v, ok := strings.Cut(line, " : "); ok {
			kv[k] = strings.TrimSpace(v)
		}
	}

	candidates := []struct{ key, scheme, defPort string }{
		{"HTTPS", "http", "443"},
		{"HTTP", "http", "80"},
		{"SOCKS", "socks5", "1080"},
	}
	for _, c := range candidates {
		if kv[c.key+"Enable"] != "1" {
			continue
		}
		host := kv[c.key+"Proxy"]
		if host == "" {
			continue
		}
		port := kv[c.key+"Port"]
		if port == "" {
			port = c.defPort
		}
		sys.Proxy = c.scheme + "://" + net.JoinHostPort(host, port)
		break
	}
	return sys
}

// parseWindowsProxy turns the Internet Settings values ProxyServer and
// ProxyOverride into a system proxy. The caller has already checked that
// ProxyEnable is 1.
//
// ProxyServer is either one host:port, which is an HTTP proxy, or a list
// such as "http=…;https=…;socks=…". For a list, HTTPS is taken first, then
// HTTP, then SOCKS. HTTPS and HTTP entries are HTTP proxies and get http://,
// SOCKS entries get socks5://, unless the value already has a scheme.
// ProxyOverride is a ';' separated list of exceptions.
func parseWindowsProxy(server, override string) systemSettings {
	var sys systemSettings
	for _, e := range strings.Split(override, ";") {
		if e = strings.TrimSpace(e); e != "" {
			sys.Exceptions = append(sys.Exceptions, e)
		}
	}

	server = strings.TrimSpace(server)
	if server == "" {
		return sys
	}
	if strings.Contains(server, "=") {
		vals := map[string]string{}
		for _, part := range strings.Split(server, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok {
				continue
			}
			vals[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
		server = ""
		for _, c := range []struct{ key, scheme string }{
			{"https", "http"},
			{"http", "http"},
			{"socks", "socks5"},
		} {
			if v := vals[c.key]; v != "" {
				server = withScheme(v, c.scheme)
				break
			}
		}
		if server == "" {
			return sys
		}
	} else {
		server = withScheme(server, "http")
	}

	if u, err := url.Parse(server); err == nil && u.Host != "" {
		sys.Proxy = u.String()
	}
	return sys
}

// withScheme adds scheme:// to v unless v already has a scheme.
func withScheme(v, scheme string) string {
	if strings.Contains(v, "://") {
		return v
	}
	return scheme + "://" + v
}
