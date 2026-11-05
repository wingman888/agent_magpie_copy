package agent

import (
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestPiModelDeclaresMaxAndXhigh(t *testing.T) {
	m := catalog.Model{ID: "glm", Name: "GLM", Efforts: []string{"low", "medium", "high", "max"}}
	got := piModel(m)
	want := map[string]any{"id": "glm", "name": "GLM", "reasoning": true,
		"thinkingLevelMap": map[string]string{"max": "max"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("max model: got %#v, want %#v", got, want)
	}

	x := piModel(catalog.Model{ID: "x", Name: "X", Efforts: []string{"low", "xhigh"}})
	if !reflect.DeepEqual(x["thinkingLevelMap"], map[string]string{"xhigh": "xhigh"}) {
		t.Errorf("xhigh model: %#v", x)
	}

	both := piModel(catalog.Model{ID: "b", Name: "B", Efforts: []string{"xhigh", "max"}})
	if !reflect.DeepEqual(both["thinkingLevelMap"], map[string]string{"xhigh": "xhigh", "max": "max"}) {
		t.Errorf("both: %#v", both)
	}
}

func TestPiModelWithoutExtraLevelsHasNoMap(t *testing.T) {
	for _, m := range []catalog.Model{
		{ID: "a", Name: "A", Efforts: []string{"low", "medium", "high"}},
		{ID: "n", Name: "N"},
	} {
		got := piModel(m)
		if _, ok := got["thinkingLevelMap"]; ok {
			t.Errorf("%s: unexpected thinkingLevelMap %#v", m.ID, got)
		}
		if got["reasoning"] != (len(m.Efforts) > 0) {
			t.Errorf("%s: reasoning %v", m.ID, got["reasoning"])
		}
	}
}
