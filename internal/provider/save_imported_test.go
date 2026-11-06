package provider

import (
	"path/filepath"
	"testing"
)

func isolateSaveImported(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// Save must keep refusing a non-local endpoint without a key (provider add).
func TestSaveStillRequiresKeyForRemoteEndpoint(t *testing.T) {
	isolateSaveImported(t)
	err := Save(Provider{ID: "remote-relay", Name: "Remote Relay", Chat: "https://relay.example/v1"})
	if err == nil {
		t.Fatal("Save accepted a remote provider without a key")
	}
	if _, err := Find("remote-relay"); err == nil {
		t.Fatal("rejected provider was saved")
	}
}

// SaveImported accepts the same provider without a key and stores it.
func TestSaveImportedAllowsMissingKeyForRemoteEndpoint(t *testing.T) {
	isolateSaveImported(t)
	if err := SaveImported(Provider{ID: "remote-relay", Name: "Remote Relay", Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("remote-relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "" {
		t.Fatalf("key = %q, want empty", p.Key)
	}
	if p.Ready() {
		t.Fatal("provider without a key reports Ready")
	}
}

// SaveImported relaxes only the key check; the other rules still apply.
func TestSaveImportedStillChecksOtherRules(t *testing.T) {
	isolateSaveImported(t)
	if err := SaveImported(Provider{ID: "magpie", Name: "x", Chat: "https://relay.example/v1"}); err == nil {
		t.Fatal("reserved id accepted")
	}
	if err := SaveImported(Provider{ID: "", Name: "—", Chat: "https://relay.example/v1"}); err == nil {
		t.Fatal("empty id accepted")
	}
	if err := SaveImported(Provider{ID: "no-url", Name: "No URL"}); err == nil {
		t.Fatal("provider without a base URL accepted")
	}
}

// A keyed import goes through the same path and stores the key.
func TestSaveImportedWithKey(t *testing.T) {
	isolateSaveImported(t)
	if err := SaveImported(Provider{ID: "keyed", Name: "Keyed", Chat: "https://relay.example/v1", Key: "sk-abc"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("keyed")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "sk-abc" {
		t.Fatalf("key = %q", p.Key)
	}
}
