package provider

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// groupHome points magpie's config and cache at empty directories, so the
// tests see no providers of the machine they run on.
func f09GroupHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// vendor saves a plain provider with a key, and the models its list offers.
func f09Vendor(t *testing.T, id string, models ...catalog.Model) {
	t.Helper()
	base := "https://" + id + ".test/v1"
	if err := Save(Provider{ID: id, Name: strings.ToUpper(id), Chat: base, Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(id, base, models); err != nil {
		t.Fatal(err)
	}
}

func f09EntryIDs(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func f09Entry(t *testing.T, id string) Entry {
	t.Helper()
	for _, e := range Catalog() {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("catalog has no %s: %v", id, f09EntryIDs(Catalog()))
	return Entry{}
}

func f09Members(ms ...string) []Member {
	var out []Member
	for _, s := range ms {
		m, err := ParseMember(s)
		if err != nil {
			panic(err)
		}
		out = append(out, m)
	}
	return out
}

func TestF09AutoGroupFromTwoVendors(t *testing.T) {
	f09GroupHome(t)
	f09Vendor(t, "a", catalog.Model{ID: "m", Efforts: []string{"low", "high"}}, catalog.Model{ID: "only-a"})
	f09Vendor(t, "b", catalog.Model{ID: "vendor/m", Name: "Vendor M", Efforts: []string{"high", "max"}})

	es := Catalog()
	ids := f09EntryIDs(es)
	gi := slices.Index(ids, "group/auto-m")
	if gi < 0 {
		t.Fatalf("no auto group: %v", ids)
	}
	// the routing groups come after every provider's models
	for i, e := range es {
		if e.Group == nil && i > gi {
			t.Fatalf("provider model %s after routing groups: %v", e.ID, ids)
		}
	}
	e := f09Entry(t, "group/auto-m")
	if e.Name != "Vendor M" {
		t.Errorf("name %q, want the one that has a display name", e.Name)
	}
	if !slices.Equal(e.Efforts, []string{"high"}) {
		t.Errorf("efforts %v, want only what every member takes", e.Efforts)
	}
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || !slices.Equal(ms, f09Members("a/m", "b/vendor/m")) {
		t.Fatalf("FindGroup: %v %v", ms, ok)
	}
	if g.ID != "auto-m" || len(g.Members) != 2 {
		t.Errorf("group %+v", g)
	}
	if _, _, ok := Resolve("group/auto-m"); ok {
		t.Errorf("Resolve must not turn a group into a provider")
	}
}

func TestF09AutoGroupNormalizedNames(t *testing.T) {
	f09GroupHome(t)
	f09Vendor(t, "a", catalog.Model{ID: "claude-opus-5-5"})
	f09Vendor(t, "b", catalog.Model{ID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5"})
	f09Vendor(t, "c", catalog.Model{ID: "Claude-Opus-5.5-20260801"})
	f09Vendor(t, "d", catalog.Model{ID: "claude-opus-5-5:batch"}, catalog.Model{ID: "v1.beta"})
	f09Vendor(t, "e", catalog.Model{ID: "claude-opus-5-5:batch"}, catalog.Model{ID: "v1.beta"})

	g, ms, ok := FindGroup("auto-claude-opus-5-5")
	if !ok || len(ms) != 3 || g.Name != "Claude Opus 5.5" {
		t.Fatalf("auto-claude-opus-5-5: %+v %v %v", g, ms, ok)
	}
	if !slices.Contains(ms, Member{Provider: "c", Model: "Claude-Opus-5.5-20260801"}) {
		t.Errorf("dated id not grouped: %v", ms)
	}

	batch := autoID("claude-opus-5-5:batch")
	if batch == "auto-claude-opus-5-5" {
		t.Fatalf("batch variant shares the plain model's id")
	}
	if _, ms, ok := FindGroup(batch); !ok || !slices.Equal(ms, f09Members("d/claude-opus-5-5:batch", "e/claude-opus-5-5:batch")) {
		t.Errorf("batch variant: %v %v", ms, ok)
	}

	beta := autoID("v1.beta")
	if !strings.HasPrefix(beta, "auto-v1-beta-") {
		t.Errorf("v1.beta id %q", beta)
	}
	if _, ms, ok := FindGroup(beta); !ok || !slices.Equal(ms, f09Members("d/v1.beta", "e/v1.beta")) {
		t.Errorf("v1.beta: %v %v", ms, ok)
	}
}

func TestF09DeleteAndShowAutoGroup(t *testing.T) {
	f09GroupHome(t)
	f09Vendor(t, "a", catalog.Model{ID: "m"})
	f09Vendor(t, "b", catalog.Model{ID: "vendor/m"})

	if err := DeleteGroup("group/auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatalf("hidden group still usable")
	}
	if slices.Contains(f09EntryIDs(Catalog()), "group/auto-m") {
		t.Fatalf("hidden group still in the catalog")
	}
	if err := DeleteGroup("auto-m"); err == nil {
		t.Errorf("deleting a hidden group should fail")
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("auto-m"); !ok || len(ms) != 2 {
		t.Fatalf("after ShowGroup: %v %v", ms, ok)
	}
	if err := DeleteGroup("no-such-group"); err == nil {
		t.Errorf("deleting a missing group should fail")
	}
}

func TestF09SaveGroupRules(t *testing.T) {
	f09GroupHome(t)
	f09Vendor(t, "a", catalog.Model{ID: "m"})
	f09Vendor(t, "b", catalog.Model{ID: "vendor/m"})

	const empty = "组里至少要有一个模型"
	if err := SaveGroup(Group{ID: "empty"}); err == nil || err.Error() != empty {
		t.Errorf("no members: %v", err)
	}
	if err := SaveGroup(Group{ID: "nested", Members: f09Members("group/auto-m")}); err == nil || err.Error() != empty {
		t.Errorf("only nested groups: %v", err)
	}
	if err := Save(Provider{ID: "group", Name: "G", Chat: "https://g.test/v1", Key: "k"}); err == nil {
		t.Errorf(`provider id "group" accepted`)
	}

	mixed := []Member{
		{Provider: "a", Model: "m"},
		{Provider: "group", Model: "auto-m"},
		{Provider: "a", Model: "m"},
		{Provider: "b", Model: "vendor/m"},
	}
	if err := SaveGroup(Group{Name: "Mine Group", Members: mixed, Routing: "ordered", Affinity: "bogus"}); err != nil {
		t.Fatal(err)
	}
	f := load()
	var saved Group
	for _, g := range f.Groups {
		if g.ID == "mine-group" {
			saved = g
		}
	}
	if saved.ID == "" {
		t.Fatalf("id not made from the name: %+v", f.Groups)
	}
	if saved.Name != "Mine Group" || !slices.Equal(saved.Members, f09Members("a/m", "b/vendor/m")) {
		t.Errorf("saved %+v", saved)
	}
	if saved.Routing != "" || saved.Affinity != "" {
		t.Errorf("unknown routing/affinity kept: %q %q", saved.Routing, saved.Affinity)
	}
	if _, ms, ok := FindGroup("group/mine-group"); !ok || len(ms) != 2 {
		t.Errorf("FindGroup by group/<id>: %v %v", ms, ok)
	}
}

func TestF09UserGroupReplacesAutoGroup(t *testing.T) {
	f09GroupHome(t)
	f09Vendor(t, "a", catalog.Model{ID: "m"})
	f09Vendor(t, "b", catalog.Model{ID: "vendor/m"})

	if err := SaveGroup(Group{ID: "auto-m", Members: f09Members("b/vendor/m")}); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("auto-m"); !ok || !slices.Equal(ms, f09Members("b/vendor/m")) {
		t.Fatalf("replaced group: %v %v", ms, ok)
	}
	groups := 0
	for _, e := range Catalog() {
		if e.Group != nil {
			groups++
		}
	}
	if groups != 1 {
		t.Errorf("want one routing group, got %d", groups)
	}
	// deleting the user's copy hides the one magpie finds, too
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Errorf("the discovered group came back after its copy was deleted")
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("auto-m"); !ok || len(ms) != 2 {
		t.Errorf("after ShowGroup: %v %v", ms, ok)
	}
}

func TestF09FindGroupSkipsUnusableMembers(t *testing.T) {
	f09GroupHome(t)
	f09Vendor(t, "a", catalog.Model{ID: "m"})
	if err := SaveGroup(Group{ID: "mine", Members: f09Members("a/m", "nobody/x")}); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("mine"); !ok || !slices.Equal(ms, f09Members("a/m")) {
		t.Errorf("unusable member kept: %v %v", ms, ok)
	}
	if err := SaveGroup(Group{ID: "lost", Members: f09Members("nobody/x")}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("lost"); ok {
		t.Errorf("a group with no usable member is usable")
	}
	if slices.Contains(f09EntryIDs(Catalog()), "group/lost") {
		t.Errorf("a group with no usable member is in the catalog")
	}
}

func TestF09SetAffinity(t *testing.T) {
	f09GroupHome(t)
	f09Vendor(t, "a", catalog.Model{ID: "m"})
	if err := SetAffinity("a", "turn"); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("a"); err != nil || p.Affinity != "turn" {
		t.Fatalf("affinity: %+v %v", p, err)
	}
	if err := SetAffinity("a", "sticky"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != "" {
		t.Errorf("unknown affinity kept: %q", p.Affinity)
	}
	if err := SetAffinity("nobody", "session"); err == nil {
		t.Errorf("unknown provider accepted")
	}
}

func TestF09NormalizeRoutingAndAffinity(t *testing.T) {
	for in, want := range map[string]string{"order": Ordered, "rotate": Rotate, "least-used": LeastUsed, "usage": LeastUsed, "ordered": "", "": ""} {
		if got := normalizeRouting(in); got != want {
			t.Errorf("normalizeRouting(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"session": AffinitySession, "turn": AffinityTurn, "off": AffinityOff, "": AffinityAuto, "on": AffinityAuto} {
		if got := normalizeAffinity(in); got != want {
			t.Errorf("normalizeAffinity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestF09GroupJSONKeepsMembersAsStrings(t *testing.T) {
	g := Group{ID: "mine", Members: f09Members("b/vendor/m", "a/only-a")}
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"members":["b/vendor/m","a/only-a"]`) {
		t.Fatalf("members not written as provider/model: %s", b)
	}
	var back Group
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(back.Members, g.Members) {
		t.Errorf("round trip: %v", back.Members)
	}
}
