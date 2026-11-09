package provider

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// TestFixCapability: a routing group answers a capability question as its
// first ready member does, while its listed levels stay the members' overlap.
func TestFixCapability(t *testing.T) {
	fixAccount := func(efforts ...string) *Account {
		return &Account{Agent: "fixagent", User: "fix-user", models: func() []catalog.Model {
			return []catalog.Model{{ID: "m", Efforts: efforts}}
		}}
	}
	fixA := Provider{ID: "fixa", Account: fixAccount("low", "high", "max"), Models: []string{"m"}}
	fixB := Provider{ID: "fixb", Account: fixAccount("high", "max"), Models: []string{"m"}}
	ab := []Member{{Provider: fixA, Model: "m"}, {Provider: fixB, Model: "m"}}
	ba := []Member{{Provider: fixB, Model: "m"}, {Provider: fixA, Model: "m"}}

	if got := groupEfforts(ab); !slices.Equal(got, []string{"high", "max"}) {
		t.Fatalf("overlap %v, want [high max]", got)
	}
	if got := ab[0].efforts(); !slices.Equal(got, []string{"low", "high", "max"}) {
		t.Fatalf("first member efforts %v", got)
	}
	if got := ba[0].efforts(); !slices.Equal(got, []string{"high", "max"}) {
		t.Fatalf("first member efforts after reordering %v", got)
	}

	// the entry a group gets: listed levels are the overlap, capability the first member's
	g := &Group{ID: "fix"}
	e := Entry{ID: "group/fix", Model: "group/fix", Efforts: groupEfforts(ab), Able: ab[0].efforts(), Group: g}
	if got := e.Capability(); !slices.Equal(got, []string{"low", "high", "max"}) {
		t.Fatalf("group capability %v, want first member's", got)
	}
	if !slices.Equal(e.Efforts, []string{"high", "max"}) {
		t.Fatalf("group listed levels %v, want overlap", e.Efforts)
	}

	// a model entry answers with its own levels
	plain := Entry{ID: "fixa/m", Efforts: []string{"low"}}
	if got := plain.Capability(); !slices.Equal(got, []string{"low"}) {
		t.Fatalf("model capability %v", got)
	}
}
