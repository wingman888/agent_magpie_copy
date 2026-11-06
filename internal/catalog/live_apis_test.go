package catalog

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The declared APIs survive a save and a read of the live list, and an
// absent list stays absent on disk.
func TestLiveKeepsAPIs(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ms := []Model{
		{ID: "gpt-x", Name: "GPT X", APIs: []string{"responses"}},
		{ID: "claude-x", Name: "Claude X", APIs: []string{"chat", "anthropic"}},
		{ID: "plain", Name: "Plain"},
	}
	if err := SaveLive("copilot", "https://example.test", ms); err != nil {
		t.Fatal(err)
	}
	got, _, ok := Live("copilot")
	if !ok || len(got) != 3 {
		t.Fatalf("live: %v %+v", ok, got)
	}
	for i := range ms {
		if !reflect.DeepEqual(got[i].APIs, ms[i].APIs) {
			t.Fatalf("model %s apis: got %v want %v", ms[i].ID, got[i].APIs, ms[i].APIs)
		}
	}

	b, err := json.Marshal(Model{ID: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"ID":"plain","Name":"","Provider":"","Released":"","Efforts":null,"Temperature":null,"Price":null}` {
		t.Fatalf("empty apis should be omitted: %s", b)
	}
}
