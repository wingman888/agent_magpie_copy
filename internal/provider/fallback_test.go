package provider

import (
	"reflect"
	"testing"
)

func TestFbxFallbackListNormalized(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Fallback: []string{"  openai/gpt-5 ", "", "openai/gpt-5", "deepseek/deepseek-chat", "  "}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"openai/gpt-5", "deepseek/deepseek-chat"}
	if !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("fallback = %q, want %q", p.Fallback, want)
	}
}

func TestFbxFallbackClearedByEmptyList(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Fallback: []string{"openai/gpt-5"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	p.Fallback = []string{""}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	p, err = Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Fallback) != 0 {
		t.Fatalf("fallback = %q, want none", p.Fallback)
	}
}
