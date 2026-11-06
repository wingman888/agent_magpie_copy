package netproxy

import (
	"strings"
	"sync"
	"time"
)

// The system proxy is read at most every systemTTL: it is asked once per
// outgoing request, and the OS query is a process launch on macOS and Windows.
const systemTTL = 30 * time.Second

var sysCache struct {
	sync.Mutex
	at  time.Time
	val systemProxy
}

func sysProxy() systemProxy {
	sysCache.Lock()
	defer sysCache.Unlock()
	if !sysCache.at.IsZero() && time.Since(sysCache.at) < systemTTL {
		return sysCache.val
	}
	sysCache.val = loadSystem()
	sysCache.at = time.Now()
	return sysCache.val
}

// parseScutil reads the output of `scutil --proxy` on macOS.
func parseScutil(out string) systemProxy {
	var s systemProxy
	values := map[string]string{}
	inExceptions := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "ExceptionsList") {
			inExceptions = true
			continue
		}
		if line == "}" {
			inExceptions = false
			continue
		}
		key, val, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if inExceptions {
			if isNumber(key) && val != "" {
				s.Exceptions = append(s.Exceptions, val)
			}
			continue
		}
		values[key] = val
	}
	host := func(name string) string {
		h := values[name+"Proxy"]
		port := values[name+"Port"]
		if values[name+"Enable"] != "1" || h == "" {
			return ""
		}
		if port != "" {
			h = h + ":" + port
		}
		return h
	}
	if h := host("HTTPS"); h != "" {
		s.HTTPS = "http://" + h
	}
	if h := host("HTTP"); h != "" {
		s.HTTP = "http://" + h
	}
	if h := host("SOCKS"); h != "" {
		s.SOCKS = "socks5://" + h
	}
	s.SimpleHostnames = values["ExcludeSimpleHostnames"] == "1"
	return s
}

// parseWindows reads the Internet Options values: ProxyEnable (0x1 or 1),
// ProxyServer ("host:port" for every protocol, or "http=…;https=…;socks=…")
// and ProxyOverride ("a;b;<local>").
func parseWindows(enable, server, override string) systemProxy {
	var s systemProxy
	e := strings.ToLower(strings.TrimSpace(enable))
	if e != "0x1" && e != "1" {
		return s
	}
	withScheme := func(a string) string {
		if strings.Contains(a, "://") {
			return a
		}
		return "http://" + a
	}
	server = strings.TrimSpace(server)
	if strings.Contains(server, "=") {
		for _, part := range strings.Split(server, ";") {
			proto, addr, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok || strings.TrimSpace(addr) == "" {
				continue
			}
			addr = strings.TrimSpace(addr)
			switch strings.ToLower(strings.TrimSpace(proto)) {
			case "https":
				s.HTTPS = withScheme(addr)
			case "http":
				s.HTTP = withScheme(addr)
			case "socks":
				if !strings.Contains(addr, "://") {
					addr = "socks5://" + addr
				}
				s.SOCKS = addr
			}
		}
	} else if server != "" {
		s.HTTP = withScheme(server)
		s.HTTPS = s.HTTP
	}
	for _, x := range strings.Split(override, ";") {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		if strings.EqualFold(x, "<local>") {
			s.SimpleHostnames = true
			continue
		}
		s.Exceptions = append(s.Exceptions, x)
	}
	return s
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
