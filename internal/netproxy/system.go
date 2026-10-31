package netproxy

import (
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// sysProxy is the OS-level proxy and its exception list.
type sysProxy struct {
	URL        *url.URL
	Exceptions []string
}

func (s *sysProxy) bypass(host string) bool { return matchException(s.Exceptions, host) }

// Hooks for tests.
var (
	goos       = runtime.GOOS
	runCommand = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	}
	// systemOverride, when set, replaces reading the OS entirely.
	systemOverride func() *sysProxy
	sysTTL         = 10 * time.Second
)

var (
	sysMu  sync.Mutex
	sysAt  time.Time
	sysVal *sysProxy
)

// ResetSystemCache forgets the cached OS proxy.
func ResetSystemCache() {
	sysMu.Lock()
	sysAt = time.Time{}
	sysMu.Unlock()
}

func systemProxy() *sysProxy {
	if systemOverride != nil {
		return systemOverride()
	}
	sysMu.Lock()
	defer sysMu.Unlock()
	if !sysAt.IsZero() && nowFunc().Sub(sysAt) < sysTTL {
		return sysVal
	}
	sysVal = readSystem()
	sysAt = nowFunc()
	return sysVal
}

func readSystem() *sysProxy {
	switch goos {
	case "darwin":
		out, err := runCommand("scutil", "--proxy")
		if err != nil {
			return nil
		}
		return parseScutil(string(out))
	case "windows":
		out, err := runCommand("reg", "query", `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`)
		if err != nil {
			return nil
		}
		return parseWindowsRegistry(string(out))
	}
	return nil
}

// matchException reports whether host is in a system exception list.
func matchException(list []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == "<local>" {
			if !strings.Contains(host, ".") {
				return true
			}
			continue
		}
		if e == host {
			return true
		}
		if strings.HasPrefix(e, "*.") {
			e = e[1:]
		}
		if strings.HasPrefix(e, ".") && (host == e[1:] || strings.HasSuffix(host, e)) {
			return true
		}
	}
	return false
}

// hostPort builds a proxy URL from host/port with the given scheme.
func mkProxy(scheme, host, port string) *url.URL {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil
	}
	addr := host
	if port = strings.TrimSpace(port); port != "" {
		addr = joinHostPort(host, port)
	}
	u, _, err := settings.ParseProxy(scheme + "://" + addr)
	if err != nil {
		return nil
	}
	return u
}

func joinHostPort(host, port string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return host + ":" + port
}

// parseScutil parses the output of `scutil --proxy`.
func parseScutil(text string) *sysProxy {
	kv := map[string]string{}
	var exc []string
	inExc := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if inExc {
			if strings.HasPrefix(line, "}") {
				inExc = false
				continue
			}
			if _, v, ok := strings.Cut(line, " : "); ok {
				exc = append(exc, strings.TrimSpace(v))
			}
			continue
		}
		k, v, ok := strings.Cut(line, " : ")
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
	for _, c := range []struct{ name, scheme string }{
		{"HTTPS", "http"}, {"HTTP", "http"}, {"SOCKS", "socks5"},
	} {
		if kv[c.name+"Enable"] != "1" {
			continue
		}
		if u := mkProxy(c.scheme, kv[c.name+"Proxy"], kv[c.name+"Port"]); u != nil {
			return &sysProxy{URL: u, Exceptions: exc}
		}
	}
	return nil
}

var regLine = regexp.MustCompile(`^\s*(\S+)\s+(REG_\w+)\s*(.*)$`)

// parseWindowsRegistry parses `reg query` output for Internet Settings.
func parseWindowsRegistry(text string) *sysProxy {
	vals := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if m := regLine.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			vals[strings.ToLower(m[1])] = strings.TrimSpace(m[3])
		}
	}
	en := strings.ToLower(vals["proxyenable"])
	if en != "0x1" && en != "1" {
		return nil
	}
	server := vals["proxyserver"]
	if server == "" {
		return nil
	}
	var exc []string
	for _, e := range strings.Split(vals["proxyoverride"], ";") {
		if e = strings.TrimSpace(e); e != "" {
			exc = append(exc, e)
		}
	}
	mk := func(scheme, v string) *url.URL {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil
		}
		if strings.Contains(v, "://") {
			u, _, err := settings.ParseProxy(v)
			if err != nil {
				return nil
			}
			return u
		}
		return mkProxy(scheme, v, "")
	}
	if !strings.Contains(server, "=") {
		if u := mk("http", server); u != nil {
			return &sysProxy{URL: u, Exceptions: exc}
		}
		return nil
	}
	m := map[string]string{}
	for _, part := range strings.Split(server, ";") {
		if k, v, ok := strings.Cut(part, "="); ok {
			m[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	for _, c := range []struct{ key, scheme string }{
		{"https", "http"}, {"http", "http"}, {"socks", "socks5"},
	} {
		if u := mk(c.scheme, m[c.key]); u != nil {
			return &sysProxy{URL: u, Exceptions: exc}
		}
	}
	return nil
}
