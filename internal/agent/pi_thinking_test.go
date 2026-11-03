package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestPiModelsDeclaresMaxAndXhigh(t *testing.T) {
	models := []catalog.Model{
		{ID: "glm", Name: "GLM", Efforts: []string{"low", "medium", "high", "max"}},
		{ID: "gpt", Name: "GPT", Efforts: []string{"low", "medium", "high", "xhigh"}},
		{ID: "both", Name: "Both", Efforts: []string{"high", "xhigh", "max"}},
		{ID: "plain", Name: "Plain", Efforts: []string{"low", "medium", "high"}},
		{ID: "none", Name: "None"},
	}
	ms := piModels(models)
	if len(ms) != len(models) {
		t.Fatalf("got %d entries, want %d", len(ms), len(models))
	}

	want := map[string]map[string]string{
		"glm":  {"max": "max"},
		"gpt":  {"xhigh": "xhigh"},
		"both": {"xhigh": "xhigh", "max": "max"},
	}
	for _, m := range ms {
		id := m["id"].(string)
		tl, has := m["thinkingLevelMap"]
		if w, ok := want[id]; ok {
			if !has {
				t.Fatalf("%s: thinkingLevelMap missing", id)
			}
			if got := tl.(map[string]string); !reflect.DeepEqual(got, w) {
				t.Fatalf("%s: thinkingLevelMap = %v, want %v", id, got, w)
			}
			continue
		}
		if has {
			t.Fatalf("%s: unexpected thinkingLevelMap %v", id, tl)
		}
	}

	// the field lands in the serialized file under the right key and shape
	b, err := json.Marshal(ms[0])
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	tlm, ok := back["thinkingLevelMap"].(map[string]any)
	if !ok || tlm["max"] != "max" || len(tlm) != 1 {
		t.Fatalf("serialized thinkingLevelMap = %v", back["thinkingLevelMap"])
	}
	if back["reasoning"] != true {
		t.Fatalf("reasoning = %v, want true", back["reasoning"])
	}
}

func TestPiModelsPlainModelsUnchanged(t *testing.T) {
	ms := piModels([]catalog.Model{{ID: "p", Name: "P", Efforts: []string{"low", "medium", "high"}}})
	if _, has := ms[0]["thinkingLevelMap"]; has {
		t.Fatalf("low/medium/high model got thinkingLevelMap: %v", ms[0])
	}
	if ms[0]["reasoning"] != true || ms[0]["id"] != "p" || ms[0]["name"] != "P" {
		t.Fatalf("existing fields changed: %v", ms[0])
	}
}

func TestPiModelsEmpty(t *testing.T) {
	ms := piModels(nil)
	if ms == nil || len(ms) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v", ms)
	}
	b, _ := json.Marshal(ms)
	if string(b) != "[]" {
		t.Fatalf("want [] in JSON, got %s", b)
	}
}

func TestPiThinkingLevelMap(t *testing.T) {
	if got := piThinkingLevelMap(nil); got != nil {
		t.Fatalf("nil efforts: got %v", got)
	}
	if got := piThinkingLevelMap([]string{"low", "high"}); got != nil {
		t.Fatalf("no extra levels: got %v", got)
	}
	if got := piThinkingLevelMap([]string{"max"}); !reflect.DeepEqual(got, map[string]string{"max": "max"}) {
		t.Fatalf("max: got %v", got)
	}
}
