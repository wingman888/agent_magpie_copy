package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A redirect to another host must not carry the custom headers there.
func TestRedirectToOtherHostDropsCustomHeaders(t *testing.T) {
	setupConfig(t)
	var gotB, gotA http.Header
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotB = r.Header.Clone()
		w.Write([]byte("{}"))
	}))
	defer b.Close()
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotA = r.Header.Clone()
		http.Redirect(w, r, b.URL+"/v1/models", http.StatusTemporaryRedirect)
	}))
	defer a.Close()

	p := Provider{ID: "relay", Name: "Relay", Chat: a.URL + "/v1", Key: "k",
		Headers: map[string]string{"X-Org-Id": "acme", "X-Tenant": "t1"}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, a.URL+"/v1/models", nil)
	p.ApplyHeaders(req)
	res, err := p.ClientFor(http.DefaultClient).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if gotA.Get("X-Org-Id") != "acme" {
		t.Errorf("first host got X-Org-Id = %q", gotA.Get("X-Org-Id"))
	}
	if gotB == nil {
		t.Fatal("redirect target was not reached")
	}
	for _, n := range []string{"X-Org-Id", "X-Tenant"} {
		if v := gotB.Values(n); len(v) != 0 {
			t.Errorf("other host got %s = %v", n, v)
		}
	}
}

// A redirect that stays on the same host keeps the custom headers.
func TestSameHostRedirectKeepsCustomHeaders(t *testing.T) {
	setupConfig(t)
	var got http.Header
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, srv.URL+"/end", http.StatusFound)
			return
		}
		got = r.Header.Clone()
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	p := Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "k",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/start", nil)
	p.ApplyHeaders(req)
	res, err := p.ClientFor(http.DefaultClient).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got.Get("X-Org-Id") != "acme" {
		t.Errorf("same-host redirect lost X-Org-Id: %v", got)
	}
}

// The default redirect limit (10) still applies.
func TestRedirectLimitStillApplies(t *testing.T) {
	setupConfig(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
	}))
	defer srv.Close()

	p := Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "k",
		Headers: map[string]string{"X-Org-Id": "acme"}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/loop", nil)
	p.ApplyHeaders(req)
	res, err := p.ClientFor(http.DefaultClient).Do(req)
	if err == nil {
		res.Body.Close()
		t.Fatal("redirect loop was followed without limit")
	}
}
