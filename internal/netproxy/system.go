package netproxy

import (
	"net"
	"strconv"
	"strings"
)

// systemProxy is one reading of the operating system's proxy settings.
type systemProxy struct {
	URL    string   // proxy URL such as http://127.0.0.1:7890, or "" for none
	Bypass []string // exception list; matching hosts go direct
}

// parseScutil reads the output of `scutil --proxy` (macOS). An enabled HTTPS
// proxy is preferred, then HTTP, then SOCKS. HTTPS and HTTP proxies are used
// as http:// proxies and SOCKS as socks5://. A proxy whose Enable flag is not
// "1", or that lacks a host or a valid port, is ignored.
func parseScutil(out string) systemProxy {
	values := map[string]string{}
	var bypass []string
	inList := false
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if inList {
			if t == "}" {
				inList = false
				continue
			}
			if _, v, ok := strings.Cut(t, " : "); ok {
				if v = strings.TrimSpace(v); v != "" {
					bypass = append(bypass, v)
				}
			}
			continue
		}
		key, val, ok := strings.Cut(t, " : ")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if key == "ExceptionsList" {
			inList = strings.HasSuffix(val, "{")
			continue
		}
		values[key] = val
	}

	sp := systemProxy{Bypass: bypass}
	for _, c := range []struct{ prefix, scheme string }{
		{"HTTPS", "http"},
		{"HTTP", "http"},
		{"SOCKS", "socks5"},
	} {
		if u := scutilEndpoint(values, c.prefix, c.scheme); u != "" {
			sp.URL = u
			break
		}
	}
	return sp
}

// scutilEndpoint builds the URL for one proxy kind, or "" if it is not usable.
func scutilEndpoint(values map[string]string, prefix, scheme string) string {
	if values[prefix+"Enable"] != "1" {
		return ""
	}
	host, port := values[prefix+"Proxy"], values[prefix+"Port"]
	n, err := strconv.Atoi(port)
	if host == "" || err != nil || n < 1 || n > 65535 {
		return ""
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(n))
}

// parseWindowsProxy reads the ProxyServer and ProxyOverride registry values.
// ProxyServer is either one host:port, taken as an HTTP proxy, or a list such
// as "http=...;https=...;socks=...". From that list the first of https, http
// and socks that has a value is used. ProxyOverride is a ";"-separated list.
// The caller is responsible for checking that ProxyEnable is 1.
func parseWindowsProxy(server, override string) systemProxy {
	sp := systemProxy{Bypass: splitList(override, ";")}
	server = strings.TrimSpace(server)
	if server == "" {
		return sp
	}
	if !strings.Contains(server, "=") {
		sp.URL = withScheme(server, "http")
		return sp
	}
	parts := map[string]string{}
	for _, p := range strings.Split(server, ";") {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if k != "" && v != "" {
			parts[k] = v
		}
	}
	switch {
	case parts["https"] != "":
		sp.URL = "http://" + stripScheme(parts["https"])
	case parts["http"] != "":
		sp.URL = "http://" + stripScheme(parts["http"])
	case parts["socks"] != "":
		sp.URL = "socks5://" + stripScheme(parts["socks"])
	}
	return sp
}

// matchBypass reports whether host is on an exception list. Matching is
// case-insensitive and follows these rules:
//   - an exact host name matches itself;
//   - "*.example.com" or ".example.com" matches subdomains and example.com;
//   - "<local>" matches host names without a dot;
//   - empty entries are ignored.
func matchBypass(host string, list []string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "" {
		return false
	}
	for _, raw := range list {
		item := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case item == "":
			continue
		case item == "<local>":
			if !strings.Contains(h, ".") {
				return true
			}
		case strings.HasPrefix(item, "*.") || strings.HasPrefix(item, "."):
			suffix := strings.TrimPrefix(item, "*") // ".example.com"
			if len(suffix) <= 1 {
				continue
			}
			if h == suffix[1:] || strings.HasSuffix(h, suffix) {
				return true
			}
		default:
			if h == item {
				return true
			}
		}
	}
	return false
}

// splitList splits s on sep and drops blank items.
func splitList(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// withScheme adds scheme:// unless v already has a scheme.
func withScheme(v, scheme string) string {
	if strings.Contains(v, "://") {
		return v
	}
	return scheme + "://" + v
}

// stripScheme removes any "scheme://" prefix, leaving host:port.
func stripScheme(v string) string {
	if i := strings.Index(v, "://"); i >= 0 {
		return v[i+3:]
	}
	return v
}
