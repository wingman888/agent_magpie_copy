package catalog

import "testing"

// A gateway may name its models "vendor/model"; the part after the last slash
// is the catalog id, and an id that matches nothing stays bare.
func TestFbchainDecorateReadsPrefixedIDs(t *testing.T) {
	live := []Model{
		{ID: "deepseek/deepseek-chat"},
		{ID: "vendor/x/gpt-5.5"},
		{ID: "nope/unknown"},
		{ID: "plain"},
	}
	known := []Model{
		{ID: "deepseek-chat", Name: "DeepSeek Chat", Provider: "deepseek"},
		{ID: "gpt-5.5", Name: "GPT-5.5", Efforts: []string{"low", "high"}, Provider: "openai"},
	}
	out := Decorate(live, known)
	if out[0].Name != "DeepSeek Chat" || out[0].ID != "deepseek/deepseek-chat" {
		t.Fatalf("deepseek/deepseek-chat: %+v", out[0])
	}
	if out[1].Name != "GPT-5.5" || len(out[1].Efforts) != 2 || out[1].ID != "vendor/x/gpt-5.5" {
		t.Fatalf("vendor/x/gpt-5.5: %+v", out[1])
	}
	if out[2].Name != "nope/unknown" || len(out[2].Efforts) != 0 || out[2].Provider != "" {
		t.Fatalf("an unmatched id must come back bare: %+v", out[2])
	}
	if out[3].Name != "plain" || out[3].Provider != "" {
		t.Fatalf("plain: %+v", out[3])
	}
}
