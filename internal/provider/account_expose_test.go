package provider

import (
	"fmt"
	"testing"

	"github.com/yetone/dial/internal/catalog"
)

// A borrowed login exposes every model it serves, even past the threshold
// that hides the list of a plain provider.
func TestAccountExposesAllModelsOverThreshold(t *testing.T) {
	sandbox(t)
	signInCopilot(t, "gho_test")

	var ms []catalog.Model
	for i := 0; i < manyModels+6; i++ {
		ms = append(ms, catalog.Model{ID: fmt.Sprintf("model-%02d", i), Name: fmt.Sprintf("Model %02d", i)})
	}
	if err := catalog.SaveLive("copilot", "https://api.example.invalid", ms); err != nil {
		t.Fatal(err)
	}

	p := mustAccount(t, "copilot")
	if got := len(p.Available()); got != len(ms) {
		t.Fatalf("Available() = %d models; want %d", got, len(ms))
	}
	if got := len(p.Exposed()); got != len(ms) {
		t.Fatalf("Exposed() = %d models; want all %d", got, len(ms))
	}
}
