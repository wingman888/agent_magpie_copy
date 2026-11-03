package provider

import (
	"reflect"
	"testing"
)

func TestSetFallbackCleansAndClears(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := SetFallback("relay", []string{" deepseek/deepseek-chat ", "", "openai/gpt-5.5", "deepseek/deepseek-chat", "  "}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"deepseek/deepseek-chat", "openai/gpt-5.5"}
	if !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("fallback %q, want %q", p.Fallback, want)
	}

	// saving the provider without naming a fallback keeps the list
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k2", Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("relay"); !reflect.DeepEqual(p.Fallback, want) {
		t.Errorf("save dropped the fallback: %q", p.Fallback)
	}

	if err := SetFallback("relay", nil); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("relay"); len(p.Fallback) != 0 {
		t.Errorf("clear left %q", p.Fallback)
	}
	if err := SetFallback("nobody", []string{"a/b"}); err == nil {
		t.Error("unknown provider accepted")
	}
}

func TestSplitFallback(t *testing.T) {
	for _, c := range []struct {
		ref, id, model string
		ok             bool
	}{
		{"deepseek/deepseek-chat", "deepseek", "deepseek-chat", true},
		{" openai/gpt-5.5 ", "openai", "gpt-5.5", true},
		{"router/vendor/model", "router", "vendor/model", true},
		{"nomodel", "", "", false},
		{"/model", "", "", false},
		{"provider/", "", "", false},
	} {
		id, model, ok := SplitFallback(c.ref)
		if id != c.id || model != c.model || ok != c.ok {
			t.Errorf("SplitFallback(%q) = %q %q %v", c.ref, id, model, ok)
		}
	}
}

func TestNormalizeCleansFallback(t *testing.T) {
	got := cleanList([]string{" a/b ", "a/b", ""})
	if !reflect.DeepEqual(got, []string{"a/b"}) {
		t.Errorf("cleanList: %q", got)
	}
}
