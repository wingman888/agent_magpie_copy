//go:build windows

package netproxy

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// readPlatform reads the Internet Options from the current user's registry.
// ProxyServer is either "host:port" for all protocols, or a list such as
// "http=host:port;https=host:port;socks=host:port".
func readPlatform() system {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return system{}
	}
	defer k.Close()
	var s system
	if en, _, err := k.GetIntegerValue("ProxyEnable"); err != nil || en != 1 {
		return s
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return s
	}
	if ov, _, err := k.GetStringValue("ProxyOverride"); err == nil {
		for _, e := range strings.Split(ov, ";") {
			s.exceptions = append(s.exceptions, strings.TrimSpace(e))
		}
	}
	if !strings.Contains(server, "=") {
		s.http = strings.TrimSpace(server)
		s.https = s.http
		return s
	}
	for _, part := range strings.Split(server, ";") {
		name, addr, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		addr = strings.TrimSpace(addr)
		switch strings.ToLower(name) {
		case "https":
			s.https = addr
		case "http":
			s.http = addr
		case "socks":
			s.socks = addr
		}
	}
	return s
}
