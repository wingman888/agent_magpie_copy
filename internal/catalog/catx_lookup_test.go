package catalog

import "testing"

func TestCatxLookupPrefixedIDs(t *testing.T) {
	known := []Model{
		{ID: "deepseek-chat", Name: "DeepSeek Chat", Efforts: []string{"low"}},
		{ID: "gpt-5.5", Name: "GPT-5.5"},
	}
	if m, ok := Lookup(known, "deepseek/deepseek-chat"); !ok || m.Name != "DeepSeek Chat" {
		t.Fatalf("prefixed lookup: %+v %v", m, ok)
	}
	if got := Efforts(known, "deepseek/deepseek-chat"); len(got) != 1 || got[0] != "low" {
		t.Fatalf("prefixed efforts: %v", got)
	}
	if m, ok := Lookup(known, "gpt-5.5"); !ok || m.Name != "GPT-5.5" {
		t.Fatalf("bare lookup: %+v %v", m, ok)
	}
	if m, ok := Lookup(known, "nope/unknown"); ok {
		t.Fatalf("unmatched id matched: %+v", m)
	}
	if got := BareID("a/b/c"); got != "c" {
		t.Fatalf("BareID: %q", got)
	}
}

func TestCatxDecorateMatchesPrefixedLiveIDs(t *testing.T) {
	known := []Model{{ID: "deepseek-chat", Name: "DeepSeek Chat", Efforts: []string{"low"}}}
	live := []Model{{ID: "deepseek/deepseek-chat"}, {ID: "unknown/thing"}}
	out := Decorate(live, known)
	if len(out) != 2 || out[0].ID != "deepseek/deepseek-chat" || out[0].Name != "DeepSeek Chat" || len(out[0].Efforts) != 1 {
		t.Fatalf("decorate: %+v", out)
	}
	if out[1].Name != "" || len(out[1].Efforts) != 0 {
		t.Fatalf("unmatched model gained metadata: %+v", out[1])
	}
}

func TestCatxProviderReadsSeveralCatalogsInOrder(t *testing.T) {
	writeCatalog(t, `{
	  "openai": {"models": {
	    "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","reasoning_options":[{"type":"effort","values":["low","high"]}]}
	  }},
	  "deepseek": {"models": {
	    "deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat"},
	    "gpt-5.5": {"id":"gpt-5.5","name":"Other GPT"}
	  }}
	}`)
	if ms := Provider("deepseek"); len(ms) != 2 {
		t.Fatalf("deepseek catalog: %+v", ms)
	}
	// Lookup over the union keeps the first catalog's entry for a shared id.
	union := append(Provider("openai"), Provider("deepseek")...)
	m, ok := Lookup(union, "gpt-5.5")
	if !ok || m.Name != "GPT-5.5" || len(m.Efforts) != 2 {
		t.Fatalf("shared id: %+v %v", m, ok)
	}
}
