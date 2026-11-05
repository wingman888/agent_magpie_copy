//go:build darwin

package netproxy

import (
	"bufio"
	"bytes"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// systemConfig reads `scutil --proxy`, cached briefly so every request does
// not start a process.
var systemConfig = cachedSystem(func() (*sysConfig, error) {
	out, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		return nil, err
	}
	return parseScutil(out), nil
})

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

// parseScutil reads the `key : value` lines and the ExceptionsList array.
func parseScutil(out []byte) *sysConfig {
	c := &sysConfig{}
	vals := map[string]string{}
	inList := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "ExceptionsList") && strings.Contains(line, "<array>"):
			inList = true
		case inList && line == "}":
			inList = false
		case inList:
			if _, v, ok := strings.Cut(line, " : "); ok {
				c.Exceptions = append(c.Exceptions, strings.TrimSpace(v))
			}
		default:
			if k, v, ok := strings.Cut(line, " : "); ok {
				vals[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	c.SimpleHostnames = vals["ExcludeSimpleHostnames"] == "1"
	c.HTTP = scutilURL(vals, "HTTP", "http")
	c.HTTPS = scutilURL(vals, "HTTPS", "http")
	c.SOCKS = scutilURL(vals, "SOCKS", "socks5")
	return c
}

func scutilURL(vals map[string]string, name, scheme string) *url.URL {
	if vals[name+"Enable"] != "1" || vals[name+"Proxy"] == "" || vals[name+"Port"] == "" {
		return nil
	}
	return &url.URL{Scheme: scheme, Host: vals[name+"Proxy"] + ":" + vals[name+"Port"]}
}
