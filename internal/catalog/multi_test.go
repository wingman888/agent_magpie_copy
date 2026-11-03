package catalog

import "testing"

func TestFindMatchesVendorPrefixAndFirstCatalogWins(t *testing.T) {
	known := []Model{
		{ID: "gpt-5.5", Name: "GPT-5.5"},
		{ID: "shared", Name: "first"},
		{ID: "deepseek-chat", Name: "DeepSeek Chat"},
		{ID: "shared", Name: "second"},
	}
	if m, ok := Find(known, "deepseek/deepseek-chat"); !ok || m.Name != "DeepSeek Chat" {
		t.Errorf("prefixed id: %+v %v", m, ok)
	}
	if m, ok := Find(known, "gpt-5.5"); !ok || m.Name != "GPT-5.5" {
		t.Errorf("bare id: %+v %v", m, ok)
	}
	if m, ok := Find(known, "shared"); !ok || m.Name != "first" {
		t.Errorf("duplicate id should keep the first: %+v", m)
	}
	if _, ok := Find(known, "vendor/none"); ok {
		t.Error("an unknown model was found")
	}
}

func TestDecorateMatchesPrefixedLiveIDs(t *testing.T) {
	known := []Model{{ID: "deepseek-chat", Name: "DeepSeek Chat", Efforts: []string{"low"}}}
	out := Decorate([]Model{{ID: "deepseek/deepseek-chat"}, {ID: "other/x"}}, known)
	if out[0].Name != "DeepSeek Chat" || len(out[0].Efforts) != 1 {
		t.Errorf("prefixed live model: %+v", out[0])
	}
	if out[1].Name != "" || out[1].Efforts != nil {
		t.Errorf("unmatched live model gained a name: %+v", out[1])
	}
}
