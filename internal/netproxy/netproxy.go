// Package netproxy decides which proxy, if any, a request from magpie goes
// through. Order: magpie's own setting, then the environment (HTTPS_PROXY and
// friends, NO_PROXY), then the operating system's proxy settings. Loopback
// addresses never use a proxy.
package netproxy

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// sysProxy is what the operating system says about proxies.
type sysProxy struct {
	HTTP, HTTPS, SOCKS string   // host:port, "" when unset
	Except             []string // bypass list, as the OS spells it
	Local              bool     // bypass dotless host names ("local names")
}

// systemSettings reads the OS proxy configuration; a var so tests can replace it.
// The per-platform readers live in system_*.go.
var systemSettings = readSystem

func init() {
	// Every default-client request magpie makes (catalog, provider models,
	// updates) follows the same rules as the gateway.
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = ForRequest
	}
}

// ForRequest is the Transport.Proxy hook.
func ForRequest(r *http.Request) (*url.URL, error) { return For(r.URL) }

// For returns the proxy a request to u should use, or nil to connect directly.
func For(u *url.URL) (*url.URL, error) {
	if u == nil || u.Host == "" {
		return nil, nil
	}
	host := strings.ToLower(u.Hostname())
	if loopback(host) {
		return nil, nil
	}
	if s := settings.Load().Proxy; s != "" {
		if s == "direct" {
			return nil, nil
		}
		return parse(s)
	}
	if raw, ok := envProxy(strings.ToLower(u.Scheme)); ok {
		if bypass(envNoProxy(), host, u.Port(), false) {
			return nil, nil
		}
		return parse(raw)
	}
	sys := systemSettings()
	if bypass(sys.Except, host, u.Port(), sys.Local) {
		return nil, nil
	}
	switch {
	case sys.HTTPS != "":
		return parse("http://" + sys.HTTPS)
	case sys.HTTP != "":
		return parse("http://" + sys.HTTP)
	case sys.SOCKS != "":
		return parse("socks5://" + sys.SOCKS)
	}
	return nil, nil
}

// parse reads a proxy address; one without a scheme is HTTP, as Go's own
// environment handling treats it.
func parse(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return url.Parse(raw)
}

// envProxy returns the proxy variable for a request scheme. ALL_PROXY covers
// whatever the specific variable does not.
func envProxy(scheme string) (string, bool) {
	var names []string
	switch scheme {
	case "https", "wss":
		names = []string{"HTTPS_PROXY", "https_proxy"}
	case "http", "ws":
		names = []string{"HTTP_PROXY", "http_proxy"}
	}
	names = append(names, "ALL_PROXY", "all_proxy")
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v, true
		}
	}
	return "", false
}

// envNoProxy is the NO_PROXY list, comma-separated.
func envNoProxy() []string {
	v := os.Getenv("NO_PROXY")
	if v == "" {
		v = os.Getenv("no_proxy")
	}
	return strings.Split(v, ",")
}

// loopback is true for localhost and loopback IPs; those never use a proxy.
func loopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// bypass reports whether host matches an exception list. Entries may be "*"
// (everything), wildcards ("*.local", "192.168.*"), a domain suffix (".corp",
// or "corp" for itself and its subdomains), an IP, or a CIDR. "host:port"
// entries only match that port. local bypasses dotless names.
func bypass(list []string, host, port string, local bool) bool {
	if local && !strings.Contains(host, ".") && net.ParseIP(host) == nil {
		return true
	}
	ip := net.ParseIP(host)
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == "*" {
			return true
		}
		if h, p, ok := strings.Cut(e, ":"); ok && !strings.Contains(e, "/") && net.ParseIP(e) == nil {
			if p != port {
				continue
			}
			e = h
		}
		switch {
		case strings.Contains(e, "/"):
			if _, n, err := net.ParseCIDR(padCIDR(e)); err == nil && ip != nil && n.Contains(ip) {
				return true
			}
		case net.ParseIP(e) != nil:
			if ip != nil && ip.Equal(net.ParseIP(e)) {
				return true
			}
		case strings.Contains(e, "*"):
			if ok, _ := path.Match(e, host); ok {
				return true
			}
		default:
			d := strings.TrimPrefix(e, ".")
			if host == d || strings.HasSuffix(host, "."+d) {
				return true
			}
		}
	}
	return false
}

// padCIDR completes the short CIDR form macOS uses ("169.254/16").
func padCIDR(s string) string {
	ip, bits, _ := strings.Cut(s, "/")
	for strings.Count(ip, ".") < 3 {
		ip += ".0"
	}
	return ip + "/" + bits
}
