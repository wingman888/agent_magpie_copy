package provider

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Custom headers are extra HTTP headers a custom (URL + key) provider needs
// on every request: an org id, a tenant tag, a private auth header. They are
// applied after the auth headers, so a name that clashes with one of them
// (Authorization, say) carries the user's value.

// customHeaders is what magpie adds to requests for this provider. Presets
// and signed-in agents never send any.
func (p Provider) customHeaders() map[string]string {
	if p.Preset != "" || p.Account != nil {
		return nil
	}
	return p.Headers
}

// ApplyHeaders sets the provider's custom headers on req, replacing any
// header the request already carries under the same name, ignoring case.
// Names keep the case the user typed: req.Header is written as given, and
// the HTTP/1.1 client sends it as given (see ClientFor).
func (p Provider) ApplyHeaders(req *http.Request) {
	hs := p.customHeaders()
	if len(hs) == 0 {
		return
	}
	for k := range req.Header {
		for name := range hs {
			if strings.EqualFold(k, name) {
				delete(req.Header, k)
				break
			}
		}
	}
	for name, v := range hs {
		req.Header[name] = []string{v}
	}
}

// ClientFor returns the client a request to this provider should use. A
// provider with custom headers gets an HTTP/1.1-only copy of c: HTTP/2
// lowercases header names on the wire, which would undo the case the user
// typed. Other providers keep c.
func (p Provider) ClientFor(c *http.Client) *http.Client {
	hs := p.customHeaders()
	if len(hs) == 0 {
		return c
	}
	names := make([]string, 0, len(hs))
	for n := range hs {
		names = append(names, n)
	}
	sort.Strings(names)
	return http1Only(c, names)
}

// h1Clients caches the HTTP/1.1-only twin of each client handed to ClientFor.
var h1Clients sync.Map // h1Key → *http.Client

type h1Key struct {
	c     *http.Client
	names string // the custom header names the redirect guard drops
}

func http1Only(c *http.Client, names []string) *http.Client {
	key := h1Key{c: c, names: strings.Join(names, "\n")}
	if v, ok := h1Clients.Load(key); ok {
		return v.(*http.Client)
	}
	t, ok := c.Transport.(*http.Transport)
	if c.Transport == nil {
		t, ok = http.DefaultTransport.(*http.Transport)
	}
	if !ok {
		return c
	}
	nt := t.Clone()
	nt.ForceAttemptHTTP2 = false
	// A non-nil empty map turns HTTP/2 off for this transport.
	nt.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	nc := &http.Client{Transport: nt, Timeout: c.Timeout, CheckRedirect: c.CheckRedirect, Jar: c.Jar}
	nc = withRedirectGuard(nc, names)
	h1Clients.Store(key, nc)
	return nc
}

// withRedirectGuard returns a copy of c whose redirects keep the original
// CheckRedirect rules, and also drop the custom headers named in names when
// a redirect leaves the host the request was first sent to. Those headers
// carry the user's secrets; another host must not see them.
func withRedirectGuard(c *http.Client, names []string) *http.Client {
	prev := c.CheckRedirect
	nc := *c
	nc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if prev != nil {
			if err := prev(req, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if len(via) == 0 || strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			return nil
		}
		for k := range req.Header {
			for _, n := range names {
				if strings.EqualFold(k, n) {
					delete(req.Header, k)
					break
				}
			}
		}
		return nil
	}
	return &nc
}

// reservedHeaders are names the Go client sets itself from the request; a
// custom value for one of them would be ignored or would break the framing.
var reservedHeaders = []string{"Host", "Content-Length", "Transfer-Encoding"}

// normalizeHeaders trims names and values, and drops entries with no name.
func normalizeHeaders(hs map[string]string) map[string]string {
	var out map[string]string
	for name, v := range hs {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[name] = strings.TrimSpace(v)
	}
	return out
}

// checkHeaders refuses names that cannot be sent as a header and values
// that would break the request line apart.
func checkHeaders(hs map[string]string) error {
	for name, v := range hs {
		if !isToken(name) {
			return fmt.Errorf("header name %q is not valid", name)
		}
		for _, r := range reservedHeaders {
			if strings.EqualFold(name, r) {
				return fmt.Errorf("header %s is set by the HTTP client and cannot be customized", r)
			}
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("header %s has a value with a line break", name)
		}
	}
	return nil
}

// isToken reports whether s is a valid HTTP header field name (RFC 9110 token).
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}
