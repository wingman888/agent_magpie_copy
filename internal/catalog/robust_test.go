package catalog

import "testing"

// Lookups and decorations with nothing to search must give empty answers, not panic.
func TestLookupAndDecorateWithNothingToSearch(t *testing.T) {
	if _, ok := Lookup(nil, "gpt-5.5"); ok {
		t.Fatal("Lookup with no catalogs found a model")
	}
	if _, ok := Lookup([]string{"", " "}, "deepseek/"); ok {
		t.Fatal("Lookup over blank catalog names found a model")
	}
	if _, ok := Lookup([]string{"nosuch-catalog"}, ""); ok {
		t.Fatal("Lookup of an empty id found a model")
	}
	if got := Decorate(nil, nil); len(got) != 0 {
		t.Fatalf("Decorate(nil, nil) = %d models, want 0", len(got))
	}
	live := []Model{{ID: "deepseek/deepseek-chat"}}
	if got := Decorate(live, nil); len(got) != 1 || got[0].ID != "deepseek/deepseek-chat" {
		t.Fatalf("Decorate with no catalog = %+v, want the live model kept", got)
	}
	if got := ProviderList(""); len(got) != 0 {
		t.Fatalf("ProviderList(\"\") = %d models, want 0", len(got))
	}
	if got := ProviderList(" , ,"); len(got) != 0 {
		t.Fatalf("ProviderList of blanks = %d models, want 0", len(got))
	}
	if got := Provider(""); len(got) != 0 {
		t.Fatalf("Provider(\"\") = %d models, want 0", len(got))
	}
}
