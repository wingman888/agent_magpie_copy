package netproxy

import (
	"bufio"
	"context"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// sysProxy is the OS proxy: the proxy for web requests and the hosts that
// bypass it.
type sysProxy struct {
	proxy      *url.URL
	exceptions []string
}

// Seams for tests.
var (
	goos       = runtime.GOOS
	runCommand = func(name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		hideWindow(cmd)
		return cmd.Output()
	}
	// sysCacheTTL bounds how long a change to the OS settings goes unnoticed.
	sysCacheTTL = 10 * time.Second
)

var sysCache struct {
	sync.Mutex
	key  string
	at   time.Time
	val  *sysProxy
	full bool
}

// resetCache forgets the cached OS proxy.
func resetCache() {
	sysCache.Lock()
	sysCache.full = false
	sysCache.val = nil
	sysCache.Unlock()
}

// system returns the OS proxy, or nil when there is none or it can't be
// read. Only macOS and Windows are consulted; elsewhere desktop sessions
// already hand the proxy to apps as environment variables.
func system() *sysProxy {
	os := goos
	if os != "darwin" && os != "windows" {
		return nil
	}
	key := os + "\x00" + getenvPath()
	sysCache.Lock()
	defer sysCache.Unlock()
	if sysCache.full && sysCache.key == key && time.Since(sysCache.at) < sysCacheTTL {
		return sysCache.val
	}
	var v *sysProxy
	switch os {
	case "darwin":
		v = readDarwin()
	case "windows":
		v = readWindows()
	}
	sysCache.key, sysCache.at, sysCache.val, sysCache.full = key, time.Now(), v, true
	return v
}

func getenvPath() string { return os.Getenv("PATH") }

func readDarwin() *sysProxy {
	out, err := runCommand("scutil", "--proxy")
	if err != nil {
		return nil
	}
	p, ex := parseScutil(string(out))
	if p == nil {
		return nil
	}
	return &sysProxy{proxy: p, exceptions: ex}
}

const winInternetSettings = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func readWindows() *sysProxy {
	out, err := runCommand("reg", "query", winInternetSettings)
	if err != nil {
		return nil
	}
	vals := parseRegQuery(string(out))
	p, ex := parseWindowsProxy(vals["proxyenable"], vals["proxyserver"], vals["proxyoverride"])
	if p == nil {
		return nil
	}
	return &sysProxy{proxy: p, exceptions: ex}
}

// parseScutil reads `scutil --proxy` output. Among the enabled proxies
// HTTPS wins over HTTP, which wins over SOCKS; HTTP and HTTPS proxies are
// HTTP proxies, SOCKS is socks5. ExceptionsList is the bypass list.
func parseScutil(out string) (*url.URL, []string) {
	vals := map[string]string{}
	var exceptions []string
	inExceptions := false
	depth := 0
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == "}" {
			depth--
			if inExceptions && depth == 1 {
				inExceptions = false
			}
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
		opens := strings.HasSuffix(v, "{")
		if inExceptions && depth == 2 && !opens {
			exceptions = append(exceptions, v)
		} else if depth == 1 && !opens {
			vals[k] = v
		}
		if opens {
			depth++
			if depth == 2 && k == "ExceptionsList" {
				inExceptions = true
			}
		}
	}
	for _, c := range []struct{ prefix, scheme string }{{"HTTPS", "http"}, {"HTTP", "http"}, {"SOCKS", "socks5"}} {
		if vals[c.prefix+"Enable"] != "1" {
			continue
		}
		host := strings.TrimSpace(vals[c.prefix+"Proxy"])
		if host == "" {
			continue
		}
		if u := hostPortURL(c.scheme, host, strings.TrimSpace(vals[c.prefix+"Port"])); u != nil {
			return u, exceptions
		}
	}
	return nil, nil
}

// hostPortURL builds scheme://host[:port], or nil if that isn't a valid
// proxy address.
func hostPortURL(scheme, host, port string) *url.URL {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	addr := host
	if port != "" && port != "0" {
		addr += ":" + port
	}
	u, err := settings.ParseProxy(scheme + "://" + addr)
	if err != nil {
		return nil
	}
	return u
}

// parseRegQuery reads `reg query` output into lower-cased value names and
// their data, e.g. "    ProxyServer    REG_SZ    127.0.0.1:7890".
func parseRegQuery(out string) map[string]string {
	vals := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		// The type is the first REG_* field; the name is everything before
		// it (names may contain spaces), the data everything after.
		ti := -1
		for i := 1; i < len(f); i++ {
			if strings.HasPrefix(f[i], "REG_") {
				ti = i
				break
			}
		}
		if ti < 0 {
			continue
		}
		idx := strings.Index(line, " "+f[ti])
		if idx < 0 {
			idx = strings.Index(line, "\t"+f[ti])
		}
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		data := strings.TrimSpace(line[idx+1+len(f[ti]):])
		vals[strings.ToLower(name)] = data
	}
	return vals
}

// parseWindowsProxy turns Internet Options' manual proxy into a URL and an
// exception list. enable is ProxyEnable as reg prints it ("0x1"). server is
// either one host:port, an HTTP proxy, or "http=…;https=…;socks=…", where
// the first of https, http and socks with a value wins.
func parseWindowsProxy(enable, server, override string) (*url.URL, []string) {
	if !regTrue(enable) {
		return nil, nil
	}
	server = strings.TrimSpace(server)
	if server == "" {
		return nil, nil
	}
	var u *url.URL
	if !strings.Contains(server, "=") {
		u = winAddr("http", server)
	} else {
		per := map[string]string{}
		for _, part := range strings.Split(server, ";") {
			k, v, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			k = strings.ToLower(strings.TrimSpace(k))
			if _, seen := per[k]; !seen && strings.TrimSpace(v) != "" {
				per[k] = strings.TrimSpace(v)
			}
		}
		for _, c := range []struct{ key, scheme string }{{"https", "http"}, {"http", "http"}, {"socks", "socks5"}} {
			if v := per[c.key]; v != "" {
				if u = winAddr(c.scheme, v); u != nil {
					break
				}
			}
		}
	}
	if u == nil {
		return nil, nil
	}
	var ex []string
	for _, e := range strings.Split(override, ";") {
		if e = strings.TrimSpace(e); e != "" {
			ex = append(ex, e)
		}
	}
	return u, ex
}

// winAddr reads one proxy address from ProxyServer. A bare host:port uses
// scheme; an address with its own scheme keeps it (socks → socks5), except
// that a SOCKS entry is always socks5.
func winAddr(scheme, addr string) *url.URL {
	addr = strings.TrimSpace(addr)
	if i := strings.Index(addr, "://"); i >= 0 {
		s := strings.ToLower(addr[:i])
		if s == "socks" || s == "socks4" {
			s = "socks5"
		}
		if scheme == "socks5" && s != "socks5h" {
			s = "socks5"
		}
		addr = s + addr[i:]
	} else {
		addr = scheme + "://" + addr
	}
	u, err := settings.ParseProxy(addr)
	if err != nil {
		return nil
	}
	return u
}

func regTrue(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return false
	}
	v = strings.TrimPrefix(v, "0x")
	return strings.Trim(v, "0") != ""
}
