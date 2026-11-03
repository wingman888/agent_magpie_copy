package main

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// `header.<name>=value` keeps the name's case, matches the prefix in any case,
// and a later pair for the same name (any case) replaces the earlier one.
func TestApplyPairsHeaders(t *testing.T) {
	var p provider.Provider
	err := applyPairs(&p, []string{
		"url=https://relay.example.com/v1",
		"header.X-Org-Id=acme",
		"HEADER.X-Gateway-Token=t0k",
		"header.x-org-id=beta",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 2 {
		t.Fatalf("headers: %#v", p.Headers)
	}
	if p.Headers["x-org-id"] != "beta" {
		t.Errorf("latest value should win: %#v", p.Headers)
	}
	if _, ok := p.Headers["X-Org-Id"]; ok {
		t.Errorf("older spelling kept: %#v", p.Headers)
	}
	if p.Headers["X-Gateway-Token"] != "t0k" {
		t.Errorf("name case not kept: %#v", p.Headers)
	}
}

func TestApplyPairsRejectsEmptyHeaderName(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{"header.=x"}); err == nil {
		t.Fatal("empty header name accepted")
	}
}
