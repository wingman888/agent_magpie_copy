package agent

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestPiThinkingLevelMap(t *testing.T) {
	old := piModels
	t.Cleanup(func() { piModels = old })
	piModels = func() []catalog.Model {
		return []catalog.Model{
			{ID: "a", Name: "A", Efforts: []string{"low", "high", "max"}},
			{ID: "b", Name: "B", Efforts: []string{"low", "medium", "high"}},
			{ID: "c", Name: "C", Efforts: []string{"high", "xhigh"}},
			{ID: "d", Name: "D"},
		}
	}
	raw, _ := json.Marshal(magpieProviderJSON("pi"))
	var v struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	m := map[string]map[string]any{}
	for _, e := range v.Models {
		m[e["id"].(string)] = e
	}
	if l := m["a"]["thinkingLevelMap"].(map[string]any); len(l) != 1 || l["max"] != "max" {
		t.Errorf("a: %v", l)
	}
	if l := m["c"]["thinkingLevelMap"].(map[string]any); len(l) != 1 || l["xhigh"] != "xhigh" {
		t.Errorf("c: %v", l)
	}
	for _, id := range []string{"b", "d"} {
		if _, ok := m[id]["thinkingLevelMap"]; ok {
			t.Errorf("%s has map", id)
		}
	}
}
