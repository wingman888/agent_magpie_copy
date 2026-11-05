//go:build windows

package netproxy

import (
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows/registry"
)

// systemConfig reads the current user's Internet Settings, cached briefly.
var systemConfig = cachedSystem(readWindows)

func cachedSystem(read func() (*sysConfig, error)) func() (*sysConfig, error) {
	var mu sync.Mutex
	var cfg *sysConfig
	var at time.Time
	return func() (*sysConfig, error) {
		mu.Lock()
		defer mu.Unlock()
		if cfg != nil && time.Since(at) < 30*time.Second {
			return cfg, nil
		}
		c, err := read()
		if err != nil {
			return nil, err
		}
		cfg, at = c, time.Now()
		return c, nil
	}
}

func readWindows() (*sysConfig, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	if enable, _, err := k.GetIntegerValue("ProxyEnable"); err != nil || enable == 0 {
		return &sysConfig{}, nil
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return &sysConfig{}, nil
	}
	override, _, _ := k.GetStringValue("ProxyOverride")
	return parseWindows(server, override), nil
}

// parseWindows reads ProxyServer ("host:port" for all schemes, or
// "http=…;https=…;socks=…") and ProxyOverride (";"-separated exceptions,
// where "<local>" means host names without a dot).
func parseWindows(server, override string) *sysConfig {
	c := &sysConfig{}
	for _, e := range strings.Split(override, ";") {
		e = strings.TrimSpace(e)
		if e == "<local>" {
			c.SimpleHostnames = true
		} else if e != "" {
			c.Exceptions = append(c.Exceptions, e)
		}
	}
	if !strings.Contains(server, "=") {
		if u := winURL("http", server); u != nil {
			c.HTTP, c.HTTPS = u, u
		}
		return c
	}
	for _, part := range strings.Split(server, ";") {
		name, addr, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch strings.ToLower(name) {
		case "http":
			c.HTTP = winURL("http", addr)
		case "https":
			c.HTTPS = winURL("http", addr) // an https entry is still spoken to with CONNECT
		case "socks":
			c.SOCKS = winURL("socks5", addr)
		}
	}
	return c
}

func winURL(scheme, addr string) *url.URL {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return nil
		}
		return u
	}
	return &url.URL{Scheme: scheme, Host: addr}
}
