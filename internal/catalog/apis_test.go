package catalog

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The live list keeps each model's declared APIs across a save and a read,
// and a model that declares none keeps nil (unknown), not an empty list.
func TestLiveRoundTripKeepsAPIs(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	in := []Model{
		{ID: "gpt-x", Name: "GPT X", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat", "anthropic"}},
		{ID: "plain"},
	}
	if err := SaveLive("apis-test", "https://example.test", in); err != nil {
		t.Fatal(err)
	}
	out, _, ok := Live("apis-test")
	if !ok || len(out) != 3 {
		t.Fatalf("live: ok=%v %+v", ok, out)
	}
	if !reflect.DeepEqual(out[0].APIs, []string{"responses"}) {
		t.Fatalf("gpt-x APIs: %v", out[0].APIs)
	}
	if !reflect.DeepEqual(out[1].APIs, []string{"chat", "anthropic"}) {
		t.Fatalf("claude-x APIs: %v", out[1].APIs)
	}
	if out[2].APIs != nil {
		t.Fatalf("plain APIs should be unknown (nil), got %v", out[2].APIs)
	}
}

// The field is written under its JSON name, and omitted when unknown.
func TestModelAPIsJSON(t *testing.T) {
	b, err := json.Marshal(Model{ID: "m", APIs: []string{"chat"}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if got, ok := m["apis"].([]any); !ok || len(got) != 1 || got[0] != "chat" {
		t.Fatalf("apis json: %s", b)
	}
	b, _ = json.Marshal(Model{ID: "m"})
	if containsKey(b, "apis") {
		t.Fatalf("empty APIs should be omitted: %s", b)
	}
}

func containsKey(b []byte, key string) bool {
	var m map[string]any
	json.Unmarshal(b, &m)
	_, ok := m[key]
	return ok
}
