package netproxy

import (
	"bufio"
	"strings"
)

// parseScutil reads the output of macOS `scutil --proxy`.
func parseScutil(out string) Proxy {
	kv := map[string]string{}
	var exc []string
	inExc := false
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if inExc {
			if line == "}" {
				inExc = false
			} else if _, v, ok := strings.Cut(line, ":"); ok {
				exc = append(exc, strings.TrimSpace(v))
			}
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "ExceptionsList" {
			inExc = true
			continue
		}
		kv[k] = v
	}
	for _, p := range []struct{ key, scheme string }{{"HTTPS", "http"}, {"HTTP", "http"}, {"SOCKS", "socks5"}} {
		host := kv[p.key+"Proxy"]
		if kv[p.key+"Enable"] != "1" || host == "" {
			continue
		}
		addr := host
		if port := kv[p.key+"Port"]; port != "" {
			addr += ":" + port
		}
		return Proxy{URL: p.scheme + "://" + addr, Bypass: exc}
	}
	return Proxy{}
}

// parseWindows reads the Internet Options values ProxyServer and ProxyOverride.
func parseWindows(server, override string, enable bool) Proxy {
	server = strings.TrimSpace(server)
	if !enable || server == "" {
		return Proxy{}
	}
	var addr string
	if !strings.Contains(server, "=") {
		addr = "http://" + stripScheme(server)
	} else {
		m := map[string]string{}
		for _, part := range strings.Split(server, ";") {
			if k, v, ok := strings.Cut(part, "="); ok && strings.TrimSpace(v) != "" {
				m[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		}
		switch {
		case m["https"] != "":
			addr = "http://" + stripScheme(m["https"])
		case m["http"] != "":
			addr = "http://" + stripScheme(m["http"])
		case m["socks"] != "":
			addr = "socks5://" + stripScheme(m["socks"])
		default:
			return Proxy{}
		}
	}
	var bypass []string
	for _, e := range strings.Split(override, ";") {
		if e = strings.TrimSpace(e); e != "" {
			bypass = append(bypass, e)
		}
	}
	return Proxy{URL: addr, Bypass: bypass}
}

func stripScheme(s string) string {
	if _, rest, ok := strings.Cut(s, "://"); ok {
		return rest
	}
	return s
}
