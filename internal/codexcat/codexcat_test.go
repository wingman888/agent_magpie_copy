package codexcat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Codex's own models, routed through magpie, keep what Codex knows of them
// — image input, context window, tools — under magpie's id; a third-party
// model gets the generic entry.
func TestCodexCatalogKeepsOwnEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"models":[
		{"slug":"gpt-5.5","display_name":"GPT-5.5","priority":3,"visibility":"list","input_modalities":["text","image"],
		 "context_window":272000,"upgrade":{"model":"gpt-6"},"availability_nux":{"message":"new"}}]}`), 0o644)

	var got struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(Catalog([]catalog.Model{
		{ID: "deepseek/deepseek-chat", Name: "deepseek-chat · DeepSeek"},
		{ID: "codex/gpt-5.5", Name: "GPT-5.5 · Codex", Efforts: []string{"low", "high"}},
	}), &got); err != nil || len(got.Models) != 2 {
		t.Fatalf("%v %v", err, got)
	}
	third, own := got.Models[0], got.Models[1]
	if third["slug"] != "deepseek/deepseek-chat" || third["base_instructions"] != Prompt || len(third["input_modalities"].([]any)) != 1 {
		t.Errorf("third-party entry: %v", third)
	}
	if own["slug"] != "codex/gpt-5.5" || own["display_name"] != "GPT-5.5 · Codex" || own["priority"] != float64(2) ||
		own["context_window"] != float64(272000) || len(own["input_modalities"].([]any)) != 2 {
		t.Errorf("own entry: %v", own)
	}
	if own["base_instructions"] != Prompt {
		t.Error("own entry without base_instructions")
	}
	if _, ok := own["upgrade"]; ok {
		t.Error("upgrade prompt kept")
	}
	if _, ok := own["availability_nux"]; ok {
		t.Error("start-up notice kept")
	}
}
