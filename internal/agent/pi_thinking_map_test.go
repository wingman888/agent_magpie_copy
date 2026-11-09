package agent

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// piThinkMapEntry marshals the Pi models[] entry for m and decodes it back.
func piThinkMapEntry(t *testing.T, m catalog.Model) map[string]any {
	t.Helper()
	b, err := json.Marshal(piModelEntry(m))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPiThinkMapMaxOnly(t *testing.T) {
	got := piThinkMapEntry(t, catalog.Model{ID: "glm", Name: "GLM", Efforts: []string{"low", "medium", "high", "max"}})
	if got["reasoning"] != true {
		t.Fatalf("reasoning: %v", got["reasoning"])
	}
	tm, ok := got["thinkingLevelMap"].(map[string]any)
	if !ok {
		t.Fatalf("thinkingLevelMap missing: %v", got)
	}
	if len(tm) != 1 || tm["max"] != "max" {
		t.Fatalf("thinkingLevelMap: %v", tm)
	}
}

func TestPiThinkMapXhighOnly(t *testing.T) {
	got := piThinkMapEntry(t, catalog.Model{ID: "gpt", Name: "GPT", Efforts: []string{"none", "low", "xhigh"}})
	tm, ok := got["thinkingLevelMap"].(map[string]any)
	if !ok {
		t.Fatalf("thinkingLevelMap missing: %v", got)
	}
	if len(tm) != 1 || tm["xhigh"] != "xhigh" {
		t.Fatalf("thinkingLevelMap: %v", tm)
	}
}

func TestPiThinkMapBoth(t *testing.T) {
	got := piThinkMapEntry(t, catalog.Model{ID: "both", Name: "Both", Efforts: []string{"high", "xhigh", "max"}})
	tm, ok := got["thinkingLevelMap"].(map[string]any)
	if !ok {
		t.Fatalf("thinkingLevelMap missing: %v", got)
	}
	if len(tm) != 2 || tm["xhigh"] != "xhigh" || tm["max"] != "max" {
		t.Fatalf("thinkingLevelMap: %v", tm)
	}
}

func TestPiThinkMapBasicLevelsNoField(t *testing.T) {
	cases := []catalog.Model{
		{ID: "lmh", Name: "LMH", Efforts: []string{"low", "medium", "high"}},
		{ID: "none", Name: "None", Efforts: nil},
	}
	for _, m := range cases {
		got := piThinkMapEntry(t, m)
		if _, ok := got["thinkingLevelMap"]; ok {
			t.Fatalf("%s: thinkingLevelMap should be absent: %v", m.ID, got)
		}
	}
	got := piThinkMapEntry(t, catalog.Model{ID: "lmh", Name: "LMH", Efforts: []string{"low", "medium", "high"}})
	if got["reasoning"] != true || got["id"] != "lmh" || got["name"] != "LMH" {
		t.Fatalf("entry: %v", got)
	}
}
