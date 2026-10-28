package provider_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func add(t *testing.T, id string, models ...string) {
	t.Helper()
	p := provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k-" + id, Models: models, Chat: "http://127.0.0.1:1/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func custom(es []provider.Entry) []string {
	var ids []string
	for _, e := range es {
		if e.Provider.Account == nil {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

func groupWith(t *testing.T, member string) (provider.Group, bool) {
	t.Helper()
	for _, g := range provider.Groups() {
		if slices.Contains(g.Members, member) {
			return g, true
		}
	}
	return provider.Group{}, false
}

func TestAcceptCatalogAndGroupLifecycle(t *testing.T) {
	isolate(t)
	add(t, "a", "m", "only-a")
	add(t, "b", "vendor/m")
	if got := strings.Join(custom(provider.Catalog()), " "); got != "a/m a/only-a b/vendor/m group/auto-m" {
		t.Fatalf("catalog: %s", got)
	}
	for _, e := range provider.Catalog() {
		if e.ID == "group/auto-m" {
			if e.Group != "auto-m" || e.Model != "m" || e.Provider.ID != "a" || len(e.Icons) != 2 {
				t.Fatalf("auto entry: %+v", e)
			}
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"b/vendor/m", "a/only-a"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	p, m, ok := provider.Resolve("group/mine")
	if !ok || p.ID != "b" || m != "vendor/m" {
		t.Fatalf("resolve: %v %s %s", ok, p.ID, m)
	}
	g, ms, ok := provider.FindGroup("group/mine")
	if !ok || g.ID != "mine" || g.Name != "Mine" || len(ms) != 2 || ms[0].Provider.ID != "b" || ms[0].Model != "vendor/m" || ms[1].Model != "only-a" {
		t.Fatalf("find: %+v %+v %v", g, ms, ok)
	}
	if _, _, ok := provider.FindGroup("mine"); ok {
		t.Fatal("found without prefix")
	}
	// user's groups first, then found ones
	gs := provider.Groups()
	if len(gs) != 2 || gs[0].ID != "mine" || gs[1].ID != "auto-m" || !gs[1].Auto {
		t.Fatalf("groups: %+v", gs)
	}

	// hide / show a found group
	if err := provider.DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := provider.Resolve("group/auto-m"); ok {
		t.Fatal("hidden group resolves")
	}
	if _, _, ok := provider.FindGroup("group/auto-m"); ok {
		t.Fatal("hidden group found")
	}
	var hidden bool
	for _, g := range provider.Groups() {
		if g.ID == "auto-m" {
			hidden = g.Hidden
		}
	}
	if !hidden {
		t.Fatal("hidden group not listed as hidden")
	}
	if slices.Contains(custom(provider.Catalog()), "group/auto-m") {
		t.Fatal("hidden group in catalog")
	}
	if err := provider.ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := provider.Resolve("group/auto-m"); !ok {
		t.Fatal("shown group does not resolve")
	}
	if err := provider.DeleteGroup("nope"); err == nil || !strings.Contains(err.Error(), `no group "nope"`) {
		t.Fatalf("delete missing: %v", err)
	}
	if err := provider.DeleteGroup("mine"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := provider.Resolve("group/mine"); ok {
		t.Fatal("deleted group resolves")
	}

	// saving a found group makes it the user's, without a duplicate
	if err := provider.SaveGroup(provider.Group{ID: "auto-m", Name: "Mine now", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, g := range provider.Groups() {
		if g.ID == "auto-m" {
			n++
			if g.Auto || g.Name != "Mine now" {
				t.Fatalf("user's version: %+v", g)
			}
		}
	}
	if n != 1 {
		t.Fatalf("auto-m listed %d times", n)
	}
}

func TestAcceptSaveGroupValidation(t *testing.T) {
	isolate(t)
	add(t, "a", "m", "x")
	for _, id := range []string{"Bad Id", "a_b", "x/y"} {
		err := provider.SaveGroup(provider.Group{ID: id, Members: []string{"a/m"}})
		if err == nil || !strings.Contains(err.Error(), "a group's id must be lowercase letters, digits and dashes") {
			t.Errorf("id %q: %v", id, err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "ok", Members: nil}); err == nil || !strings.Contains(err.Error(), "a group needs a model in it") {
		t.Errorf("empty: %v", err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "ok", Members: []string{"", "group/x"}}); err == nil || !strings.Contains(err.Error(), "a group needs a model in it") {
		t.Errorf("only nested: %v", err)
	}
	err := provider.SaveGroup(provider.Group{ID: " My-Group ", Members: []string{"a/m", "group/x", "a/m", "a/x"}, Routing: "bogus", Affinity: "bogus"})
	if err != nil {
		t.Fatal(err)
	}
	g, ms, ok := provider.FindGroup("group/my-group")
	if !ok || g.Name != "my-group" || g.Routing != "" || g.Affinity != "" || g.Auto || g.Hidden {
		t.Fatalf("saved: %+v %v", g, ok)
	}
	if !slices.Equal(g.Members, []string{"a/m", "a/x"}) || len(ms) != 2 {
		t.Fatalf("members: %v", g.Members)
	}
	// kept values
	if err := provider.SaveGroup(provider.Group{Name: "Fast Lane", Members: []string{"a/m"}, Routing: provider.Rotate, Affinity: provider.AffinitySession}); err != nil {
		t.Fatal(err)
	}
	g, _, ok = provider.FindGroup("group/fast-lane")
	if !ok || g.Routing != provider.Rotate || g.Affinity != provider.AffinitySession {
		t.Fatalf("slug/values: %+v %v", g, ok)
	}
	// nested groups and unavailable members are left out of resolved members
	if err := provider.SaveGroup(provider.Group{ID: "partial", Members: []string{"zzz/none", "a/x", "a/x"}}); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := provider.FindGroup("group/partial"); !ok || len(ms) != 1 || ms[0].Model != "x" {
		t.Fatalf("partial: %+v", ms)
	}
	if err := provider.SaveGroup(provider.Group{ID: "none", Members: []string{"zzz/none"}}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(custom(provider.Catalog()), "group/none") {
		t.Fatal("group with no ready member in catalog")
	}
}

func TestAcceptProviderIDGroupAndAffinity(t *testing.T) {
	isolate(t)
	err := provider.Save(provider.Provider{ID: "group", Key: "k", Chat: "http://127.0.0.1:1/v1"})
	if err == nil || !strings.Contains(err.Error(), `"group" starts the ids of routing groups; pick another id`) {
		t.Fatalf("group id: %v", err)
	}
	add(t, "p", "m")
	if err := provider.SetAffinity("p", provider.AffinitySession); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("p")
	if err != nil || p.Affinity != provider.AffinitySession {
		t.Fatalf("affinity: %+v %v", p, err)
	}
	if err := provider.Save(provider.Provider{ID: "q", Key: "k", Chat: "http://127.0.0.1:1/v1", Affinity: "bogus"}); err != nil {
		t.Fatal(err)
	}
	if q, _ := provider.Find("q"); q == nil || q.Affinity != "" {
		t.Fatalf("bogus affinity kept: %+v", q)
	}
	if !slices.Equal(provider.Affinities, []string{"", "session", "turn", "off"}) || provider.GroupPrefix != "group/" {
		t.Fatal("constants")
	}
}

func TestAcceptAutoGroupNames(t *testing.T) {
	isolate(t)
	add(t, "p1", "claude-opus-5-5")
	add(t, "p2", "anthropic/claude-opus-5.5")
	add(t, "p3", "Claude-Opus-5.5")
	add(t, "p4", "claude-opus-5-5-20260801")
	add(t, "p5", "anthropic/claude-opus-5.5:batch")
	add(t, "p6", "claude-opus-5-5:batch")
	add(t, "p7", "gpt-5.1-codex")
	add(t, "p8", "gpt-5-1-codex")
	add(t, "p9", "v1.beta")
	add(t, "p10", "V1.beta")
	add(t, "p11", "v1-beta")
	add(t, "p12", "solo")
	add(t, "p12b", "solo", "other") // same provider twice isn't a group; p12b is another provider though
	add(t, "dup", "dm", "vendor/dm")

	base, ok := groupWith(t, "p1/claude-opus-5-5")
	want := []string{"p1/claude-opus-5-5", "p2/anthropic/claude-opus-5.5", "p3/Claude-Opus-5.5", "p4/claude-opus-5-5-20260801"}
	if !ok || base.ID != "auto-claude-opus-5-5" || !base.Auto || !slices.Equal(base.Members, want) {
		t.Fatalf("base group: %+v", base)
	}
	batch, ok := groupWith(t, "p5/anthropic/claude-opus-5.5:batch")
	if !ok || batch.ID == base.ID || !slices.Equal(batch.Members, []string{"p5/anthropic/claude-opus-5.5:batch", "p6/claude-opus-5-5:batch"}) {
		t.Fatalf("batch group: %+v", batch)
	}
	codex, ok := groupWith(t, "p7/gpt-5.1-codex")
	if !ok || !slices.Contains(codex.Members, "p8/gpt-5-1-codex") || len(codex.Members) != 2 {
		t.Fatalf("codex group: %+v", codex)
	}
	beta, ok := groupWith(t, "p9/v1.beta")
	if !ok || !slices.Contains(beta.Members, "p10/V1.beta") || slices.Contains(beta.Members, "p11/v1-beta") {
		t.Fatalf("v1.beta group: %+v", beta)
	}
	if g, ok := groupWith(t, "dup/dm"); ok {
		t.Fatalf("same-provider duplicates grouped: %+v", g)
	}
	// the group's entry is made from the first member
	for _, e := range provider.Catalog() {
		if e.ID == "group/auto-claude-opus-5-5" {
			if e.Provider.ID != "p1" || e.Model != "claude-opus-5-5" || len(e.Icons) != 4 {
				t.Fatalf("entry: %+v", e)
			}
		}
	}
}
