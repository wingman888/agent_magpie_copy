package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A custom provider's own headers go on the request magpie forwards, and
// they override the defaults magpie would otherwise send.
func TestHdrtForwardSendsCustomHeaders(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"msg","type":"message","content":[]}`}
	setup(t, provider.Anthropic, f)
	p, err := provider.Find("fake")
	if err != nil {
		t.Fatal(err)
	}
	p.Headers = map[string]string{"X-Org-Id": "acme", "anthropic-version": "2099-01-01"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/messages", `{"model":"fake/m1","max_tokens":16,"messages":[]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if got := f.head.Get("X-Org-Id"); got != "acme" {
		t.Errorf("X-Org-Id = %q, want acme", got)
	}
	if got := f.head.Values("anthropic-version"); len(got) != 1 || got[0] != "2099-01-01" {
		t.Errorf("anthropic-version = %v, want the custom value only", got)
	}
	if got := f.head.Get("x-api-key"); got == "" && f.head.Get("Authorization") == "" {
		t.Errorf("auth header missing from forwarded request")
	}
}
