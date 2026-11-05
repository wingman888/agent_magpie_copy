package netproxy

import (
	"strings"
	"sync"
	"time"
)

// sysProxy is the operating system's proxy setting, as URLs.
type sysProxy struct {
	HTTPS, HTTP, SOCKS string   // "" when off
	Exceptions         []string // the OS exception list, same syntax as NO_PROXY
}

// url picks the system proxy to use: HTTPS, then HTTP, then SOCKS.
func (s sysProxy) url() string {
	for _, v := range []string{s.HTTPS, s.HTTP, s.SOCKS} {
		if v != "" {
			return v
		}
	}
	return ""
}

var sysCache struct {
	sync.Mutex
	at  time.Time
	val sysProxy
}

// system reads the OS proxy settings, at most every 30 seconds: on macOS
// that means running a command.
func system() sysProxy {
	sysCache.Lock()
	defer sysCache.Unlock()
	if time.Since(sysCache.at) < 30*time.Second {
		return sysCache.val
	}
	sysCache.val, sysCache.at = readSystem(), time.Now()
	return sysCache.val
}

// parseScutil reads `scutil --proxy` output (macOS).
func parseScutil(out []byte) sysProxy {
	kv := map[string]string{}
	var exc []string
	inExc := false
	for _, line := range strings.Split(string(out), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "ExceptionsList"):
			inExc = true
		case inExc && t == "}":
			inExc = false
		case inExc:
			if _, v, ok := strings.Cut(t, " : "); ok {
				exc = append(exc, v)
			}
		default:
			if k, v, ok := strings.Cut(t, " : "); ok {
				kv[k] = v
			}
		}
	}
	on := func(k string) bool { return kv[k] == "1" }
	var s sysProxy
	if on("HTTPSEnable") && kv["HTTPSProxy"] != "" {
		s.HTTPS = "http://" + hostPort(kv["HTTPSProxy"], kv["HTTPSPort"])
	}
	if on("HTTPEnable") && kv["HTTPProxy"] != "" {
		s.HTTP = "http://" + hostPort(kv["HTTPProxy"], kv["HTTPPort"])
	}
	if on("SOCKSEnable") && kv["SOCKSProxy"] != "" {
		s.SOCKS = "socks5://" + hostPort(kv["SOCKSProxy"], kv["SOCKSPort"])
	}
	s.Exceptions = exc
	if on("ExcludeSimpleHostnames") {
		s.Exceptions = append(s.Exceptions, "<local>")
	}
	return s
}

// parseWinProxy reads the Internet Settings values (Windows). The server is
// either one host:port for every protocol or "http=…;https=…;socks=…".
func parseWinProxy(enabled bool, server, override string) sysProxy {
	var s sysProxy
	if !enabled || strings.TrimSpace(server) == "" {
		return s
	}
	server = strings.TrimSpace(server)
	if !strings.Contains(server, "=") {
		s.HTTP, s.HTTPS = "http://"+server, "http://"+server
	}
	for _, part := range strings.Split(server, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || v == "" {
			continue
		}
		switch strings.ToLower(k) {
		case "http":
			s.HTTP = "http://" + v
		case "https":
			s.HTTPS = "http://" + v
		case "socks", "socks5":
			s.SOCKS = "socks5://" + v
		}
	}
	for _, e := range strings.Split(override, ";") {
		if e = strings.TrimSpace(e); e != "" {
			s.Exceptions = append(s.Exceptions, e)
		}
	}
	return s
}

func hostPort(h, p string) string {
	if p == "" {
		return h
	}
	return h + ":" + p
}
