package main

import (
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestChdrApplyPairsHeaders(t *testing.T) {
	var p provider.Provider
	err := applyPairs(&p, []string{
		"url=https://relay.example/v1",
		"HEADER.X-ORG-id =  padded  ",
		"header.X-Org-Id=acme",
		"Header.=dropped",
		"header.   =dropped too",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-ORG-id": "padded", "X-Org-Id": "acme"}
	if !reflect.DeepEqual(p.Headers, want) {
		t.Fatalf("headers = %v, want %v", p.Headers, want)
	}
	if p.Chat != "https://relay.example/v1" {
		t.Errorf("url pair was not applied: %q", p.Chat)
	}
}

func TestChdrApplyPairsHeaderNeedsEquals(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{"header.X-Foo"}); err == nil {
		t.Fatal("expected an error for a header pair without =")
	}
}
