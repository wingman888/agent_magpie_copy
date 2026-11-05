package gateway

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
)

// The vendor transport must consult netproxy for every request, not the
// environment alone, and must never be left with a nil Proxy (which would
// bypass the proxy rules entirely).
func TestVendorTransportUsesNetproxy(t *testing.T) {
	tr, ok := New().client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("vendor transport is %T, want *http.Transport", New().client.Transport)
	}
	if tr.Proxy == nil {
		t.Fatal("vendor transport has nil Proxy; requests would bypass netproxy")
	}

	for _, raw := range []string{"https://chatgpt.com/backend-api/x", "http://127.0.0.1:3425/", "http://localhost/"} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		want, wantErr := netproxy.For(u)
		got, gotErr := tr.Proxy(&http.Request{URL: u})
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("%s: error mismatch: got %v, want %v", raw, gotErr, wantErr)
		}
		if gotStr, wantStr := urlString(got), urlString(want); gotStr != wantStr {
			t.Errorf("%s: transport proxy %q, netproxy.For %q", raw, gotStr, wantStr)
		}
	}
}

func urlString(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.String()
}
