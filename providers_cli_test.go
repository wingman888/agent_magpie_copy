package main

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestApplyPairsHeaders(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{
		"url=https://relay.example.com/v1",
		"header.X-Org-Id=acme",
		"HEADER.x-gateway-auth=tok=with=equals",
	}); err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://relay.example.com/v1" {
		t.Errorf("Chat = %q", p.Chat)
	}
	want := map[string]string{"X-Org-Id": "acme", "x-gateway-auth": "tok=with=equals"}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("Headers[%q] = %q, want %q (all: %v)", k, p.Headers[k], v, p.Headers)
		}
	}
}
