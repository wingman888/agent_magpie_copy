package provider

import (
	"testing"
)

func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func TestSaveImportedAcceptsKeylessLink(t *testing.T) {
	isolateConfig(t)
	p := mustParse(t, "magpie://import?name=Nokey%20Relay&chat=https://nokey.example/v1")
	if err := SaveImported(p); err != nil {
		t.Fatalf("SaveImported without key: %v", err)
	}
	got, err := Find("nokey-relay")
	if err != nil {
		t.Fatalf("saved provider not found: %v", err)
	}
	if got.Ready() {
		t.Errorf("keyless provider should not be ready")
	}
}

func TestSaveImportedReplacesSameID(t *testing.T) {
	isolateConfig(t)
	first := mustParse(t, "magpie://import?name=Acme&chat=https://a.example/v1&key=sk-first")
	second := mustParse(t, "magpie://import?name=Acme&chat=https://b.example/v1&key=sk-second")
	if err := SaveImported(first); err != nil {
		t.Fatal(err)
	}
	if err := SaveImported(second); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range All() {
		if p.ID == "acme" {
			n++
			if p.Chat != "https://b.example/v1" || p.Key != "sk-second" {
				t.Errorf("provider not replaced: chat=%q key=%q", p.Chat, p.Key)
			}
		}
	}
	if n != 1 {
		t.Errorf("found %d providers with id acme, want 1", n)
	}
}

func TestSaveImportedRejectsReservedID(t *testing.T) {
	isolateConfig(t)
	p := Provider{ID: "magpie", Name: "magpie", Chat: "https://x.example/v1"}
	if err := SaveImported(p); err == nil {
		t.Fatal("reserved id magpie must be refused")
	}
}
