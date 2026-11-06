package agent

import (
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The picker lists the routing groups first, in a section of their own,
// with the icons of their members' providers, each once.
func TestRgxPickerGroupsFirstWithIconsOnce(t *testing.T) {
	a := provider.Provider{ID: "a", Name: "Alpha", Icon: "anthropic"}
	b := provider.Provider{ID: "b", Name: "Beta", Icon: "openai"}
	c := provider.Provider{ID: "c", Name: "Gamma", Icon: "anthropic"}
	entries := []provider.Entry{
		{ID: "a/m", Model: "m", Name: "M", Provider: a},
		{ID: "b/vendor/m", Model: "vendor/m", Name: "Vendor M", Provider: b},
		{ID: "group/auto-m", Name: "Claude M", Kind: provider.KindGroup, Auto: true,
			Members: []string{"a/m", "b/vendor/m", "c/m"}},
	}
	icons := map[string]string{"a": a.Icon, "b": b.Icon, "c": c.Icon}
	opts := pickerOptions(entries, "magpie/", func(pid string) string { return icons[pid] })

	g := opts[0]
	if g.Group != "Routing groups" || g.Value != "magpie/group/auto-m" || g.Label != "Claude M" {
		t.Fatalf("first option should be the group: %+v", g)
	}
	if g.Note != "routing group · via magpie" {
		t.Fatalf("note: %q", g.Note)
	}
	if want := []string{"anthropic", "openai"}; !reflect.DeepEqual(g.Icons, want) {
		t.Fatalf("icons %v, want %v", g.Icons, want)
	}
	for _, o := range opts[1:] {
		if o.Group == "Routing groups" {
			t.Fatalf("a provider model landed in the groups section: %+v", o)
		}
	}
	if len(opts) != 3 || opts[1].Group != "Alpha" || opts[2].Group != "Beta" {
		t.Fatalf("provider sections out of order: %+v", opts)
	}
}
