package catalog

import "testing"

func TestModelsMergesCatalogsInOrder(t *testing.T) {
	writeCatalog(t, `{
	  "openai": {"id":"openai","name":"OpenAI","models":{
	    "gpt-5.5":{"id":"gpt-5.5","name":"GPT-5.5"}}},
	  "deepseek": {"id":"deepseek","name":"DeepSeek","models":{
	    "deepseek-chat":{"id":"deepseek-chat","name":"DeepSeek Chat"},
	    "gpt-5.5":{"id":"gpt-5.5","name":"duplicate"}}}
	}`)
	got := Models([]string{"openai", "deepseek", "openai"})
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	seen := map[string]int{}
	for _, id := range ids {
		seen[id]++
	}
	if seen["gpt-5.5"] != 1 || seen["deepseek-chat"] != 1 || len(ids) != 2 {
		t.Fatalf("Models merged ids = %v, want each of gpt-5.5 and deepseek-chat once", ids)
	}
	if got[0].ID != "gpt-5.5" || got[0].Name != "GPT-5.5" {
		t.Errorf("first model = %+v, want the openai one listed first", got[0])
	}
	if len(Models(nil)) != 0 {
		t.Errorf("Models(nil) should be empty")
	}
}

func TestTail(t *testing.T) {
	for in, want := range map[string]string{
		"deepseek/deepseek-chat": "deepseek-chat",
		"a/b/c":                  "c",
		"gpt-5.5":                "gpt-5.5",
	} {
		if got := Tail(in); got != want {
			t.Errorf("Tail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecoratePrefixedIDsGetCatalogNameAndEfforts(t *testing.T) {
	writeCatalog(t, `{
	  "openai": {"id":"openai","name":"OpenAI","models":{
	    "gpt-5.5":{"id":"gpt-5.5","name":"GPT-5.5",
	      "reasoning_options":[{"type":"effort","values":["low","high"]}]}}},
	  "deepseek": {"id":"deepseek","name":"DeepSeek","models":{
	    "deepseek-chat":{"id":"deepseek-chat","name":"DeepSeek Chat",
	      "reasoning_options":[{"type":"effort","values":["high","max"]}]}}}
	}`)
	known := Models([]string{"openai", "deepseek"})
	live := []Model{
		{ID: "deepseek/deepseek-chat"},
		{ID: "gpt-5.5"},
		{ID: "other/unknown-model"},
	}
	got := Decorate(live, known)
	if len(got) != 3 {
		t.Fatalf("Decorate returned %d models, want 3", len(got))
	}
	if got[0].ID != "deepseek/deepseek-chat" || got[0].Name != "DeepSeek Chat" {
		t.Errorf("prefixed model = %+v, want its id kept and the catalog name", got[0])
	}
	if e := Efforts(got, "deepseek/deepseek-chat"); len(e) != 2 || e[0] != "high" {
		t.Errorf("Efforts for prefixed id = %v, want [high max]", e)
	}
	if got[1].Name != "GPT-5.5" || len(got[1].Efforts) != 2 {
		t.Errorf("gpt-5.5 = %+v, want catalog name and efforts", got[1])
	}
	if got[2].ID != "other/unknown-model" || got[2].Name != "" || len(got[2].Efforts) != 0 {
		t.Errorf("unmatched model = %+v, want returned as-is without name or efforts", got[2])
	}
}
