package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A routing group is owned by the provider of its first usable member.
func TestGroupOwnedByFirstMember_f09(t *testing.T) {
	grpIsolate_f09(t)
	for _, id := range []string{"own-a", "own-b"} {
		up := "https://" + id + ".example/v1"
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: up}); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive(id, up, []catalog.Model{{ID: "m"}}); err != nil {
			t.Fatal(err)
		}
	}
	found := false
	for _, e := range provider.Catalog() {
		if e.ID == "group/auto-m" {
			found = true
			if got := ownerOf(e); got != "own-a" {
				t.Fatalf("owned_by %q, want own-a", got)
			}
			if got := modelObject(e)["owned_by"]; got != "own-a" {
				t.Fatalf("modelObject owned_by %v", got)
			}
		}
	}
	if !found {
		t.Fatal("no group/auto-m in the catalog")
	}
}

// A group's own affinity setting is all it has: an empty one is automatic,
// whatever affinity its members' providers were set to.
func TestGroupAffinityIsNotInherited_f09(t *testing.T) {
	grpIsolate_f09(t)
	p := provider.Provider{ID: "inh", Name: "inh", Key: "k", Chat: "https://inh.example/v1", Affinity: "session"}
	ms := []provider.Member{{ProviderID: "inh", Model: "m", Provider: p}}
	s := New()
	_, pl := s.planGroup(provider.Group{ID: "g", Name: "G"}, ms, provider.Chat, nil)
	if pl.group == nil || pl.group.Affinity != "" {
		t.Fatalf("group affinity %+v, want empty", pl.group)
	}
}

// A model whose name says its family is judged by that family first, even
// when the request came in another protocol (kept for TestKeysMadeForOneProtocol).
// A model with no family is judged by the request's protocol: the key that
// names it is the pool, unnamed keys come after it, marked aside.
func TestKeyTierFollowsRequestProtocol_f09(t *testing.T) {
	p := keyTierProvider_f09("tier-req")
	cs, _ := perKeyOf(p, "claude-opus-5-5", provider.Chat)
	if got := keyNames_f09(p, cs); got != "k1 k2 kc*" {
		t.Fatalf("claude from chat: %s", got)
	}
	cs, _ = perKeyOf(p, "m", provider.Chat)
	if got := keyNames_f09(p, cs); got != "kc k1* k2*" {
		t.Fatalf("familyless model from chat: %s", got)
	}
}
