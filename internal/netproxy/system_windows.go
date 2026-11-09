//go:build windows

package netproxy

import (
	"os/exec"
	"strings"
	"sync"
	"time"
)

const inetKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`

var (
	sysMu    sync.Mutex
	sysAt    time.Time
	sysCache sysProxy
)

// readSystem reads the Internet Options proxy values with `reg query`,
// cached briefly so each request does not start a process.
func readSystem() sysProxy {
	sysMu.Lock()
	defer sysMu.Unlock()
	if time.Since(sysAt) < 30*time.Second {
		return sysCache
	}
	sysAt = time.Now()
	sysCache = sysProxy{}
	out, err := exec.Command("reg", "query", inetKey).Output()
	if err != nil {
		return sysCache
	}
	vals := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		// name  REG_TYPE  value...
		for i := 1; i < len(f); i++ {
			if strings.HasPrefix(f[i], "REG_") {
				vals[f[0]] = strings.Join(f[i+1:], " ")
				break
			}
		}
	}
	if vals["ProxyEnable"] != "0x1" {
		return sysCache
	}
	server := vals["ProxyServer"]
	if strings.Contains(server, "=") {
		parts := map[string]string{}
		for _, kv := range strings.Split(server, ";") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				parts[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		}
		sysCache.HTTPS, sysCache.HTTP, sysCache.SOCKS = parts["https"], parts["http"], parts["socks"]
	} else {
		sysCache.HTTP = server
	}
	for _, e := range strings.Split(vals["ProxyOverride"], ";") {
		switch e = strings.TrimSpace(e); e {
		case "":
		case "<local>":
			sysCache.Local = true
		default:
			sysCache.Except = append(sysCache.Except, e)
		}
	}
	return sysCache
}
