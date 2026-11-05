package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// Item 1: an id not given comes from the name; a name not given is the id;
// an id holding anything but lowercase letters, digits and dashes is refused.
func TestSaveGroupIDFromNameAndIDRules(t *testing.T) {
	groupSetup(t)
	if err := SaveGroup(Group{Name: "Claude Opus 5.5", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if g, _, ok := FindGroup("claude-opus-5-5"); !ok || g.Name != "Claude Opus 5.5" {
		t.Fatalf("id from name: %+v ok=%v", g, ok)
	}
	if err := SaveGroup(Group{Name: "Mine", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if g, _, ok := FindGroup("mine"); !ok || g.ID != "mine" || g.Name != "Mine" {
		t.Fatalf("mine: %+v ok=%v", g, ok)
	}
	if err := SaveGroup(Group{ID: "solo", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if g, _, ok := FindGroup("solo"); !ok || g.Name != "solo" {
		t.Fatalf("name defaults to id: %+v ok=%v", g, ok)
	}
	if err := SaveGroup(Group{ID: "a--b", Members: []string{"a/m"}}); err != nil {
		t.Fatalf("double dash refused: %v", err)
	}
	for _, bad := range []string{"a_b", "a.b", "a b", "a/b"} {
		if err := SaveGroup(Group{ID: bad, Members: []string{"a/m"}}); err == nil {
			t.Errorf("id %q saved", bad)
		}
	}
}

// Item 2: "ordered" is not a value; unknown routing and affinity fall back.
func TestSaveGroupRoutingFallbacks(t *testing.T) {
	groupSetup(t)
	if err := SaveGroup(Group{ID: "o", Members: []string{"a/m"}, Routing: "ordered"}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("o"); g.Routing != "" {
		t.Fatalf("ordered kept as %q", g.Routing)
	}
	if err := SaveGroup(Group{ID: "o", Members: []string{"a/m"}, Routing: "order", Affinity: "forever"}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("o"); g.Routing != Ordered || g.Affinity != "" {
		t.Fatalf("order/forever: %+v", g)
	}
}

// Item 3: a group with no member left, empty or only nested groups, is refused
// with the message the contract names.
func TestSaveGroupNeedsAMember(t *testing.T) {
	groupSetup(t)
	err := SaveGroup(Group{ID: "x", Members: []string{" ", ""}})
	if err == nil || !strings.Contains(err.Error(), "组里至少要有一个模型") {
		t.Fatalf("empty members: %v", err)
	}
	err = SaveGroup(Group{ID: "x", Members: []string{"group/auto-m"}})
	if err == nil || !strings.Contains(err.Error(), "组里至少要有一个模型") {
		t.Fatalf("only nested group: %v", err)
	}
}

// Item 4: the provider id "group" is refused by Save and by SetAffinity.
func TestProviderIDGroupRefused(t *testing.T) {
	groupSetup(t)
	if err := Save(Provider{ID: "group", Name: "G", Chat: "https://g.test/v1", Key: "sk-g"}); err == nil {
		t.Fatal("Save accepted provider id group")
	}
	if err := SetAffinity("group", "session"); err == nil {
		t.Fatal("SetAffinity accepted provider id group")
	}
}

// Item 5: deleting an unknown group errors; a discovered group is hidden and
// ShowGroup brings it back.
func TestDeleteAndShowGroup(t *testing.T) {
	groupSetup(t)
	if err := DeleteGroup("no-such-group"); err == nil {
		t.Fatal("deleting an unknown group did not error")
	}
	if _, _, ok := FindGroup("auto-m"); !ok {
		t.Fatal("auto-m not discovered")
	}
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatal("hidden auto group still found")
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); !ok {
		t.Fatal("shown auto group not back")
	}
}

// Item 14: the config keys are "routing" and "affinity" on providers and
// groups alike; an absent one is the automatic default.
func TestRoutingAffinityJSONKeys(t *testing.T) {
	b, err := json.Marshal(Provider{ID: "a", Routing: Rotate, Affinity: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !strings.Contains(s, `"routing":"rotate"`) || !strings.Contains(s, `"affinity":"session"`) {
		t.Fatalf("provider json %s", s)
	}
	b, err = json.Marshal(Group{ID: "g", Members: []string{"a/m"}, Routing: LeastUsed, Affinity: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !strings.Contains(s, `"routing":"least-used"`) || !strings.Contains(s, `"affinity":"off"`) {
		t.Fatalf("group json %s", s)
	}
	var p Provider
	if err := json.Unmarshal([]byte(`{"id":"old"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Routing != "" || p.Affinity != "" {
		t.Fatalf("old config not automatic: %+v", p)
	}
}
