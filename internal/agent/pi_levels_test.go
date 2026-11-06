package agent

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestPiModelEntryDeclaresMaxAndXhigh(t *testing.T) {
	cases := []struct {
		efforts []string
		want    map[string]any // nil means no thinkingLevelMap at all
	}{
		{[]string{"low", "medium", "high"}, nil},
		{nil, nil},
		{[]string{"low", "high", "max"}, map[string]any{"max": "max"}},
		{[]string{"minimal", "xhigh", "max"}, map[string]any{"xhigh": "xhigh", "max": "max"}},
		{[]string{"none", "high", "xhigh"}, map[string]any{"xhigh": "xhigh"}},
	}
	for _, c := range cases {
		e := piModelEntry(catalog.Model{ID: "m", Name: "M", Efforts: c.efforts})
		got, has := e["thinkingLevelMap"]
		if c.want == nil {
			if has {
				t.Errorf("efforts %v: unexpected thinkingLevelMap %v", c.efforts, got)
			}
			continue
		}
		m, ok := got.(map[string]any)
		if !has || !ok || len(m) != len(c.want) {
			t.Errorf("efforts %v: thinkingLevelMap %v", c.efforts, got)
			continue
		}
		for k, v := range c.want {
			if m[k] != v {
				t.Errorf("efforts %v: thinkingLevelMap[%s] = %v", c.efforts, k, m[k])
			}
		}
	}
	if e := piModelEntry(catalog.Model{ID: "m", Name: "M", Efforts: []string{"high"}}); e["reasoning"] != true {
		t.Errorf("reasoning flag lost: %v", e)
	}
}
