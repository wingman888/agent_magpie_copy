package provider

import (
	"slices"
	"strings"
	"testing"
)

// groupSetup gives two ready providers, A offering m and B offering vendor/m.
func groupSetup(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "a", Name: "A", Chat: "https://a.test/v1", Key: "sk-a-1111", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "b", Name: "B", Chat: "https://b.test/v1", Key: "sk-b-2222", Models: []string{"vendor/m"}}); err != nil {
		t.Fatal(err)
	}
}

func catalogIDs() []string {
	var out []string
	for _, e := range Catalog() {
		out = append(out, e.ID)
	}
	return out
}

func memberRefs(ms []Member) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Ref)
	}
	return out
}

// Acceptance 1: the discovered group comes after the providers' own models,
// and it is listed with the group id and its members in provider order.
func TestDiscoveredGroupCatalog(t *testing.T) {
	groupSetup(t)
	want := []string{"a/m", "b/vendor/m", "group/auto-m"}
	if got := catalogIDs(); !slices.Equal(got, want) {
		t.Fatalf("catalog %v, want %v", got, want)
	}
	es := Catalog()
	if g := es[2]; g.Group != "auto-m" || g.Provider.ID != groupPrefix {
		t.Fatalf("group entry %+v", g)
	}
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || !g.Auto || !slices.Equal(memberRefs(ms), []string{"a/m", "b/vendor/m"}) {
		t.Fatalf("FindGroup: %+v %v %v", g, memberRefs(ms), ok)
	}
	// resolving the group answers as its first member
	if p, model, ok := Resolve("group/auto-m"); !ok || p.ID != "a" || model != "m" {
		t.Fatalf("Resolve group: %v %q %v", p.ID, model, ok)
	}
}

// Acceptance 3: a discovered group can be hidden, and shown again.
func TestDeleteAndShowDiscoveredGroup(t *testing.T) {
	groupSetup(t)
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/auto-m"); ok {
		t.Fatal("hidden group still resolves")
	}
	if _, _, ok := Resolve("group/auto-m"); ok {
		t.Fatal("hidden group still resolves in Resolve")
	}
	for _, id := range catalogIDs() {
		if strings.HasPrefix(id, "group/") {
			t.Fatalf("hidden group still in catalog: %v", catalogIDs())
		}
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("group/auto-m"); !ok || len(ms) != 2 {
		t.Fatalf("shown group: %v %v", memberRefs(ms), ok)
	}
	// deleting what is not there is an error
	if err := DeleteGroup("nothing-here"); err == nil {
		t.Fatal("deleted a group that does not exist")
	}
}

// Acceptance 4: validation of saved groups and providers.
func TestGroupValidation(t *testing.T) {
	groupSetup(t)
	err := SaveGroup(Group{ID: "empty", Members: nil})
	if err == nil || err.Error() != "组里至少要有一个模型" {
		t.Fatalf("empty group: %v", err)
	}
	// only nested groups: nothing is left, so it is empty too
	if err := SaveGroup(Group{ID: "nest", Members: []string{"group/auto-m"}}); err == nil {
		t.Fatal("group of only a group saved")
	}
	if err := SaveGroup(Group{ID: "Bad_Id", Members: []string{"a/m"}}); err == nil {
		t.Fatal("bad group id saved")
	}
	if err := Save(Provider{ID: "group", Name: "G", Chat: "https://g.test/v1", Key: "sk-g"}); err == nil {
		t.Fatal("provider id group saved")
	}
	if err := Save(Provider{Name: "Group", Chat: "https://g.test/v1", Key: "sk-g"}); err == nil {
		t.Fatal("provider named Group saved")
	}
	// a group inside a group is ignored, the rest is kept
	if err := SaveGroup(Group{ID: "mine", Members: []string{"group/auto-m", "b/vendor/m", "a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := FindGroup("mine")
	if !slices.Equal(g.Members, []string{"b/vendor/m", "a/only-a"}) {
		t.Fatalf("members %v", g.Members)
	}
}

// A user's group comes first in its order, and saving over a discovered
// group makes it the user's; deleting that hides it again.
func TestUserGroupOwnership(t *testing.T) {
	groupSetup(t)
	if err := SaveGroup(Group{ID: "mine", Name: "Mine", Members: []string{"b/vendor/m", "a/m"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/mine")
	if !ok || g.Name != "Mine" || g.Routing != Ordered || g.Auto || !slices.Equal(memberRefs(ms), []string{"b/vendor/m", "a/m"}) {
		t.Fatalf("mine: %+v %v", g, memberRefs(ms))
	}
	if err := SaveGroup(Group{ID: "auto-m", Members: []string{"b/vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	if g, ms, _ := FindGroup("auto-m"); g.Auto || g.Name != "auto-m" || len(ms) != 1 {
		t.Fatalf("converted group: %+v %v", g, memberRefs(ms))
	}
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatal("deleted converted group came back")
	}
	if err := DeleteGroup("mine"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("mine"); ok {
		t.Fatal("deleted group still there")
	}
}

// Unknown routing and affinity values fall back to the defaults; the old
// "usage" name of least-used still means least-used.
func TestRoutingAndAffinityValues(t *testing.T) {
	groupSetup(t)
	if err := SaveGroup(Group{ID: "r", Members: []string{"a/m"}, Routing: "ordered", Affinity: "forever"}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("r"); g.Routing != "" || g.Affinity != "" {
		t.Fatalf("invalid values kept: %+v", g)
	}
	if err := SetAffinity("a", "session"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != "session" {
		t.Fatalf("affinity %q", p.Affinity)
	}
	if err := SetAffinity("a", "bogus"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != "" {
		t.Fatalf("bogus affinity kept: %q", p.Affinity)
	}
	if err := SetRouting("a", "usage"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Routing != LeastUsed || LeastUsed != "least-used" {
		t.Fatalf("routing %q", p.Routing)
	}
	if err := SetAffinity("nope", "turn"); err == nil {
		t.Fatal("affinity set on an unknown provider")
	}
}
