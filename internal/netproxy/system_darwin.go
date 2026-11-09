//go:build darwin

package netproxy

import (
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	sysMu    sync.Mutex
	sysAt    time.Time
	sysCache sysProxy
)

// readSystem asks `scutil --proxy` (the network settings), cached briefly so
// each request does not start a process.
func readSystem() sysProxy {
	sysMu.Lock()
	defer sysMu.Unlock()
	if time.Since(sysAt) < 30*time.Second {
		return sysCache
	}
	sysAt = time.Now()
	out, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		sysCache = sysProxy{}
		return sysCache
	}
	sysCache = parseScutil(string(out))
	return sysCache
}

// parseScutil reads the dictionary `scutil --proxy` prints.
func parseScutil(out string) sysProxy {
	var p sysProxy
	vals := map[string]string{}
	inExcept := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExceptionsList") {
			inExcept = true
			continue
		}
		if inExcept {
			if line == "}" {
				inExcept = false
				continue
			}
			if _, v, ok := strings.Cut(line, " : "); ok {
				p.Except = append(p.Except, strings.TrimSpace(v))
			}
			continue
		}
		if k, v, ok := strings.Cut(line, " : "); ok {
			vals[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	p.Local = vals["ExcludeSimpleHostnames"] == "1"
	hostPort := func(name string) string {
		h, port := vals[name+"Proxy"], vals[name+"Port"]
		if vals[name+"Enable"] != "1" || h == "" {
			return ""
		}
		if port != "" {
			return h + ":" + port
		}
		return h
	}
	p.HTTP = hostPort("HTTP")
	p.HTTPS = hostPort("HTTPS")
	p.SOCKS = hostPort("SOCKS")
	return p
}
