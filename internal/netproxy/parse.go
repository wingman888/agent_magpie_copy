package netproxy

import (
	"net"
	"strings"
)

// These parsers are plain functions with no platform dependency, so they
// build and test on every OS.

// parseScutil reads the output of `scutil --proxy`. It returns the proxy
// URL of the first enabled kind, in the order HTTPS, HTTP, SOCKS, and the
// ExceptionsList. HTTP and HTTPS give http:// URLs and SOCKS gives socks5://.
// It returns "" if none is enabled.
func parseScutil(out string) (string, []string) {
	kv := map[string]string{}
	var except []string
	inExc := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExceptionsList") {
			inExc = true
			continue
		}
		if inExc {
			if line == "}" {
				inExc = false
				continue
			}
			if _, v, ok := strings.Cut(line, " : "); ok {
				if v = strings.TrimSpace(v); v != "" {
					except = append(except, v)
				}
			}
			continue
		}
		if k, v, ok := strings.Cut(line, " : "); ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	for _, c := range []struct{ kind, scheme string }{
		{"HTTPS", "http"},
		{"HTTP", "http"},
		{"SOCKS", "socks5"},
	} {
		if kv[c.kind+"Enable"] != "1" {
			continue
		}
		host := kv[c.kind+"Proxy"]
		if host == "" {
			continue
		}
		addr := host
		if port := kv[c.kind+"Port"]; port != "" {
			addr = net.JoinHostPort(host, port)
		}
		return c.scheme + "://" + addr, except
	}
	return "", except
}

// parseWinProxyServer reads the Windows ProxyServer value. A single host:port
// is an HTTP proxy. A list such as "http=…;https=…;socks=…" gives the first
// of https, http, socks that has a value. It returns "" if there is none.
func parseWinProxyServer(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "=") {
		return withScheme("http", s)
	}
	m := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if v = strings.TrimSpace(v); v != "" && m[k] == "" {
			m[k] = v
		}
	}
	if v := m["https"]; v != "" {
		return withScheme("http", v)
	}
	if v := m["http"]; v != "" {
		return withScheme("http", v)
	}
	if v := m["socks"]; v != "" {
		return withScheme("socks5", v)
	}
	return ""
}

func withScheme(def, v string) string {
	if strings.Contains(v, "://") {
		return v
	}
	return def + "://" + v
}

// splitList splits s on sep, trims each item, and drops empty ones.
func splitList(s, sep string) []string {
	var out []string
	for _, item := range strings.Split(s, sep) {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// matchBypass reports whether host matches an OS exception list:
//   - "<local>" matches a host name without a dot;
//   - "*.example.com" or ".example.com" matches the subdomains of example.com
//     and example.com itself;
//   - anything else must equal the host name.
//
// Matching ignores case, and empty items are skipped.
func matchBypass(host string, list []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
			continue
		case e == "<local>":
			if !strings.Contains(host, ".") {
				return true
			}
		case strings.HasPrefix(e, "*.") || strings.HasPrefix(e, "."):
			d := strings.TrimPrefix(strings.TrimPrefix(e, "*"), ".")
			if host == d || strings.HasSuffix(host, "."+d) {
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
