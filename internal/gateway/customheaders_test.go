package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A forwarded request carries the custom provider's headers, and one of
// them overrides the key the gateway signed with.
func TestChdrForwardCarriesCustomHeaders(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"x","object":"chat.completion","choices":[]}`}
	setup(t, provider.Chat, f)
	p, err := provider.Find("fake")
	if err != nil {
		t.Fatal(err)
	}
	p.Headers = map[string]string{"X-Org-Id": "acme", "Authorization": "Bearer override"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	post(t, "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`)
	if got := f.head.Get("X-Org-Id"); got != "acme" {
		t.Errorf("X-Org-Id = %q, want acme", got)
	}
	if got := f.head.Get("Authorization"); got != "Bearer override" {
		t.Errorf("Authorization = %q, want the user's value", got)
	}
}
