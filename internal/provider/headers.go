package provider

import (
	"crypto/tls"
	"fmt"
	"net/http"
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
	if len(p.customHeaders()) == 0 {
		return c
	}
	return http1Only(c)
}

// h1Clients caches the HTTP/1.1-only twin of each client handed to ClientFor.
var h1Clients sync.Map // *http.Client → *http.Client

func http1Only(c *http.Client) *http.Client {
	if v, ok := h1Clients.Load(c); ok {
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
	h1Clients.Store(c, nc)
	return nc
}

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
