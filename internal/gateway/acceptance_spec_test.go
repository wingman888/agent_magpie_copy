package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// accSpecRelay saves a provider "relay" with keys k1 (named personal) and k2
// (named team), both enabled, pointed at the given upstream.
func accSpecRelay(t *testing.T, up *httptest.Server) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{
		ID: "relay", Name: "Relay", Key: "k1", KeyName: "personal", Models: []string{"m1"},
		Chat: up.URL + "/v1", Keys: []provider.ExtraKey{{Name: "team", Key: "k2"}},
	}); err != nil {
		t.Fatal(err)
	}
}

// Two enabled keys: the first is limited, the second answers within the same
// request. The record names the failed key, the answering provider is relay,
// and the next request goes straight to the second key.
func TestAccSpecKeyRelayRecordsFailedKeyName(t *testing.T) {
	b := &byKey{code: map[string]int{"k1": 429}}
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	accSpecRelay(t, up)

	srv := New()
	h := srv.Handler()
	if code := chatVia(h); code != 200 {
		t.Fatalf("first request status %d", code)
	}
	if got := join(b.took()); got != "k1,k2" {
		t.Fatalf("first request keys = %s, want k1,k2", got)
	}
	if code := chatVia(h); code != 200 {
		t.Fatalf("second request status %d", code)
	}
	if got := join(b.took()); got != "k2" {
		t.Fatalf("second request keys = %s, want k2 only", got)
	}

	calls := srv.Recent() // newest first
	if len(calls) != 2 {
		t.Fatalf("%d calls recorded, want 2", len(calls))
	}
	second, first := calls[0], calls[1]
	if first.Provider != "relay" || first.Status != 200 {
		t.Fatalf("first call = %+v", first)
	}
	if !strings.Contains(first.Fallback, "relay") || !strings.Contains(first.Fallback, "personal") {
		t.Fatalf("first call Fallback = %q, want the failed key named", first.Fallback)
	}
	if second.Provider != "relay" || second.Fallback != "" {
		t.Fatalf("second call = %+v, want relay with no fallback", second)
	}
}

// A disabled key never reaches the upstream.
func TestAccSpecDisabledKeyNeverSent(t *testing.T) {
	b := &byKey{code: map[string]int{}}
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	accSpecRelay(t, up)
	if err := provider.SetKeyOn("relay", provider.KeyRef("k2"), false); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	for i := 0; i < 2; i++ {
		if code := chatVia(h); code != 200 {
			t.Fatalf("request %d status %d", i, code)
		}
	}
	for _, k := range b.took() {
		if k == "k2" {
			t.Fatal("disabled key k2 was sent upstream")
		}
	}
}

// Retry statuses move the request to the fallback model; the fallback's own
// name is sent and the call records the fallback provider as the answer.
func TestAccSpecFallbackStatuses(t *testing.T) {
	cases := []struct {
		name string
		code int
		msg  string
	}{
		{"429 rate limit", 429, `{"error":{"message":"rate limit exceeded"}}`},
		{"402 payment", 402, `{"error":{"message":"payment required"}}`},
		{"529 overloaded", 529, `{"error":{"message":"overloaded"}}`},
		{"403 quota used up", 403, `{"error":{"message":"quota exhausted for this key"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prim := &scripted{codes: map[string]int{"k1": c.code}, body: map[string]string{"k1": c.msg}}
			back := &scripted{}
			fallbackSetup(t, prim, back)
			srv := New()
			rec := postTo(srv.Handler(), "/v1/chat/completions", primChat)
			if rec.Code != 200 {
				t.Fatalf("client saw status %d: %s", rec.Code, rec.Body.String())
			}
			bm, bk := back.took()
			if join(bm) != "m2" || join(bk) != "k2" {
				t.Fatalf("fallback sent models %v keys %v; want m2 with k2", bm, bk)
			}
			calls := srv.Recent()
			if len(calls) == 0 || calls[0].Provider != "back" || !strings.Contains(calls[0].Fallback, "prim") {
				t.Fatalf("call = %+v", calls)
			}
		})
	}
}

// A 403 that is not about quota is not retried; a 400 is returned as is.
func TestAccSpecNonRetryableReturnedAsIs(t *testing.T) {
	cases := []struct {
		name string
		code int
		msg  string
		want string
	}{
		{"400 bad request", 400, `{"error":{"message":"bad param"}}`, "bad param"},
		{"403 forbidden model", 403, `{"error":{"message":"model not allowed for this key"}}`, "model not allowed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prim := &scripted{codes: map[string]int{"k1": c.code}, body: map[string]string{"k1": c.msg}}
			back := &scripted{}
			fallbackSetup(t, prim, back)
			rec := postTo(New().Handler(), "/v1/chat/completions", primChat)
			if rec.Code != c.code {
				t.Fatalf("client saw status %d, want %d", rec.Code, c.code)
			}
			if !strings.Contains(rec.Body.String(), c.want) {
				t.Fatalf("body %q does not carry upstream's message", rec.Body.String())
			}
			if bm, _ := back.took(); len(bm) != 0 {
				t.Fatalf("fallback was reached with %v", bm)
			}
		})
	}
}

// When every candidate fails, the client gets the last candidate's error.
func TestAccSpecLastCandidateErrorReturned(t *testing.T) {
	prim := &scripted{codes: map[string]int{"k1": 429}}
	back := &scripted{codes: map[string]int{"k2": 503}, body: map[string]string{"k2": `{"error":{"message":"busy-from-back"}}`}}
	fallbackSetup(t, prim, back)
	rec := postTo(New().Handler(), "/v1/chat/completions", primChat)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "busy-from-back") {
		t.Fatalf("client saw %d %q, want the fallback's 503", rec.Code, rec.Body.String())
	}
}
