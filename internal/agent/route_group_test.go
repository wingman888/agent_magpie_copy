package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// f09GroupPicker gives a test its own config and two providers: a offers m and
// only-a, b offers vendor/m, so magpie groups the two m's as auto-m.
func f09GroupPicker(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Chat: "https://a.example/v1", Key: "ka", Models: []string{"m", "only-a"}},
		{ID: "b", Name: "B", Chat: "https://b.example/v1", Key: "kb", Icon: "openai", Models: []string{"vendor/m"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
}

// The routing groups lead the picker in their own section, ahead of every
// provider's section, and say they are routed by magpie.
func TestF09PickerRoutingGroupsFirst(t *testing.T) {
	f09GroupPicker(t)
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"a/m", "a/only-a", "b/vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	opts := viaMagpie("magpie/")
	if len(opts) == 0 {
		t.Fatal("empty picker")
	}
	seenProvider := false
	var groups []Option
	for _, o := range opts {
		if o.Group == groupsSection {
			if seenProvider {
				t.Fatalf("routing group %q listed after a provider section", o.Value)
			}
			groups = append(groups, o)
			continue
		}
		seenProvider = true
	}
	if len(groups) != 2 {
		t.Fatalf("want 2 routing groups (mine, auto-m), got %+v", groups)
	}
	for _, g := range groups {
		if g.Note != "routing group · via magpie" {
			t.Errorf("%s: note %q", g.Value, g.Note)
		}
		if !strings.HasPrefix(g.Value, "magpie/group/") {
			t.Errorf("value %q", g.Value)
		}
	}
	if opts[0].Group != groupsSection || opts[0].Label != "Mine" || opts[0].Value != "magpie/group/mine" {
		t.Fatalf("first option: %+v", opts[0])
	}
	// members' provider icons, each once: a has no icon so its id stands in
	if strings.Join(opts[0].Icons, ",") != "a,openai" || opts[0].Icon != "a" {
		t.Errorf("icons: %v icon=%q", opts[0].Icons, opts[0].Icon)
	}
	// a provider's own models keep their provider as the section
	var sections []string
	for _, o := range opts[len(groups):] {
		if len(sections) == 0 || sections[len(sections)-1] != o.Group {
			sections = append(sections, o.Group)
		}
	}
	if strings.Join(sections, ",") != "A,B" {
		t.Errorf("provider sections: %v", sections)
	}
}

// A group reference is a catalog model for every agent's own model files.
func TestF09IsMagpieKnowsGroups(t *testing.T) {
	f09GroupPicker(t)
	if !isMagpie("group/auto-m") {
		t.Error("auto group should be a magpie model")
	}
	if isMagpie("group/nope") {
		t.Error("unknown group should not be a magpie model")
	}
}
