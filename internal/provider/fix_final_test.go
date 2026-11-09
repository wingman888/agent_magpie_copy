package provider

import (
	"reflect"
	"testing"
)

// TestFixFinalPresetSaveClearsIcon: Save does not write an icon for a preset
// into providers.json; the preset's own icon comes back on read via normalize.
func TestFixFinalPresetSaveClearsIcon(t *testing.T) {
	customHdrsEnv(t)
	p, err := FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "sk-test"
	p.Icon = "file:0123456789abcdef.png" // a stale value must not be stored for a preset
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	for _, s := range load().Providers {
		if s.ID == "deepseek" && s.Icon != "" {
			t.Errorf("stored preset icon = %q, want empty", s.Icon)
		}
	}
	got, err := Find("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if got.Icon != "deepseek-color" {
		t.Errorf("preset icon on read = %q, want deepseek-color", got.Icon)
	}
}

// TestFixFinalCleanHeadersDeterministic: raw keys that trim to the same name
// resolve the same way on every call, the later raw key in sorted order winning.
func TestFixFinalCleanHeadersDeterministic(t *testing.T) {
	in := map[string]string{"X-A": "1", " X-A": "2", "\tX-A ": "3"}
	// sorted raw keys: "\tX-A " (0x09) < " X-A" (0x20) < "X-A" (0x58);
	// "X-A" comes last, so its value "1" wins.
	want := map[string]string{"X-A": "1"}
	for i := 0; i < 50; i++ {
		if got := cleanHeaders(in); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: cleanHeaders = %v, want %v", i, got, want)
		}
	}
}
