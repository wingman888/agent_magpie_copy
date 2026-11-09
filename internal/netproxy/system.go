package netproxy

import (
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// systemConfig is the operating system's proxy setting, already reduced to
// the one proxy magpie should use. proxy is nil when none is enabled.
type systemConfig struct {
	proxy      *url.URL
	exceptions []string
}

// bypasses reports whether the system exception list excludes host.
func (c systemConfig) bypasses(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	ip := net.ParseIP(host)
	for _, item := range c.exceptions {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" || host == "" {
			continue
		}
		switch {
		case item == "<local>":
			if !strings.Contains(host, ".") {
				return true
			}
		case strings.Contains(item, "/"):
			_, n, err := net.ParseCIDR(item)
			if err == nil && ip != nil && n.Contains(ip) {
				return true
			}
		case strings.HasPrefix(item, "*."):
			if host == item[2:] || strings.HasSuffix(host, item[1:]) {
				return true
			}
		case strings.HasPrefix(item, "."):
			if host == item[1:] || strings.HasSuffix(host, item) {
				return true
			}
		default:
			if host == item {
				return true
			}
		}
	}
	return false
}

// systemTTL bounds how long a reading of the system proxy is reused, so a
// change made in the OS takes effect within about this long.
const systemTTL = 10 * time.Second

// loadSystem reads the operating system's setting. It is replaced in tests.
var loadSystem = platformSystemProxy

var sysCache struct {
	mu  sync.Mutex
	at  time.Time
	val systemConfig
	set bool
}

// systemProxy returns the system setting, reading it again after systemTTL.
func systemProxy() systemConfig {
	sysCache.mu.Lock()
	defer sysCache.mu.Unlock()
	if sysCache.set && time.Since(sysCache.at) < systemTTL {
		return sysCache.val
	}
	sysCache.val = loadSystem()
	sysCache.at = time.Now()
	sysCache.set = true
	return sysCache.val
}

// resetSystemCache forces the next systemProxy call to read again.
func resetSystemCache() {
	sysCache.mu.Lock()
	defer sysCache.mu.Unlock()
	sysCache.set = false
}

// parseScutil reads the output of `scutil --proxy` on macOS. Among enabled
// proxies it prefers HTTPS, then HTTP (both used as HTTP proxies), then SOCKS.
func parseScutil(out string) systemConfig {
	kv := map[string]string{}
	var exceptions []string
	inList := false
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "ExceptionsList") {
			inList = true
			continue
		}
		if inList {
			if t == "}" {
				inList = false
				continue
			}
			if i := strings.Index(t, " : "); i >= 0 {
				exceptions = append(exceptions, strings.TrimSpace(t[i+3:]))
			}
			continue
		}
		if i := strings.Index(t, " : "); i >= 0 {
			kv[strings.TrimSpace(t[:i])] = strings.TrimSpace(t[i+3:])
		}
	}
	pick := func(prefix, scheme, defPort string) *url.URL {
		if kv[prefix+"Enable"] != "1" {
			return nil
		}
		host := kv[prefix+"Proxy"]
		if host == "" {
			return nil
		}
		port := kv[prefix+"Port"]
		if port == "" {
			port = defPort
		}
		return &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, port)}
	}
	cfg := systemConfig{exceptions: exceptions}
	cfg.proxy = pick("HTTPS", "http", "443")
	if cfg.proxy == nil {
		cfg.proxy = pick("HTTP", "http", "80")
	}
	if cfg.proxy == nil {
		cfg.proxy = pick("SOCKS", "socks5", "1080")
	}
	return cfg
}

// parseRegQuery reads the output of `reg query KEY` and maps each value name
// to its data. Values of other types are kept as text.
func parseRegQuery(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, rest, ok := cutField(strings.TrimSpace(line))
		if !ok || !strings.HasPrefix(rest, "REG_") {
			continue
		}
		_, val, ok := cutField(rest)
		if !ok {
			continue
		}
		m[name] = val
	}
	return m
}

// cutField splits s at its first run of blanks and returns the first field and
// the rest of s with surrounding blanks trimmed.
func cutField(s string) (first, rest string, ok bool) {
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return "", "", false
	}
	return s[:i], strings.TrimSpace(s[i:]), true
}

// parseWindowsProxy reads the values of the Internet Settings key on Windows.
// ProxyServer is either one host:port, which is an HTTP proxy, or a list of
// protocol=address pairs; the HTTPS, HTTP and SOCKS entries are tried in that
// order. ProxyOverride is a semicolon-separated exception list.
func parseWindowsProxy(enable, server, override string) systemConfig {
	cfg := systemConfig{exceptions: splitList(override)}
	e := strings.ToLower(strings.TrimSpace(enable))
	if e != "1" && e != "0x1" {
		return cfg
	}
	server = strings.TrimSpace(server)
	if server == "" {
		return cfg
	}
	var raw string
	if strings.Contains(server, "=") {
		m := map[string]string{}
		for _, part := range strings.Split(server, ";") {
			if k, v, ok := strings.Cut(part, "="); ok {
				m[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		}
		for _, k := range []string{"https", "http", "socks"} {
			if v := m[k]; v != "" {
				raw = windowsEntry(k, v)
				break
			}
		}
	} else {
		raw = windowsEntry("http", server)
	}
	if raw == "" {
		return cfg
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return cfg
	}
	cfg.proxy = u
	return cfg
}

// windowsEntry gives the URL for one ProxyServer entry: SOCKS is socks5, the
// others are HTTP. A value that already has a scheme is used as it is.
func windowsEntry(key, v string) string {
	if strings.Contains(v, "://") {
		return v
	}
	if key == "socks" {
		return "socks5://" + v
	}
	return "http://" + v
}

func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ";") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
