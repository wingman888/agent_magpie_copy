package catalog

import "testing"

// A live "provider/model" id takes the name and reasoning levels of the
// catalog's model after its last slash, and keeps its own id.
func TestDecorateMatchesPrefixedID(t *testing.T) {
	known := []Model{{ID: "deepseek-chat", Name: "DeepSeek Chat", Efforts: []string{"high"}, Provider: "deepseek"}}
	live := []Model{{ID: "deepseek/deepseek-chat"}, {ID: "other/unknown"}}
	out := Decorate(live, known)
	if len(out) != 2 {
		t.Fatalf("got %d models", len(out))
	}
	if out[0].ID != "deepseek/deepseek-chat" || out[0].Name != "DeepSeek Chat" || len(out[0].Efforts) != 1 || out[0].Provider != "deepseek" {
		t.Fatalf("prefixed model = %+v", out[0])
	}
	if out[1].ID != "other/unknown" || out[1].Name != "" || len(out[1].Efforts) != 0 {
		t.Fatalf("unmatched model = %+v", out[1])
	}
}
