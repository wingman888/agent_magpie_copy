package netproxy

import (
	"net"
	"strings"
)

// system is what the operating system's proxy settings say. The readers
// are per platform (system_*.go); the parsers here are plain text, so they
// are tested on every platform.
type system struct {
	HTTPS, HTTP, SOCKS string   // host:port, "" when not set
	Exceptions         []string // hosts and patterns that bypass the proxy
	SimpleLocal        bool     // host names without a dot bypass it
}

// readSystem is replaced in tests.
var readSystem = platformSystem

// parseScutil reads the output of `scutil --proxy` on macOS.
func parseScutil(out string) system {
	kv := map[string]string{}
	var exc []string
	inExc := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if inExc {
			if line == "}" {
				inExc = false
				continue
			}
			if _, v, ok := strings.Cut(line, " : "); ok {
				exc = append(exc, strings.TrimSpace(v))
			}
			continue
		}
		if strings.HasPrefix(line, "ExceptionsList : ") {
			inExc = true
			continue
		}
		if k, v, ok := strings.Cut(line, " : "); ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	s := system{Exceptions: exc, SimpleLocal: kv["ExcludeSimpleHostnames"] == "1"}
	if kv["HTTPSEnable"] == "1" {
		s.HTTPS = joinHostPort(kv["HTTPSProxy"], kv["HTTPSPort"])
	}
	if kv["HTTPEnable"] == "1" {
		s.HTTP = joinHostPort(kv["HTTPProxy"], kv["HTTPPort"])
	}
	if kv["SOCKSEnable"] == "1" {
		s.SOCKS = joinHostPort(kv["SOCKSProxy"], kv["SOCKSPort"])
	}
	return s
}

func joinHostPort(host, port string) string {
	if host == "" {
		return ""
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

// parseWindows reads the Internet Settings values on Windows: enabled is
// ProxyEnable, server is ProxyServer ("host:port" for all protocols, or
// "http=…;https=…;socks=…"), override is ProxyOverride ("a;b;<local>").
func parseWindows(enabled bool, server, override string) system {
	var s system
	server = strings.TrimSpace(server)
	if !enabled || server == "" {
		return s
	}
	if !strings.Contains(server, "=") {
		s.HTTPS, s.HTTP = server, server
	} else {
		for _, part := range strings.Split(server, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "https":
				s.HTTPS = v
			case "http":
				s.HTTP = v
			case "socks":
				s.SOCKS = v
			}
		}
	}
	for _, e := range strings.Split(override, ";") {
		e = strings.TrimSpace(e)
		switch {
		case e == "":
		case strings.EqualFold(e, "<local>"):
			s.SimpleLocal = true
		default:
			s.Exceptions = append(s.Exceptions, e)
		}
	}
	return s
}
