package main

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestHeaderNamesClashIgnoringCase(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{"header.X-Org-Id=acme", "header.x-org-id=other"}); err == nil {
		t.Fatal("a second header that differs only in case was accepted")
	}
	var q provider.Provider
	if err := applyPairs(&q, []string{"header.X-Org-Id=acme", "header.X-Gateway=gw"}); err != nil {
		t.Fatalf("distinct headers refused: %v", err)
	}
	if q.Headers["X-Org-Id"] != "acme" || q.Headers["X-Gateway"] != "gw" {
		t.Fatalf("headers = %#v", q.Headers)
	}
}
