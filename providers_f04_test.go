package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// After `provider icon <id> ""` the icon key is not written to providers.json.
func TestF04IconResetOmitsIconField(t *testing.T) {
	f04Isolate(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-test", Icon: "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	f04Capture(t, func() {
		if err := providerCmd([]string{"provider", "icon", "relay", ""}); err != nil {
			t.Fatal(err)
		}
	})
	b, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(f.Providers))
	}
	if _, ok := f.Providers[0]["icon"]; ok {
		t.Errorf("icon field written after reset: %s", b)
	}
}

// The usage text names the header key form, the icon key and the icon command.
func TestF04UsageListsNewKeys(t *testing.T) {
	for _, want := range []string{"header.X-Foo", "icon", "provider icon"} {
		if !strings.Contains(providerUsage, want) {
			t.Errorf("providerUsage missing %q", want)
		}
	}
}
