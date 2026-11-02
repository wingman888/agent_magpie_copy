// Package netproxy decides which proxy, if any, magpie's own requests to
// vendors go through. The order is:
//
//  1. the proxy setting: "direct" means none, anything else is the proxy;
//  2. the environment (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY, NO_PROXY and
//     their lower-case forms), as Go's standard library reads it;
//  3. the operating system's proxy (macOS network settings, Windows
//     Internet Options), which an app launched from the Dock or the Start
//     menu has no environment for;
//  4. none.
//
// Loopback hosts are always reached directly, whatever the above says.
package netproxy

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// Sources Describe reports.
const (
	SourceSettings    = "settings"
	SourceOff         = "off"
	SourceEnvironment = "environment"
	SourceSystem      = "system"
	SourceNone        = "none"
)

// sample is the vendor Describe asks about: a typical HTTPS endpoint.
var sample = &url.URL{Scheme: "https", Host: "chatgpt.com", Path: "/"}

// Install makes http.DefaultTransport, and so http.DefaultClient and
// http.Get, follow the same rules. Call it once at startup; calling it again
// is harmless.
func Install() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = Request
	}
}

// Request is For for an http.Transport's Proxy field.
func Request(r *http.Request) (*url.URL, error) {
	if r == nil {
		return nil, nil
	}
	return For(r.URL)
}

// For returns the proxy to reach u through, or nil for a direct connection.
// A proxy setting that cannot be parsed is an error rather than a silent
// direct connection.
func For(u *url.URL) (*url.URL, error) {
	if u == nil || IsLoopback(u.Hostname()) {
		return nil, nil
	}
	p, _, err := resolve(u)
	return p, err
}

// Describe reports the proxy requests to vendors take right now and where
// it comes from: settings, off, environment, system or none. The proxy is
// "" for off and none.
func Describe() (proxy, source string) {
	p, source, err := resolve(sample)
	if err != nil {
		// a broken setting: show what is saved, the requests will fail
		return settings.Load().Proxy, SourceSettings
	}
	if p == nil {
		if source != SourceOff {
			source = SourceNone
		}
		return "", source
	}
	return p.String(), source
}

// resolve walks the order for u, loopback aside. A nil proxy with source
// environment or system means that level decided on a direct connection.
func resolve(u *url.URL) (*url.URL, string, error) {
	s := strings.TrimSpace(settings.Load().Proxy)
	switch {
	case strings.EqualFold(s, settings.ProxyDirect):
		return nil, SourceOff, nil
	case s != "":
		p, err := settings.ParseProxy(s)
		if err != nil {
			return nil, SourceSettings, fmt.Errorf("proxy setting: %w", err)
		}
		return p, SourceSettings, nil
	}
	if env := fromEnvironment(); env.set() {
		p, err := env.proxyFor(u)
		return p, SourceEnvironment, err
	}
	if sys := systemProxyFunc(); sys.URL != nil {
		if bypassed(u.Hostname(), sys.Exceptions) {
			return nil, SourceSystem, nil
		}
		return sys.URL, SourceSystem, nil
	}
	return nil, SourceNone, nil
}

// IsLoopback reports whether host is this machine: localhost, a name under
// .localhost, or a loopback IP. host may carry a port.
func IsLoopback(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if host == "" {
		return false
	}
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if i := strings.IndexByte(host, '%'); i >= 0 { // fe80::1%lo0
		host = host[:i]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
