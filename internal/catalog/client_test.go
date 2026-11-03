package catalog

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The HTTP/2 client lowercases header names on the wire, so a request that
// carries custom headers must go over HTTP/1.1, where the name is sent as
// typed. httptest canonicalizes names on the server side, so the protocol the
// server saw is what this test can check.
func TestHeaderClientSpeaksHTTP1OnlyWithHeaders(t *testing.T) {
	type seenReq struct {
		proto int
		org   string
	}
	seen := make(chan seenReq, 4)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- seenReq{proto: r.ProtoMajor, org: r.Header.Get("X-ORG-id")}
		io.WriteString(w, "ok")
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	base := srv.Client()

	// No custom headers: the base client is returned unchanged and still negotiates HTTP/2.
	if HeaderClient(base, nil) != base || HeaderClient(base, map[string]string{}) != base {
		t.Fatal("HeaderClient without headers must return the base client")
	}
	res, err := base.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := <-seen; got.proto != 2 {
		t.Fatalf("without custom headers: want HTTP/2, got HTTP/%d", got.proto)
	}

	// Custom headers: an HTTP/1.1-only copy is used, and the header arrives.
	c := HeaderClient(base, map[string]string{"X-ORG-id": "acme"})
	if c == base {
		t.Fatal("HeaderClient with headers must return a different client")
	}
	if again := HeaderClient(base, map[string]string{"X-ORG-id": "acme"}); again != c {
		t.Fatal("HeaderClient should reuse the same client for the same base")
	}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	SetHeaders(req.Header, map[string]string{"X-ORG-id": "acme"})
	res, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := <-seen; got.proto != 1 || got.org != "acme" {
		t.Fatalf("with custom headers: want HTTP/1.1 and X-ORG-id=acme, got HTTP/%d %q", got.proto, got.org)
	}
}
