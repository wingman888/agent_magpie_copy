package provider

import "testing"

func TestSaveNoKeyPathsDiffer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	keyless := Provider{ID: "snk-relay", Name: "Snk Relay", Chat: "http://[::1]:9/v1"}

	if err := Save(keyless); err == nil {
		t.Fatal("Save should still refuse a keyless non-local provider")
	}
	if err := SaveAllowNoKey(keyless); err != nil {
		t.Fatalf("SaveAllowNoKey: %v", err)
	}
	p, err := Find("snk-relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Ready() {
		t.Fatal("keyless provider should not be ready")
	}
}
