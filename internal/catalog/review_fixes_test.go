package catalog

import "testing"

func TestMatchUsesOnlyTheTailOfAPrefixedID(t *testing.T) {
	known := []Model{{ID: "deepseek-chat", Name: "DeepSeek Chat"}, {ID: "gpt-5.5", Name: "GPT-5.5"}}
	if m, ok := Match(known, "a/b/deepseek-chat"); !ok || m.ID != "deepseek-chat" {
		t.Errorf("Match(nested prefix) = %+v, %v", m, ok)
	}
	// a whole id containing a slash is not looked up as it is
	if _, ok := Match([]Model{{ID: "x/y"}}, "x/y"); ok {
		t.Error("prefixed id matched by its whole string")
	}
	if m, ok := Match(known, "unknown/gpt-5.5"); !ok || m.Name != "GPT-5.5" {
		t.Errorf("prefix does not matter for the tail: %+v, %v", m, ok)
	}
	if _, ok := Match(known, "deepseek/unknown"); ok {
		t.Error("unmatched tail matched")
	}
}
