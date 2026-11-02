package netproxy

import (
	"bufio"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sysProxy is the operating system's proxy: the one to use and the hosts
// that bypass it.
type sysProxy struct {
	URL        *url.URL
	Exceptions []string
}

// systemProxyFunc returns the system proxy; tests replace it.
var systemProxyFunc = cachedSystemProxy

// sysTTL is how long a reading of the system proxy is trusted, so a change
// in the system settings counts within that long.
const sysTTL = 10 * time.Second

var sysCache struct {
	sync.Mutex
	at  time.Time
	val sysProxy
}

func cachedSystemProxy() sysProxy {
	sysCache.Lock()
	defer sysCache.Unlock()
	if !sysCache.at.IsZero() && time.Since(sysCache.at) < sysTTL {
		return sysCache.val
	}
	v, err := readSystemProxy()
	if err != nil {
		v = sysProxy{} // can't tell: no system proxy
	}
	sysCache.val, sysCache.at = v, time.Now()
	return v
}

// parseScutil reads `scutil --proxy`, macOS's network proxy settings:
//
//	<dictionary> {
//	  ExceptionsList : <array> {
//	    0 : *.local
//	  }
//	  HTTPEnable : 1
//	  HTTPPort : 7890
//	  HTTPProxy : 127.0.0.1
//	  ...
//	}
//
// Of the enabled proxies HTTPS wins over HTTP, which wins over SOCKS. HTTP
// and HTTPS proxies are HTTP proxies; SOCKS is socks5.
func parseScutil(out string) sysProxy {
	vals := map[string]string{}
	var exceptions []string
	inList, depth := false, 0
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if inList {
			if line == "}" {
				inList = false
				continue
			}
			if _, v, ok := strings.Cut(line, " : "); ok {
				exceptions = append(exceptions, strings.TrimSpace(v))
			}
			continue
		}
		if line == "}" {
			depth--
			continue
		}
		k, v, ok := strings.Cut(line, " : ")
		if !ok {
			if strings.HasSuffix(line, "{") {
				depth++
			}
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if strings.HasSuffix(v, "{") {
			if k == "ExceptionsList" && depth <= 1 {
				inList = true
			} else {
				depth++
			}
			continue
		}
		if depth <= 1 {
			vals[k] = v
		}
	}
	if vals["ExcludeSimpleHostnames"] == "1" {
		exceptions = append(exceptions, "<local>")
	}
	for _, kind := range []struct{ key, scheme string }{{"HTTPS", "http"}, {"HTTP", "http"}, {"SOCKS", "socks5"}} {
		if vals[kind.key+"Enable"] != "1" {
			continue
		}
		host := vals[kind.key+"Proxy"]
		if host == "" {
			continue
		}
		if u := proxyURL(kind.scheme, host, vals[kind.key+"Port"]); u != nil {
			return sysProxy{URL: u, Exceptions: exceptions}
		}
	}
	return sysProxy{}
}

// proxyURL builds scheme://host:port; port may be empty or "0".
func proxyURL(scheme, host, port string) *url.URL {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return nil
	}
	if n, err := strconv.Atoi(port); err == nil && n > 0 {
		host = net.JoinHostPort(host, strconv.Itoa(n))
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return &url.URL{Scheme: scheme, Host: host}
}

// parseWindows reads the Internet Options values: ProxyEnable, ProxyServer
// and ProxyOverride. ProxyServer is either one host:port for everything (an
// HTTP proxy) or per-protocol "http=…;https=…;socks=…", where the first of
// https, http and socks that has a value is used. ProxyOverride is the
// semicolon-separated list of exceptions.
func parseWindows(enabled bool, server, override string) sysProxy {
	server = strings.TrimSpace(server)
	if !enabled || server == "" {
		return sysProxy{}
	}
	var u *url.URL
	if !strings.Contains(server, "=") {
		u = windowsEntry("http", strings.Trim(server, "; "))
	} else {
		byProto := map[string]string{}
		for _, part := range strings.Split(server, ";") {
			k, v, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
			if v != "" && byProto[k] == "" {
				byProto[k] = v
			}
		}
		for _, kind := range []struct{ key, scheme string }{{"https", "http"}, {"http", "http"}, {"socks", "socks5"}} {
			if v := byProto[kind.key]; v != "" {
				if u = windowsEntry(kind.scheme, v); u != nil {
					break
				}
			}
		}
	}
	if u == nil {
		return sysProxy{}
	}
	return sysProxy{URL: u, Exceptions: strings.Split(override, ";")}
}

// windowsEntry turns one ProxyServer value, "host:port" or with a scheme of
// its own, into a proxy URL.
func windowsEntry(scheme, v string) *url.URL {
	if i := strings.Index(v, "://"); i >= 0 {
		s := strings.ToLower(v[:i])
		v = v[i+3:]
		switch s {
		case "socks", "socks5", "socks4":
			scheme = "socks5"
		case "socks5h":
			scheme = "socks5h"
		case "http", "https":
			if scheme != "socks5" {
				scheme = s
			}
		}
	}
	v = strings.TrimRight(v, "/")
	if v == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		host, port = v, ""
	}
	return proxyURL(scheme, host, port)
}

// bypassed reports whether host is on the system's exceptions list:
//   - the same name, any case;
//   - *.example.com or .example.com: example.com and every name under it;
//   - <local>: a name without a dot;
//   - other patterns with *, such as 192.168.*: a wildcard match.
//
// Empty entries are ignored.
func bypassed(host string, exceptions []string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "" {
		return false
	}
	for _, e := range exceptions {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
			continue
		case e == "<local>":
			if !strings.Contains(host, ".") && net.ParseIP(host) == nil {
				return true
			}
		case strings.HasPrefix(e, "*.") || strings.HasPrefix(e, "."):
			domain := strings.TrimPrefix(strings.TrimPrefix(e, "*"), ".")
			if domain != "" && (host == domain || strings.HasSuffix(host, "."+domain)) {
				return true
			}
		case e == host:
			return true
		case strings.Contains(e, "*"):
			if ok, _ := path.Match(e, host); ok {
				return true
			}
		}
	}
	return false
}
