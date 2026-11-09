package catalog

import "testing"

func TestS1HDecorateLooksUpVendorPrefixedIDs(t *testing.T) {
	live := []Model{
		{ID: "deepseek/deepseek-chat", Name: "deepseek/deepseek-chat"},
		{ID: "gpt-5.5", Name: "gpt-5.5"},
		{ID: "vendor/unknown", Name: "vendor/unknown"},
	}
	known := []Model{
		{ID: "deepseek-chat", Name: "DeepSeek Chat"},
		{ID: "gpt-5.5", Name: "GPT-5.5", Efforts: []string{"low", "high"}},
	}
	out := Decorate(live, known)
	if out[0].ID != "deepseek/deepseek-chat" || out[0].Name != "DeepSeek Chat" {
		t.Fatalf("prefixed id: %+v", out[0])
	}
	if out[1].Name != "GPT-5.5" || len(out[1].Efforts) != 2 {
		t.Fatalf("direct id: %+v", out[1])
	}
	if out[2].ID != "vendor/unknown" || out[2].Name != "vendor/unknown" || len(out[2].Efforts) != 0 {
		t.Fatalf("unmatched id should pass through untouched: %+v", out[2])
	}
}
