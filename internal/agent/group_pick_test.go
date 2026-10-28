package agent

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestPickerListsRoutingGroupsFirst(t *testing.T) {
	codexHome(t, "", "")
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"other/m1", "fake/m1"}}); err != nil {
		t.Fatal(err)
	}
	opts := viaMagpie("")
	if len(opts) == 0 || opts[0].Group != RoutingGroups {
		t.Fatalf("groups not first: %+v", opts)
	}
	o := opts[0]
	if o.Value != "group/mine" || o.Label != "Mine" || o.Note != "routing group · via magpie" || len(o.Icons) != 2 {
		t.Errorf("option: %+v", o)
	}
	for _, o := range opts[1:] {
		if o.Group == RoutingGroups {
			t.Errorf("group after providers: %+v", o)
		}
	}
	var found bool
	for _, m := range magpieModels() {
		if m.ID == "group/mine" {
			found = true
			if m.Name != "Mine · routing group" {
				t.Errorf("name %q", m.Name)
			}
		}
	}
	if !found {
		t.Error("group missing from models")
	}
}
