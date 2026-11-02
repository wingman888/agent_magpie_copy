package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestCustomHeadersForwarded(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"c1","model":"m1","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`}
	up := setup(t, provider.Chat, f)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1",
		Headers: map[string]string{"X-Org-Id": "acme", "Authorization": "Token mine"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.head.Get("X-Org-Id") != "acme" || f.head.Get("Authorization") != "Token mine" || len(f.head.Values("Authorization")) != 1 {
		t.Fatalf("upstream headers %v", f.head)
	}
}
