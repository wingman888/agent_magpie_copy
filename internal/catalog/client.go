package catalog

import (
	"crypto/tls"
	"net/http"
	"sync"
)

// h1Clients caches the HTTP/1.1-only copy of each base client, so requests
// with custom headers share one connection pool rather than opening a new one
// per request.
var h1Clients sync.Map // *http.Client -> *http.Client

// HeaderClient returns the client to send a request with. Go's HTTP/2 client
// lowercases header names on the wire, which would break the rule that a
// custom header goes out under the name the user typed. So when headers is
// non-empty, the result is a copy of base that speaks HTTP/1.1 only, keeping
// base's proxy, timeouts and pool settings. With no headers, or when base has
// no *http.Transport to copy, base itself is returned.
func HeaderClient(base *http.Client, headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return base
	}
	if c, ok := h1Clients.Load(base); ok {
		return c.(*http.Client)
	}
	var t *http.Transport
	var ok bool
	if base.Transport == nil {
		t, ok = http.DefaultTransport.(*http.Transport)
	} else {
		t, ok = base.Transport.(*http.Transport)
	}
	if !ok {
		return base
	}
	nt := t.Clone()
	nt.ForceAttemptHTTP2 = false
	// A non-nil, empty TLSNextProto stops the transport from enabling HTTP/2
	// on its own, so no "h2" is offered in ALPN.
	nt.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	c := *base
	c.Transport = nt
	actual, _ := h1Clients.LoadOrStore(base, &c)
	return actual.(*http.Client)
}
