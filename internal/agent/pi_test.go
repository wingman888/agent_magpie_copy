package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestPiThinkingLevelMapDeclaresXhighAndMax(t *testing.T) {
	cfg := piProviderJSON([]catalog.Model{
		{ID: "both", Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
		{ID: "max-only", Efforts: []string{"none", "low", "high", "max"}},
		{ID: "xhigh-only", Efforts: []string{"low", "medium", "high", "xhigh"}},
		{ID: "plain", Efforts: []string{"low", "medium", "high"}},
		{ID: "no-reasoning"},
	})
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Models []struct {
			ID               string            `json:"id"`
			Reasoning        bool              `json:"reasoning"`
			ThinkingLevelMap map[string]string `json:"thinkingLevelMap"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Models) != 5 {
		t.Fatalf("models: got %d", len(out.Models))
	}
	want := map[string]map[string]string{
		"both":         {"xhigh": "xhigh", "max": "max"},
		"max-only":     {"max": "max"},
		"xhigh-only":   {"xhigh": "xhigh"},
		"plain":        nil,
		"no-reasoning": nil,
	}
	for _, m := range out.Models {
		if !reflect.DeepEqual(m.ThinkingLevelMap, want[m.ID]) {
			t.Errorf("%s: thinkingLevelMap = %v, want %v", m.ID, m.ThinkingLevelMap, want[m.ID])
		}
	}
	// the plain entries must not even carry the key
	for _, id := range []string{"plain", "no-reasoning"} {
		for _, raw := range rawPiModels(t, b) {
			if raw["id"] == id {
				if _, ok := raw["thinkingLevelMap"]; ok {
					t.Errorf("%s: thinkingLevelMap present, want absent", id)
				}
			}
		}
	}
}

func rawPiModels(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	var out struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out.Models
}
