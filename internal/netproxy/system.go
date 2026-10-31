package netproxy

import (
	"bufio"
	"context"
	"net"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// sysProxy is the proxy the operating system is configured with.
type sysProxy struct {
	URL        *url.URL
	Exceptions []string
}

const systemTTL = 10 * time.Second

var (
	// goos and systemProxyReader are variables so tests can replace them.
	goos              = runtime.GOOS
	systemProxyReader = readSystemProxy

	sysMu     sync.Mutex
	sysAt     time.Time
	sysCached sysProxy
	sysOK     bool
)

// ResetSystemCache forgets the cached system proxy.
func ResetSystemCache() {
	sysMu.Lock()
	sysAt = time.Time{}
	sysMu.Unlock()
}

func systemProxy() (sysProxy, bool) {
	sysMu.Lock()
	defer sysMu.Unlock()
	if !sysAt.IsZero() && time.Since(sysAt) < systemTTL {
		return sysCached, sysOK
	}
	sysCached, sysOK = systemProxyReader()
	sysAt = time.Now()
	return sysCached, sysOK
}

// readSystemProxy reads the OS settings; any failure means "no system proxy".
func readSystemProxy() (sp sysProxy, ok bool) {
	defer func() {
		if recover() != nil {
			sp, ok = sysProxy{}, false
		}
	}()
	switch goos {
	case "darwin":
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "scutil", "--proxy").Output()
		if err != nil {
			return sysProxy{}, false
		}
		return parseScutil(string(out))
	case "windows":
		server, override, enabled := readWindowsRegistry()
		return parseWindows(server, override, enabled)
	}
	return sysProxy{}, false
}

func hostPort(host, port string) string {
	if port == "" || port == "0" {
		return host
	}
	return net.JoinHostPort(host, port)
}

// parseScutil parses the output of `scutil --proxy`.
func parseScutil(out string) (sysProxy, bool) {
	vals := map[string]string{}
	var exceptions []string
	inExc := false
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "}" {
			inExc = false
			continue
		}
		k, v, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "ExceptionsList" {
			inExc = strings.HasPrefix(v, "<array>")
			continue
		}
		if inExc {
			if v != "" {
				exceptions = append(exceptions, v)
			}
			continue
		}
		vals[k] = v
	}
	for _, c := range []struct{ name, scheme string }{
		{"HTTPS", "http"}, {"HTTP", "http"}, {"SOCKS", "socks5"},
	} {
		if vals[c.name+"Enable"] != "1" {
			continue
		}
		host := vals[c.name+"Proxy"]
		if host == "" {
			continue
		}
		u, err := url.Parse(c.scheme + "://" + hostPort(host, vals[c.name+"Port"]))
		if err != nil || u.Hostname() == "" {
			continue
		}
		return sysProxy{URL: u, Exceptions: exceptions}, true
	}
	return sysProxy{}, false
}

// parseWindows interprets the Internet Options values ProxyServer and
// ProxyOverride; enabled is ProxyEnable.
func parseWindows(server, override string, enabled bool) (sysProxy, bool) {
	server = strings.TrimSpace(server)
	if !enabled || server == "" {
		return sysProxy{}, false
	}
	mk := func(scheme, v string) *url.URL {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil
		}
		if !strings.Contains(v, "://") {
			v = scheme + "://" + v
		}
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" {
			return nil
		}
		return u
	}
	var pick *url.URL
	if !strings.Contains(server, "=") {
		pick = mk("http", server)
	} else {
		m := map[string]string{}
		for _, part := range strings.Split(server, ";") {
			k, v, ok := strings.Cut(part, "=")
			if ok {
				m[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		}
		if pick = mk("http", m["https"]); pick == nil {
			if pick = mk("http", m["http"]); pick == nil {
				pick = mk("socks5", m["socks"])
			}
		}
	}
	if pick == nil {
		return sysProxy{}, false
	}
	return sysProxy{URL: pick, Exceptions: strings.Split(override, ";")}, true
}

// bypass reports whether host matches the system exception list.
func bypass(host string, list []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
		case e == "<local>":
			if !strings.Contains(host, ".") {
				return true
			}
		case strings.HasPrefix(e, "*.") || strings.HasPrefix(e, "."):
			d := strings.TrimPrefix(strings.TrimPrefix(e, "*"), ".")
			if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
				return true
			}
		case host == e:
			return true
		}
	}
	return false
}
