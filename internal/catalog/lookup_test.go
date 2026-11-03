package catalog

import "testing"

func TestBaseStripsRelayPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"deepseek/deepseek-chat": "deepseek-chat",
		"a/b/c":                  "c",
		"gpt-5.5":                "gpt-5.5",
		"":                       "",
	} {
		if got := Base(in); got != want {
			t.Errorf("Base(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindMatchesPrefixedIDs(t *testing.T) {
	models := []Model{
		{ID: "gpt-5.5", Name: "GPT-5.5"},
		{ID: "deepseek-chat", Name: "DeepSeek Chat", Efforts: []string{"low"}},
	}
	m, ok := Find(models, "deepseek/deepseek-chat")
	if !ok || m.Name != "DeepSeek Chat" || len(m.Efforts) != 1 {
		t.Fatalf("prefixed: %+v %v", m, ok)
	}
	if m.ID != "deepseek-chat" {
		t.Errorf("Find returned id %q, want the catalog's own id", m.ID)
	}
	if m, ok := Find(models, "gpt-5.5"); !ok || m.Name != "GPT-5.5" {
		t.Fatalf("bare: %+v %v", m, ok)
	}
	for _, miss := range []string{"nope", "x/nope", ""} {
		if _, ok := Find(models, miss); ok {
			t.Errorf("Find(%q) matched", miss)
		}
	}
}

func TestFindFallsBackToFullID(t *testing.T) {
	// a catalog whose own ids contain a slash still matches on the full id
	models := []Model{{ID: "openai/gpt-oss", Name: "GPT OSS"}}
	if m, ok := Find(models, "openai/gpt-oss"); !ok || m.Name != "GPT OSS" {
		t.Fatalf("slashed catalog id: %+v %v", m, ok)
	}
}

func TestEffortsSeesPrefix(t *testing.T) {
	models := []Model{{ID: "gpt-5.5", Efforts: []string{"low", "high"}}}
	if got := Efforts(models, "openai/gpt-5.5"); len(got) != 2 || got[1] != "high" {
		t.Fatalf("efforts: %v", got)
	}
	if got := Efforts(models, "openai/unknown"); got != nil {
		t.Fatalf("unknown model got efforts: %v", got)
	}
}

func TestUnionReadsCatalogsInOrder(t *testing.T) {
	writeCatalog(t, `{
	  "openai":   {"models": {
	      "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5"},
	      "shared":  {"id":"shared","name":"Shared from OpenAI"}}},
	  "deepseek": {"models": {
	      "deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat"},
	      "shared":        {"id":"shared","name":"Shared from DeepSeek"}}}
	}`)

	ms := Union("openai", "deepseek", "missing")
	if len(ms) != 3 {
		t.Fatalf("union: %+v", ms)
	}
	byID := map[string]Model{}
	for _, m := range ms {
		byID[m.ID] = m
	}
	if byID["shared"].Name != "Shared from OpenAI" {
		t.Errorf("earlier catalog should win: %+v", byID["shared"])
	}
	if _, ok := byID["deepseek-chat"]; !ok {
		t.Errorf("deepseek model missing from union")
	}
	if got := Union("deepseek", "openai"); got[0].Name == "" {
		t.Errorf("reverse order: %+v", got)
	}
}

func TestDecorateMatchesPrefixedLiveIDs(t *testing.T) {
	writeCatalog(t, `{
	  "openai":   {"models": {"gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","reasoning_options":[{"type":"effort","values":["low","high"]}]}}},
	  "deepseek": {"models": {"deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat"}}}
	}`)
	known := Union("openai", "deepseek")
	out := Decorate([]Model{{ID: "deepseek/deepseek-chat"}, {ID: "gpt-5.5"}, {ID: "relay/unknown"}}, known)
	if out[0].ID != "deepseek/deepseek-chat" || out[0].Name != "DeepSeek Chat" {
		t.Errorf("prefixed live model: %+v", out[0])
	}
	if out[1].Name != "GPT-5.5" || len(out[1].Efforts) != 2 {
		t.Errorf("gpt-5.5: %+v", out[1])
	}
	if out[2].Name != "" || len(out[2].Efforts) != 0 {
		t.Errorf("unmatched model gained data: %+v", out[2])
	}
}
