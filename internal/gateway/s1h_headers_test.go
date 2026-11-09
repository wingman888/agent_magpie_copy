package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestS1HForwardCarriesTheProvidersOwnHeaders(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"x","choices":[]}`}
	setup(t, provider.Chat, f)
	p, err := provider.Find("fake")
	if err != nil {
		t.Fatal(err)
	}
	p.Headers = map[string]string{"X-Org-Id": "acme"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	post(t, "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`)
	if f.path == "" {
		t.Fatal("the upstream was never called")
	}
	if got := f.head.Get("X-Org-Id"); got != "acme" {
		t.Errorf("X-Org-Id upstream: %q", got)
	}
}
