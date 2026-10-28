package agent_test

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

func TestAcceptPickerListsRoutingGroupsFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, id := range []string{"pa", "pb"} {
		if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k", Models: []string{"m"}, Chat: "http://127.0.0.1:1/v1"}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := agent.Find("opencode")
	if err != nil {
		t.Fatal(err)
	}
	var opts []agent.Option
	for _, f := range a.Fields {
		if f.Key == "model" && f.Options != nil {
			opts = f.Options(a.Values())
		}
	}
	var grouped, seenProvider bool
	for _, o := range opts {
		if o.Group == agent.RoutingGroups {
			if seenProvider {
				t.Fatalf("routing group after a provider's: %+v", o)
			}
			if o.Label != "auto-m" && o.Label == "" {
				t.Fatalf("no label: %+v", o)
			}
			if o.Note != "routing group · via magpie" || len(o.Icons) != 2 {
				t.Fatalf("group option: %+v", o)
			}
			grouped = true
		} else if strings.HasSuffix(o.Note, "via magpie") {
			seenProvider = true
		}
	}
	if !grouped || !seenProvider {
		t.Fatalf("options: %+v", opts)
	}
}
