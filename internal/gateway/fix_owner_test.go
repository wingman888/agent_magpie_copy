package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// TestFixGroupOwnedByMagpie: a routing group has no provider, so it is
// owned by magpie rather than by an empty provider id.
func TestFixGroupOwnedByMagpie(t *testing.T) {
	grp := provider.Entry{ID: "group/fix", Model: "group/fix", Name: "Fix", Group: &provider.Group{ID: "fix"}}
	if got := modelObject(grp)["owned_by"]; got != "magpie" {
		t.Fatalf("group owned_by %v, want magpie", got)
	}
	plain := provider.Entry{ID: "fixp/m", Model: "m", Name: "M", Provider: provider.Provider{ID: "fixp"}}
	if got := modelObject(plain)["owned_by"]; got != "fixp" {
		t.Fatalf("model owned_by %v, want fixp", got)
	}
}
