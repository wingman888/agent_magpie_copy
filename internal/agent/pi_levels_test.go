package agent

import (
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestPiModelDeclaresXhighAndMax(t *testing.T) {
	cases := []struct {
		name    string
		efforts []string
		levels  map[string]any // nil: no thinkingLevelMap at all
		reason  bool
	}{
		{"max", []string{"low", "medium", "high", "max"}, map[string]any{"max": "max"}, true},
		{"xhigh", []string{"low", "medium", "high", "xhigh"}, map[string]any{"xhigh": "xhigh"}, true},
		{"both", []string{"none", "high", "xhigh", "max"}, map[string]any{"xhigh": "xhigh", "max": "max"}, true},
		{"plain", []string{"low", "medium", "high"}, nil, true},
		{"no reasoning", nil, nil, false},
	}
	for _, c := range cases {
		e := piModel(catalog.Model{ID: "m", Name: "M", Efforts: c.efforts})
		if e["reasoning"] != c.reason {
			t.Errorf("%s: reasoning = %v", c.name, e["reasoning"])
		}
		got, has := e["thinkingLevelMap"]
		if c.levels == nil {
			if has {
				t.Errorf("%s: unexpected thinkingLevelMap %v", c.name, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, c.levels) {
			t.Errorf("%s: thinkingLevelMap = %v, want %v", c.name, got, c.levels)
		}
	}
}
