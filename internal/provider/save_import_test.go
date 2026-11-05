package provider

import (
	"strings"
	"testing"
)

func isolateSave(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// Save (provider add) must keep refusing a remote provider without a key.
func TestSaveStillRefusesKeylessRemote(t *testing.T) {
	isolateSave(t)
	err := Save(Provider{Name: "Remote", Chat: "https://relay.example/v1"})
	if err == nil || !strings.Contains(err.Error(), "needs an API key") {
		t.Fatalf("Save err = %v, want needs an API key", err)
	}
	if len(All()) != 0 {
		t.Fatalf("keyless remote provider was saved by Save")
	}
}

// SaveImported accepts the same keyless remote provider, saved unavailable.
func TestSaveImportedAcceptsKeylessRemote(t *testing.T) {
	isolateSave(t)
	if err := SaveImported(Provider{Name: "Remote", Chat: "https://relay.example/v1"}); err != nil {
		t.Fatalf("SaveImported: %v", err)
	}
	p, err := Find("remote")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if p.Ready() {
		t.Errorf("keyless remote provider should not be Ready")
	}
}

// SaveImported still enforces the base URL requirement.
func TestSaveImportedNeedsBaseURL(t *testing.T) {
	isolateSave(t)
	if err := SaveImported(Provider{Name: "Empty"}); err == nil {
		t.Fatalf("SaveImported accepted a provider with no base URL")
	}
}
