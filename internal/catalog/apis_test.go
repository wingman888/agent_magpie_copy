package catalog

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// The live list keeps which wire APIs a model is served on, and gives them
// back unchanged; a model that doesn't say keeps no field at all.
func TestLiveRoundTripKeepsAPIs(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Cleanup(Reset)

	want := []Model{
		{ID: "gpt-x", Name: "GPT X", APIs: []string{"responses"}},
		{ID: "claude-x", Name: "Claude X", APIs: []string{"chat", "anthropic"}},
		{ID: "plain", Name: "Plain"},
	}
	if err := SaveLive("apitest", "https://example.test", want); err != nil {
		t.Fatal(err)
	}

	got, _, ok := Live("apitest")
	if !ok {
		t.Fatal("no live list read back")
	}
	if len(got) != len(want) {
		t.Fatalf("models: %+v", got)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i].APIs, want[i].APIs) {
			t.Errorf("%s: APIs = %v, want %v", want[i].ID, got[i].APIs, want[i].APIs)
		}
	}
	if got[2].APIs != nil {
		t.Errorf("unknown APIs read back as %v", got[2].APIs)
	}

	// On disk, the field is "apis" and is left out when empty.
	b, err := os.ReadFile(LivePath("apitest"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Count(s, `"apis"`) != 2 {
		t.Errorf("want two \"apis\" keys on disk, got file:\n%s", s)
	}
}
