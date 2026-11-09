package agent

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestS4GroupOptionIconsOncePerProvider(t *testing.T) {
	e := provider.Entry{ID: "group/mine", Name: "Mine", Group: &provider.Group{ID: "mine"}}
	ms := []provider.Member{
		{Provider: provider.Provider{ID: "a", Icon: "alpha"}, Model: "m"},
		{Provider: provider.Provider{ID: "a", Icon: "alpha"}, Model: "n"},
		{Provider: provider.Provider{ID: "b", Icon: "beta"}, Model: "m"},
	}
	o := groupOption("magpie/", e, ms)
	if o.Value != "magpie/group/mine" || o.Group != "Routing groups" || o.Note != "routing group · via magpie" {
		t.Fatalf("option: %+v", o)
	}
	if len(o.Icons) != 2 || o.Icons[0] != "alpha" || o.Icons[1] != "beta" || o.Icon != "alpha" {
		t.Fatalf("icons: %+v", o.Icons)
	}
}

func TestS4IsMagpieGroupRef(t *testing.T) {
	if isMagpie("group/no-such-group-s4") {
		t.Fatal("an unknown group is not a catalog reference")
	}
}
