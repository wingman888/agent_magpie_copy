package netproxy

import (
	"strings"
)

// parseScutil reads the output of `scutil --proxy` on macOS.
func parseScutil(out string) sysProxy {
	var sp sysProxy
	kv := map[string]string{}
	inExc := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if inExc {
			if line == "}" {
				inExc = false
				continue
			}
			if i := strings.Index(line, " : "); i >= 0 {
				sp.Exceptions = append(sp.Exceptions, line[i+3:])
			}
			continue
		}
		if strings.HasPrefix(line, "ExceptionsList") {
			inExc = true
			continue
		}
		if i := strings.Index(line, " : "); i >= 0 {
			kv[line[:i]] = line[i+3:]
		}
	}
	if kv["ExcludeSimpleHostnames"] == "1" {
		sp.Exceptions = append(sp.Exceptions, "<local>")
	}
	host := func(name string) string {
		if kv[name+"Enable"] != "1" || kv[name+"Proxy"] == "" {
			return ""
		}
		h := kv[name+"Proxy"]
		if p := kv[name+"Port"]; p != "" {
			h += ":" + p
		}
		return h
	}
	sp.HTTP = host("HTTP")
	sp.HTTPS = host("HTTPS")
	sp.SOCKS = host("SOCKS")
	return sp
}

// parseWindowsProxy turns the Internet Settings values into a sysProxy.
// server is ProxyServer, either "host:port" for every protocol or
// "http=h:p;https=h:p;socks=h:p"; override is ProxyOverride, separated by ";".
func parseWindowsProxy(enabled bool, server, override string) sysProxy {
	var sp sysProxy
	if !enabled {
		return sp
	}
	if strings.Contains(server, "=") {
		for _, part := range strings.Split(server, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok {
				continue
			}
			switch strings.ToLower(k) {
			case "http":
				sp.HTTP = v
			case "https":
				sp.HTTPS = v
			case "socks":
				sp.SOCKS = v
			}
		}
	} else {
		sp.HTTP = strings.TrimSpace(server)
	}
	for _, e := range strings.Split(override, ";") {
		if e = strings.TrimSpace(e); e != "" {
			sp.Exceptions = append(sp.Exceptions, e)
		}
	}
	return sp
}
